package providerresources

import (
	"regexp"
	"strings"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
)

var inventoryBucketOwner = regexp.MustCompile(`^\d{12}$`)
var inventoryBucketRegion = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-\d+$`)

// AttachableIdentity requires physical identity except for S3's explicit
// account-and-name contract. S3 does not expose a stable bucket incarnation.
func AttachableIdentity(r *ProviderResource) bool {
	if r == nil || r.AccountIdentity == "" {
		return false
	}
	if r.Kind == "object-storage" {
		if _, ok := s3BucketIdentity(r); ok {
			return true
		}
		return gcsBucketIdentity(r) && r.Incarnation != ""
	}
	return r.Incarnation != ""
}

func s3BucketIdentity(r *ProviderResource) (string, bool) {
	if r == nil || r.Provider != connections.AWS || r.Kind != "object-storage" || !catalogconnect.ValidBucketName(r.Provider, r.Name) || r.ExternalID != "arn:aws:s3:::"+r.Name || !inventoryBucketRegion.MatchString(r.Region) || strings.HasPrefix(r.Region, "cn-") {
		return "", false
	}
	parts := strings.Split(r.Scope, "/")
	if len(parts) != 5 || parts[0] != "account" || !inventoryBucketOwner.MatchString(parts[1]) || parts[2] != "regions" || parts[3] != r.Region || parts[4] != "managed-resources" {
		return "", false
	}
	expected := connections.AccountIdentity(connections.AWS, parts[1], map[string]string{"region": "account-inventory"})
	return parts[1], expected != "" && expected == r.AccountIdentity
}

func resourceEngine(r *ProviderResource) string {
	if r.Kind == "object-storage" {
		switch r.Provider {
		case connections.AWS:
			return catalogconnect.EngineS3
		case connections.GCP:
			return catalogconnect.EngineGCS
		}
	}
	return engine(r.Kind)
}

func (s *Service) decorateBucket(r *ProviderResource) {
	r.IdentityScope = "unverified"
	r.BucketOwner = ""
	if r.Incarnation != "" {
		r.IdentityScope = "physical"
	}
	if owner, ok := s3BucketIdentity(r); ok {
		r.IdentityScope = "account-name"
		r.BucketOwner = owner
	}
	if r.Kind == "object-storage" && (r.Provider == connections.AWS || r.Provider == connections.GCP) && AttachableIdentity(r) && r.Status != "missing" && s.deps.Endpoints != nil {
		r.CredentialMethods = []string{"bucket_access"}
	}
}

func gcsBucketIdentity(r *ProviderResource) bool {
	if r == nil || r.Provider != connections.GCP || r.Kind != "object-storage" || !catalogconnect.ValidBucketName(r.Provider, r.Name) || r.Region == "" {
		return false
	}
	parts := strings.Split(r.ExternalID, "/")
	if len(parts) != 4 || parts[0] != "projects" || parts[1] == "" || parts[2] != "buckets" || parts[3] != r.Name || r.Scope != "projects/"+parts[1]+"/all-regions/managed-resources" {
		return false
	}
	return connections.AccountIdentity(connections.GCP, "", map[string]string{"project_id": parts[1]}) == r.AccountIdentity
}

func bucketAccessMatches(r *ProviderResource, a catalogconnect.BucketAccess) bool {
	if r.Kind != "object-storage" || !AttachableIdentity(r) || a.Provider != r.Provider || a.Bucket != r.Name || a.Region != r.Region {
		return false
	}
	if r.Provider == connections.AWS {
		owner, ok := s3BucketIdentity(r)
		return ok && a.ExpectedOwner == owner
	}
	return a.ExpectedOwner == "" && gcsBucketIdentity(r)
}
func normalizeBucketAccess(r *ProviderResource, a catalogconnect.BucketAccess) (string, error) {
	if a.Provider == "" {
		a.Provider = r.Provider
	}
	if a.Bucket == "" {
		a.Bucket = r.Name
	}
	if a.Region == "" {
		a.Region = r.Region
	}
	if a.ExpectedOwner == "" && r.Provider == connections.AWS {
		a.ExpectedOwner, _ = s3BucketIdentity(r)
	}
	if !bucketAccessMatches(r, a) {
		return "", ErrInvalid
	}
	raw, err := catalogconnect.EncodeBucketAccess(a)
	if err != nil {
		return "", ErrInvalid
	}
	return raw, nil
}
func bucketEndpointMatches(r *ProviderResource, e *catalogconnect.Endpoint) bool {
	if e == nil || e.OrgID != r.OrgID || !AttachableIdentity(r) || e.Engine != resourceEngine(r) || e.Database != r.Name || e.Port != 443 || e.TLS != catalogconnect.TLSVerifyFull {
		return false
	}
	host := "storage.googleapis.com"
	if r.Provider == connections.AWS {
		host = "s3." + r.Region + ".amazonaws.com"
	}
	return e.Host == host && e.Username == ""
}
