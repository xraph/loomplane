package cloudprofiles

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
)

// FailureDomain records a fact and its provenance. A name alone proves no isolation.
type FailureDomain struct {
	Kind          string   `json:"kind"`
	ID            string   `json:"id"`
	Source        string   `json:"source"`
	Verified      bool     `json:"verified"`
	DependencyIDs []string `json:"dependency_ids,omitempty"`
}

// CloudLocation describes placement without moving any attached workload.
type CloudLocation struct {
	EvidenceSource   string          `json:"evidence_source,omitempty"`
	EvidenceRef      string          `json:"evidence_ref,omitempty"`
	VerifiedBy       string          `json:"verified_by,omitempty"`
	ID               string          `json:"id" forge:"id"`
	OrgID            string          `json:"org_id"`
	CloudID          string          `json:"cloud_id"`
	Name             string          `json:"name"`
	Provider         string          `json:"provider"`
	AccountID        string          `json:"account_id,omitempty"`
	ConnectionID     string          `json:"connection_id,omitempty"`
	Region           string          `json:"region"`
	NetworkRefs      []string        `json:"network_refs,omitempty"`
	CIDRs            []string        `json:"cidrs,omitempty"`
	FailureDomains   []FailureDomain `json:"failure_domains"`
	EvidenceRevision int64           `json:"evidence_revision"`
	CheckedAt        time.Time       `json:"checked_at"`
	Revision         int64           `json:"revision"`
}

// LocationInput pins the Cloud and location revisions. Public saves record unverified facts.
type LocationInput struct {
	ID               string          `json:"id,omitempty"`
	Name             string          `json:"name"`
	Provider         string          `json:"provider"`
	AccountID        string          `json:"account_id"`
	ConnectionID     string          `json:"connection_id"`
	Region           string          `json:"region"`
	NetworkRefs      []string        `json:"network_refs,omitempty"`
	CIDRs            []string        `json:"cidrs,omitempty"`
	FailureDomains   []FailureDomain `json:"failure_domains,omitempty"`
	EvidenceRevision int64           `json:"evidence_revision"`
	CheckedAt        time.Time       `json:"checked_at"`
	Revision         int64           `json:"revision"`
	CloudRevision    int64           `json:"cloud_revision"`
}

// LocationStore uses the same explicit project scope as target reads.
type LocationStore interface {
	SaveLocation(context.Context, *CloudLocation, int64, int64) error
	GetLocation(context.Context, string, string, string, string) (*CloudLocation, error)
	ListLocations(context.Context, string, string, string) ([]CloudLocation, error)
	DeleteLocation(context.Context, string, string, string, int64, int64) error
}

func normalizeLocation(in LocationInput) (*CloudLocation, error) {
	value, err := name(in.Name)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Provider) == "" || strings.TrimSpace(in.AccountID) == "" || strings.TrimSpace(in.ConnectionID) == "" || strings.TrimSpace(in.Region) == "" || in.CloudRevision < 1 || in.Revision < 0 || (in.ID == "" && in.Revision != 0) || (in.ID != "" && in.Revision < 1) {
		return nil, ErrInvalid
	}
	if in.EvidenceRevision != 0 || !in.CheckedAt.IsZero() {
		return nil, fmt.Errorf("%w: verification requires saved authorized evidence", ErrInvalid)
	}
	if len(in.NetworkRefs) > 64 || len(in.CIDRs) > 64 || len(in.FailureDomains) > 64 {
		return nil, ErrInvalid
	}
	for _, cidr := range in.CIDRs {
		if _, err = netip.ParsePrefix(cidr); err != nil {
			return nil, fmt.Errorf("%w: invalid network CIDR", ErrInvalid)
		}
	}
	for _, domain := range in.FailureDomains {
		if domain.Verified || domain.Kind == "" || domain.ID == "" || (domain.Source != "operator" && domain.Source != "unknown") || len(domain.DependencyIDs) > 64 {
			return nil, fmt.Errorf("%w: failure domains need provenance and cannot claim verification", ErrInvalid)
		}
	}
	facts := append([]FailureDomain{}, in.FailureDomains...)
	return &CloudLocation{ID: in.ID, Name: value, Provider: strings.TrimSpace(in.Provider), AccountID: strings.TrimSpace(in.AccountID), ConnectionID: strings.TrimSpace(in.ConnectionID), Region: strings.TrimSpace(in.Region), NetworkRefs: append([]string{}, in.NetworkRefs...), CIDRs: append([]string{}, in.CIDRs...), FailureDomains: facts, Revision: in.Revision + 1}, nil
}

func sameLocationPlacement(a, b CloudLocation) bool {
	return a.Provider == b.Provider && a.AccountID == b.AccountID && a.Region == b.Region
}

func projectLocation(in CloudLocation) CloudLocation {
	in.AccountID, in.ConnectionID = "", ""
	in.EvidenceRef, in.VerifiedBy = "", ""
	in.NetworkRefs, in.CIDRs = nil, nil
	in.FailureDomains = append([]FailureDomain{}, in.FailureDomains...)
	for i := range in.FailureDomains {
		in.FailureDomains[i].DependencyIDs = nil
		switch in.FailureDomains[i].Kind {
		case "region", "zone", "datacenter":
		default:
			in.FailureDomains[i].ID = ""
		}
	}
	return in
}

func (s *Service) locationStore() (LocationStore, error) {
	st, ok := s.store.(LocationStore)
	if !ok {
		return nil, ErrInvalid
	}
	return st, nil
}

// SaveLocation records metadata with organization authority and immutable account identity.
func (s *Service) SaveLocation(ctx context.Context, p security.Principal, cloudID string, in LocationInput) (*CloudLocation, error) {
	if err := s.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID); err != nil {
		return nil, err
	}
	location, err := normalizeLocation(in)
	if err != nil {
		return nil, err
	}
	if placementErr := s.placement(ctx, p.OrgID, CreateTargetInput{Provider: location.Provider, ConnectionID: location.ConnectionID, Region: location.Region}); placementErr != nil {
		return nil, placementErr
	}
	all, err := s.connections.All(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	match := false
	for _, c := range all {
		if c.ID == location.ConnectionID && c.OrgID == p.OrgID && c.Status == connections.StatusReady && connections.AccountIdentity(c.Cloud, c.Account, c.Details) == location.AccountID {
			match = true
		}
	}
	if !match {
		return nil, fmt.Errorf("%w: account identity does not match the ready connection", ErrInvalid)
	}
	st, err := s.locationStore()
	if err != nil {
		return nil, err
	}
	if location.ID == "" {
		id, idErr := typeid.Generate("location")
		if idErr != nil {
			return nil, idErr
		}
		location.ID = id.String()
	}
	location.OrgID, location.CloudID = p.OrgID, cloudID
	if err := st.SaveLocation(ctx, location, in.CloudRevision, in.Revision); err != nil {
		return nil, err
	}
	s.record(ctx, p, "", "cloud_location.save", map[string]any{"cloud_id": cloudID, "location_id": location.ID, "revision": location.Revision})
	return location, nil
}

// GetLocation reads the manager view of one location.
func (s *Service) GetLocation(ctx context.Context, p security.Principal, cloudID, id string) (*CloudLocation, error) {
	if err := s.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID); err != nil {
		return nil, err
	}
	st, err := s.locationStore()
	if err != nil {
		return nil, err
	}
	return st.GetLocation(ctx, p.OrgID, "", cloudID, id)
}

// ListLocations reads the manager view of the Cloud's locations.
func (s *Service) ListLocations(ctx context.Context, p security.Principal, cloudID string) ([]CloudLocation, error) {
	if err := s.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID); err != nil {
		return nil, err
	}
	st, err := s.locationStore()
	if err != nil {
		return nil, err
	}
	return st.ListLocations(ctx, p.OrgID, "", cloudID)
}

// GetProjectLocation reads only locations reached by a current target grant.
func (s *Service) GetProjectLocation(ctx context.Context, p security.Principal, project, cloudID, id string) (*CloudLocation, error) {
	if project == "" {
		return nil, ErrInvalid
	}
	if err := s.scope(ctx, p, project, security.CloudAccountRead); err != nil {
		return nil, err
	}
	st, err := s.locationStore()
	if err != nil {
		return nil, err
	}
	location, err := st.GetLocation(ctx, p.OrgID, project, cloudID, id)
	if err != nil {
		return nil, err
	}
	value := projectLocation(*location)
	return &value, nil
}

// ListProjectLocations returns sanitized facts for locations of granted targets.
func (s *Service) ListProjectLocations(ctx context.Context, p security.Principal, project, cloudID string) ([]CloudLocation, error) {
	if project == "" {
		return nil, ErrInvalid
	}
	if err := s.scope(ctx, p, project, security.CloudAccountRead); err != nil {
		return nil, err
	}
	st, err := s.locationStore()
	if err != nil {
		return nil, err
	}
	locations, err := st.ListLocations(ctx, p.OrgID, project, cloudID)
	if err != nil {
		return nil, err
	}
	for i := range locations {
		locations[i] = projectLocation(locations[i])
	}
	return locations, nil
}

// DeleteLocation refuses locations still attached to targets or resources.
func (s *Service) DeleteLocation(ctx context.Context, p security.Principal, cloudID, id string, revision, cloudRevision int64) error {
	if err := s.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID); err != nil {
		return err
	}
	st, err := s.locationStore()
	if err != nil {
		return err
	}
	if err := st.DeleteLocation(ctx, p.OrgID, cloudID, id, revision, cloudRevision); err != nil {
		return err
	}
	s.record(ctx, p, "", "cloud_location.delete", map[string]any{"cloud_id": cloudID, "location_id": id, "revision": revision})
	return nil
}

// AttestedFailureDomain is one fact an authorized operator has checked.
type AttestedFailureDomain struct {
	Kind          string   `json:"kind"`
	ID            string   `json:"id"`
	DependencyIDs []string `json:"dependency_ids,omitempty"`
}

// LocationVerificationInput records a responsible operator's saved evidence.
// It does not claim that the provider observed independent failure domains.
type LocationVerificationInput struct {
	CloudRevision  int64                   `json:"cloud_revision"`
	Revision       int64                   `json:"revision"`
	EvidenceRef    string                  `json:"evidence_ref"`
	FailureDomains []AttestedFailureDomain `json:"failure_domains"`
}

func attestLocation(location CloudLocation, in LocationVerificationInput, actor string, now time.Time) (*CloudLocation, error) {
	ref, err := url.Parse(in.EvidenceRef)
	if err != nil || len(in.EvidenceRef) > 512 || strings.ContainsAny(in.EvidenceRef, " \t\n\r") || ref.User != nil || ref.RawQuery != "" || ref.Fragment != "" || (ref.Scheme != "https" && ref.Scheme != "audit" && ref.Scheme != "urn") || (ref.Scheme == "https" && ref.Host == "") || (ref.Host == "" && ref.Opaque == "") || actor == "" || now.IsZero() || in.CloudRevision < 1 {
		return nil, ErrInvalid
	}
	if in.Revision != location.Revision {
		return nil, ErrConflict
	}
	if len(in.FailureDomains) == 0 || len(in.FailureDomains) > 64 {
		return nil, ErrInvalid
	}
	// Retain unknown facts so an attestation cannot erase unknown correlations.
	facts := []FailureDomain{}
	for _, domain := range location.FailureDomains {
		if !domain.Verified {
			facts = append(facts, domain)
		}
	}
	for _, domain := range in.FailureDomains {
		if domain.Kind == "" || domain.ID == "" || domain.ID == "unknown" || len(domain.DependencyIDs) > 64 {
			return nil, ErrInvalid
		}
		facts = append(facts, FailureDomain{Kind: domain.Kind, ID: domain.ID, Source: "operator-attested", Verified: true, DependencyIDs: append([]string{}, domain.DependencyIDs...)})
	}
	if len(facts) > 64 {
		return nil, ErrInvalid
	}
	location.FailureDomains = facts
	location.Revision++
	location.EvidenceRevision = location.Revision
	location.CheckedAt = now.UTC()
	location.EvidenceSource = "operator-attested"
	location.EvidenceRef = in.EvidenceRef
	location.VerifiedBy = actor
	return &location, nil
}

// VerifyLocation saves an operator attestation against the current account and region.
func (s *Service) VerifyLocation(ctx context.Context, p security.Principal, cloudID, id string, in LocationVerificationInput) (*CloudLocation, error) {
	if err := s.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID); err != nil {
		return nil, err
	}
	st, err := s.locationStore()
	if err != nil {
		return nil, err
	}
	current, err := st.GetLocation(ctx, p.OrgID, "", cloudID, id)
	if err != nil {
		return nil, err
	}
	checked, err := attestLocation(*current, in, p.UserID, s.now())
	if err != nil {
		return nil, err
	}
	if err := st.SaveLocation(ctx, checked, in.CloudRevision, in.Revision); err != nil {
		return nil, err
	}
	s.record(ctx, p, "", "cloud_location.verify", map[string]any{"cloud_id": cloudID, "location_id": id, "revision": checked.Revision, "evidence_revision": checked.EvidenceRevision, "evidence_source": checked.EvidenceSource})
	return checked, nil
}
