package access

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
)

var ErrPolicy = errors.New("invalid federation policy")

// Compile builds default-deny policy from trusted, server-owned approval records.
// A revision hash binds approval to content; it does not authenticate the approver.
func Compile(in domain.PolicyInput) (domain.PolicyBundle, error) {
	return CompileAt(in, time.Now())
}

// CompileAt permits deterministic compilation using the controller's current time.
func CompileAt(in domain.PolicyInput, now time.Time) (domain.PolicyBundle, error) {
	denied := domain.PolicyBundle{}
	if !in.Valid() || in.Generation == 0 || now.IsZero() || len(in.Exports)+len(in.Grants) > domain.MaxSnapshotRecords {
		return denied, ErrPolicy
	}
	exports := make(map[string]domain.ServiceExport, len(in.Exports))
	revisions := make(map[string]string, len(in.Exports))
	for _, export := range in.Exports {
		canonical, err := canonicalExport(export)
		if err != nil || export.OrgID != in.OrgID {
			return denied, ErrPolicy
		}
		if _, exists := exports[export.ID]; exists {
			return denied, ErrPolicy
		}
		exports[export.ID] = canonical
		revisions[export.ID], err = ExportRevision(canonical)
		if err != nil {
			return denied, err
		}
	}
	bundle := domain.PolicyBundle{Scope: in.Scope, Generation: in.Generation, ExpiresAt: now.Add(domain.MaxPolicyLease)}
	seen := make(map[string]bool, len(in.Grants))
	used := make(map[string]bool)
	for _, grant := range in.Grants {
		canonical, err := canonicalGrant(grant)
		if err != nil || grant.OrgID != in.OrgID || grant.Source.OrgID != in.OrgID || seen[grant.ID] {
			return denied, ErrPolicy
		}
		seen[grant.ID] = true
		if grant.Scope != in.Scope && grant.Source.Scope != in.Scope {
			continue
		}
		if grant.RevokedAt != nil || (grant.ExpiresAt != nil && !grant.ExpiresAt.After(now)) {
			continue
		}
		revision, err := GrantRevision(canonical)
		if err != nil {
			return denied, err
		}
		export, exists := exports[grant.ExportID]
		if !exists || grant.ApprovedRevision != revision || grant.ExportRevision != revisions[grant.ExportID] || grant.Scope != export.Scope || grant.Port != export.Port || grant.Protocol != export.Protocol {
			continue
		}
		bundle.Grants = append(bundle.Grants, canonical)
		if !used[export.ID] {
			bundle.Exports = append(bundle.Exports, export)
			used[export.ID] = true
		}
		if grant.ExpiresAt != nil && grant.ExpiresAt.Before(bundle.ExpiresAt) {
			bundle.ExpiresAt = *grant.ExpiresAt
		}
	}
	slices.SortFunc(bundle.Grants, func(a, b domain.AccessGrant) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(bundle.Exports, func(a, b domain.ServiceExport) int { return strings.Compare(a.ID, b.ID) })
	// Renewal changes the lease, not the content revision. Generation still fences
	// revocations and subsequent restoration of otherwise identical rules.
	content := bundle
	content.ExpiresAt = time.Time{}
	var err error
	bundle.Revision, err = policyDigest("bundle", content)
	if err != nil {
		return denied, err
	}
	encoded, err := json.Marshal(bundle)
	if err != nil || len(encoded) > domain.MaxSnapshotBytes/2 {
		return denied, ErrPolicy
	}
	return bundle, nil
}

// ExportRevision binds a grant to the exact service contract and destinations.
func ExportRevision(export domain.ServiceExport) (string, error) {
	canonical, err := canonicalExport(export)
	if err != nil {
		return "", err
	}
	return policyDigest("export", canonical)
}

// GrantRevision covers every permission and its owner, excluding approval state.
func GrantRevision(grant domain.AccessGrant) (string, error) {
	canonical, err := canonicalGrant(grant)
	if err != nil {
		return "", err
	}
	canonical.ApprovedRevision = ""
	canonical.RevokedAt = nil
	return policyDigest("grant", canonical)
}

func canonicalExport(export domain.ServiceExport) (domain.ServiceExport, error) {
	if !validMetadata(export.Metadata) || !domain.ValidIdentitySegment(export.ServiceID) || export.Port == 0 || !validProtocol(export.Protocol) || (export.IdentityProfile != domain.WorkloadIdentity && export.IdentityProfile != domain.GatewayIdentity) || len(export.DestinationIDs) == 0 || len(export.DestinationIDs) > domain.MaxSnapshotRecords {
		return domain.ServiceExport{}, ErrPolicy
	}
	var err error
	export.DestinationIDs, err = canonicalStrings(export.DestinationIDs, domain.ValidIdentitySegment)
	if err != nil {
		return domain.ServiceExport{}, err
	}
	export.CreatedAt, export.UpdatedAt = time.Time{}, time.Time{}
	return export, nil
}

func canonicalGrant(grant domain.AccessGrant) (domain.AccessGrant, error) {
	if !validMetadata(grant.Metadata) || !grant.Source.Valid() || grant.Source.OrgID != grant.OrgID || !domain.ValidIdentitySegment(grant.Source.DestinationID) || !domain.ValidIdentitySegment(grant.Source.ServiceID) || !domain.ValidIdentitySegment(grant.ExportID) || !domain.ValidIdentitySegment(grant.OwnerID) || !validRevision(grant.ExportRevision) || grant.Port == 0 || !validProtocol(grant.Protocol) {
		return domain.AccessGrant{}, ErrPolicy
	}
	if len(grant.HTTPMethods) > 16 || len(grant.HTTPPaths) > 32 || len(grant.GRPCMethods) > 32 || (grant.Protocol != domain.HTTP && (len(grant.HTTPMethods)+len(grant.HTTPPaths) > 0)) || (grant.Protocol != domain.GRPC && len(grant.GRPCMethods) > 0) {
		return domain.AccessGrant{}, ErrPolicy
	}
	var err error
	grant.HTTPMethods, err = canonicalStrings(grant.HTTPMethods, domain.ValidHTTPMethod)
	if err != nil {
		return domain.AccessGrant{}, err
	}
	grant.HTTPPaths, err = canonicalStrings(grant.HTTPPaths, domain.ValidHTTPPath)
	if err != nil {
		return domain.AccessGrant{}, err
	}
	grant.GRPCMethods, err = canonicalStrings(grant.GRPCMethods, domain.ValidGRPCMethod)
	if err != nil {
		return domain.AccessGrant{}, err
	}
	grant.CreatedAt, grant.UpdatedAt = time.Time{}, time.Time{}
	if grant.ExpiresAt != nil {
		stamp := grant.ExpiresAt.UTC()
		grant.ExpiresAt = &stamp
	}
	if grant.RevokedAt != nil {
		stamp := grant.RevokedAt.UTC()
		grant.RevokedAt = &stamp
	}
	return grant, nil
}
func canonicalStrings(values []string, valid func(string) bool) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := slices.Clone(values)
	slices.Sort(result)
	for i, value := range result {
		if !valid(value) || (i > 0 && result[i-1] == value) {
			return nil, ErrPolicy
		}
	}
	return result, nil
}
func validMetadata(m domain.Metadata) bool {
	return m.Valid() && domain.ValidIdentitySegment(m.ID) && m.Generation > 0
}
func validProtocol(protocol domain.Protocol) bool {
	return protocol == domain.HTTP || protocol == domain.GRPC || protocol == domain.TCP
}
func policyDigest(kind string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", ErrPolicy
	}
	digest := sha256.Sum256(append([]byte("loomplane/federation/"+kind+"/v1\x00"), data...))
	return hex.EncodeToString(digest[:]), nil
}
