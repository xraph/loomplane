package cloudinfra

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/idna"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// Zone grants its Name namespace independently of the compute account or Cloud.
// ProviderZone identifies the hosting zone, which may be an ancestor of Name.
type Zone struct {
	ID                   string           `json:"id" forge:"id"`
	OrgID                string           `json:"org_id"`
	Name                 string           `json:"name"`
	Mode                 string           `json:"mode"`
	ConnectionID         string           `json:"connection_id,omitempty"`
	ConnectionGeneration int64            `json:"connection_generation,omitempty"`
	Provider             string           `json:"provider,omitempty"`
	AccountID            string           `json:"account_id,omitempty"`
	RecordOwnership      bool             `json:"record_ownership"`
	ProviderZone         provider.DNSZone `json:"provider_zone"`
	ProjectIDs           []string         `json:"project_ids"`
	ProofName            string           `json:"proof_name,omitempty"`
	ProofValue           string           `json:"proof_value,omitempty"`
	ProofExpiresAt       time.Time        `json:"proof_expires_at"`
	ProofCheckedAt       time.Time        `json:"proof_checked_at"`
	VerifiedAt           time.Time        `json:"verified_at"`
	// ProofRecord is the receipt for the TXT record Loomplane published for
	// a managed zone's proof. Only that exact record is ever changed.
	ProofRecord *provider.DNSRecord `json:"proof_record,omitempty"`
	// ProofPublishAttempted says a write of the proof was sent and its
	// answer isn't saved, so the next try only reconciles.
	ProofPublishAttempted bool `json:"proof_publish_attempted,omitempty"`
	// ProofPublishAttemptedAt is when the last write of the proof was sent.
	ProofPublishAttemptedAt time.Time `json:"proof_publish_attempted_at"`
	// ProofPublishError is why the last publish failed, for the owner.
	ProofPublishError string `json:"proof_publish_error,omitempty"`
	// RegistrationID names the registration that bought the domain, if one did.
	RegistrationID string `json:"registration_id,omitempty"`
	// ChallengeConnectionID is the second Cloudflare connection whose token
	// answers DNS-01 challenges for this zone's wildcard routes.
	ChallengeConnectionID string `json:"challenge_connection_id,omitempty"`
	Revision              int64  `json:"revision"`
}

// Wildcards reports whether routes under the zone may use wildcard
// hostnames: a Cloudflare zone with a DNS challenge connection attached.
func (z Zone) Wildcards() bool {
	return z.Mode == "managed" && z.Provider == connections.Cloudflare && z.ChallengeConnectionID != "" && z.ownsProviderZone()
}

// ownsProviderZone is a zone that is its Cloudflare zone, not a namespace
// inside a larger one. A Cloudflare token reaches whole zones, so a
// challenge token for shop.acme.dev would edit all of acme.dev.
func (z Zone) ownsProviderZone() bool {
	return z.ProviderZone.ID != "" && z.ProviderZone.Name == z.Name
}

type ZoneInput struct {
	Name         string   `json:"name"`
	Mode         string   `json:"mode"`
	ConnectionID string   `json:"connection_id,omitempty"`
	ProjectIDs   []string `json:"project_ids"`
}

type DomainResolver interface {
	LookupTXT(context.Context, string) ([]string, error)
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// NormalizeHostname accepts exact DNS hostnames, including IDNs and the apex.
// Wildcards, ports, IP literals and URLs cannot become ownership claims.
func NormalizeHostname(value string) (string, error) {
	value = strings.TrimSuffix(strings.TrimSpace(value), ".")
	host, err := idna.Lookup.ToASCII(strings.ToLower(value))
	if err != nil || len(host) > 253 || !strings.Contains(host, ".") || net.ParseIP(host) != nil {
		return "", ErrInvalid
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", ErrInvalid
		}
		for _, ch := range label {
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
				return "", ErrInvalid
			}
		}
	}
	return host, nil
}

func zoneRelative(host, zone string) (string, error) {
	if host == zone {
		return "", nil
	}
	if !strings.HasSuffix(host, "."+zone) {
		return "", ErrInvalid
	}
	return strings.TrimSuffix(host, "."+zone), nil
}

func (s *Service) domainStore() (DomainStore, error) {
	if s.Domains != nil {
		return s.Domains, nil
	}
	if store, ok := s.Store.(DomainStore); ok {
		return store, nil
	}
	return nil, ErrInvalid
}

func (s *Service) domainResolver() DomainResolver {
	if s.Resolver != nil {
		return s.Resolver
	}
	return net.DefaultResolver
}

func (s *Service) domainScope(ctx context.Context, p security.Principal, project string, write bool) error {
	if p.OrgID == "" || p.UserID == "" {
		return security.ErrUnauthenticated
	}
	if write && p.ReadOnly {
		return security.ErrForbidden
	}
	permission, scope := security.OrganizationCloudManage, p.OrgID
	if project != "" {
		permission, scope = security.InfrastructureRead, project
		if write {
			permission = security.InfrastructureModify
		}
	}
	return s.Authz.Require(ctx, p, permission, scope)
}

func (s *Service) CreateZone(ctx context.Context, p security.Principal, in ZoneInput) (*Zone, error) {
	if err := s.domainScope(ctx, p, "", true); err != nil {
		return nil, err
	}
	name, err := NormalizeHostname(in.Name)
	// A zone without grants is usable by no project until granted.
	if err != nil || (in.Mode != "managed" && in.Mode != "external") {
		return nil, ErrInvalid
	}
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	var zone *Zone
	if in.Mode == "managed" {
		zone, err = s.createManagedZone(ctx, p, name, in.ConnectionID, in.ProjectIDs)
	} else {
		zone, err = s.externalZone(ctx, st, p, name, in)
	}
	if err != nil {
		return nil, err
	}
	if checkErr := st.CreateZone(ctx, zone); checkErr != nil {
		return nil, checkErr
	}
	s.domainAudit(ctx, p, "domain.zone.write", zone.ID, "")
	// The zone exists whether or not its proof publishes; a failure is on
	// the zone, and Check again or the background check retries it.
	if publishErr := s.publishZoneProof(ctx, st, zone); publishErr != nil {
		s.warn("Publish zone proof", publishErr)
	}
	return zone, nil
}

func zoneProjects(ctx context.Context, st DomainStore, org string, projectIDs []string) error {
	for _, project := range projectIDs {
		if project == "" {
			return ErrInvalid
		}
		if err := st.DomainProject(ctx, org, project); err != nil {
			return err
		}
	}
	return nil
}

// createManagedZone builds a zone in the most specific zone the connection's
// DNS provider hosts. It waits on the TXT proof like an external zone. The
// caller stores it, then publishes the proof with publishZoneProof.
func (s *Service) createManagedZone(ctx context.Context, p security.Principal, name, connectionID string, projectIDs []string) (*Zone, error) {
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	if checkErr := zoneProjects(ctx, st, p.OrgID, projectIDs); checkErr != nil {
		return nil, checkErr
	}
	id, err := randomID("zone")
	if err != nil {
		return nil, err
	}
	zone := &Zone{ID: id, OrgID: p.OrgID, Name: name, Mode: "managed", ConnectionID: connectionID, ProjectIDs: append([]string{}, projectIDs...), Revision: 1}
	conn, raw, err := s.Connections.Credential(ctx, p.OrgID, connectionID)
	if err != nil {
		return nil, err
	}
	cred, err := Credential(conn, raw)
	if err != nil {
		return nil, err
	}
	adapter, err := s.Adapters.DNS(conn.Cloud, provider.DNSZoneDiscovery, provider.DNSRecordDiscovery, provider.DNSRecordUpsert, provider.DNSRecordDeletion)
	if err != nil {
		return nil, err
	}
	if ownership, ok := adapter.(provider.DNSRecordOwnership); ok {
		zone.RecordOwnership = ownership.SupportsDNSRecordOwnership()
	}
	// Hosting access selects the record-writing account. Public TXT proves
	// control only over the requested namespace, even in a larger hosted zone.
	zones, err := adapter.ListZones(ctx, cred)
	if err != nil {
		return nil, err
	}
	for _, candidate := range zones {
		if _, scopeErr := zoneRelative(name, candidate.Name); scopeErr == nil && len(candidate.Name) > len(zone.ProviderZone.Name) {
			zone.ProviderZone = candidate
		}
	}
	if zone.ProviderZone.ID == "" {
		return nil, provider.ErrInfrastructureNotFound
	}
	current, err := adapter.GetZone(ctx, cred, zone.ProviderZone.Name)
	if err != nil {
		return nil, err
	}
	if current.ID != zone.ProviderZone.ID || current.Name != zone.ProviderZone.Name || current.AccountID != cred.AccountID {
		return nil, provider.ErrInfrastructureIdentity
	}
	zone.ProviderZone = current
	zone.Provider, zone.AccountID, zone.ConnectionGeneration = conn.Cloud, cred.AccountID, conn.Generation
	if proofErr := s.renewZoneProof(zone); proofErr != nil {
		return nil, proofErr
	}
	return zone, nil
}

func (s *Service) externalZone(ctx context.Context, st DomainStore, p security.Principal, name string, in ZoneInput) (*Zone, error) {
	if err := zoneProjects(ctx, st, p.OrgID, in.ProjectIDs); err != nil {
		return nil, err
	}
	id, err := randomID("zone")
	if err != nil {
		return nil, err
	}
	if in.ConnectionID != "" {
		return nil, ErrInvalid
	}
	zone := &Zone{ID: id, OrgID: p.OrgID, Name: name, Mode: "external", ProjectIDs: append([]string{}, in.ProjectIDs...), Revision: 1}
	if proofErr := s.renewZoneProof(zone); proofErr != nil {
		return nil, proofErr
	}
	return zone, nil
}

func (s *Service) VerifyZone(ctx context.Context, p security.Principal, id string) (*Zone, error) {
	if err := s.domainScope(ctx, p, "", true); err != nil {
		return nil, err
	}
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	zone, err := st.Zone(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	// A managed zone's proof is published first if it still needs to be.
	if _, checkErr := s.checkZoneProof(ctx, st, zone); checkErr != nil {
		return nil, checkErr
	}
	s.domainAudit(ctx, p, "domain.zone.write", zone.ID, "")
	return zone, nil
}

func (s *Service) verifyZoneControl(ctx context.Context, zone *Zone) error {
	if zone.Mode == "managed" {
		adapter, credential, err := s.zoneCredential(ctx, zone)
		if err != nil {
			return err
		}
		current, err := adapter.GetZone(ctx, credential, zone.ProviderZone.Name)
		if err != nil {
			return err
		}
		if current.ID != zone.ProviderZone.ID || current.Name != zone.ProviderZone.Name || current.AccountID != zone.AccountID {
			return provider.ErrInfrastructureIdentity
		}
		if _, err = zoneRelative(zone.Name, current.Name); err != nil {
			return err
		}
	}
	if !zone.hasControlProof() {
		return ErrConflict
	}
	if zone.VerifiedAt.IsZero() && !s.now().Before(zone.ProofExpiresAt) {
		return ErrConflict
	}
	values, err := s.domainResolver().LookupTXT(ctx, zone.ProofName)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound && !dnsErr.IsTimeout && !dnsErr.IsTemporary {
			// Missing public TXT is an unproven claim, including during propagation.
			return ErrConflict
		}
		return err
	}
	if !slices.Contains(values, zone.ProofValue) {
		return ErrConflict
	}
	return nil
}

// hasControlProof rejects legacy hosting-only verification and malformed scope.
func (z *Zone) hasControlProof() bool {
	return z.ProofName == "_loomplane."+z.Name && z.ProofValue != "" && !z.ProofExpiresAt.IsZero()
}

// verified is a zone a project can route through: checked, and checked
// against its own TXT proof.
func (z *Zone) verified() bool {
	return !z.VerifiedAt.IsZero() && z.hasControlProof()
}

func (s *Service) renewZoneProof(zone *Zone) error {
	token, err := reviewToken()
	if err != nil {
		return err
	}
	zone.ProofName = "_loomplane." + zone.Name
	zone.ProofValue = "loomplane=" + zone.ID + "-" + token
	zone.ProofExpiresAt = s.now().UTC().Add(24 * time.Hour)
	zone.VerifiedAt = time.Time{}
	return nil
}

func (s *Service) ListZones(ctx context.Context, p security.Principal) ([]*Zone, error) {
	if err := s.domainScope(ctx, p, "", false); err != nil {
		return nil, err
	}
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	return st.Zones(ctx, p.OrgID)
}

type ZoneUpdate struct {
	Revision   int64    `json:"revision"`
	ProjectIDs []string `json:"project_ids"`
	RenewProof bool     `json:"renew_proof"`
}

func (s *Service) UpdateZone(ctx context.Context, p security.Principal, id string, in ZoneUpdate) (*Zone, error) {
	if err := s.domainScope(ctx, p, "", true); err != nil {
		return nil, err
	}
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	zone, err := st.Zone(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if zone.Revision != in.Revision {
		return nil, ErrConflict
	}
	for _, project := range in.ProjectIDs {
		if project == "" {
			return nil, ErrInvalid
		}
		if checkErr := st.DomainProject(ctx, p.OrgID, project); checkErr != nil {
			return nil, checkErr
		}
	}
	zone.ProjectIDs = append([]string{}, in.ProjectIDs...)
	if in.RenewProof {
		if !zone.VerifiedAt.IsZero() && zone.hasControlProof() {
			return nil, ErrConflict
		}
		if proofErr := s.renewZoneProof(zone); proofErr != nil {
			return nil, proofErr
		}
	}
	if checkErr := st.SaveZone(ctx, zone); checkErr != nil {
		return nil, checkErr
	}
	s.domainAudit(ctx, p, "domain.zone.update", zone.ID, "")
	if in.RenewProof {
		// The published record takes the new value in place, by its receipt.
		if publishErr := s.publishZoneProof(ctx, st, zone); publishErr != nil {
			s.warn("Publish renewed zone proof", publishErr)
		}
	}
	return zone, nil
}

func (s *Service) RemoveZone(ctx context.Context, p security.Principal, id string, revision int64) error {
	if err := s.domainScope(ctx, p, "", true); err != nil {
		return err
	}
	st, err := s.domainStore()
	if err != nil {
		return err
	}
	zone, err := st.Zone(ctx, p.OrgID, id)
	if err != nil {
		return err
	}
	if zone.Mode == "managed" && (zone.ProofRecord != nil || zone.ProofPublishAttempted) {
		// The proof record goes first, and only from the revision the
		// caller saw. The zone goes at that same revision, so a check in
		// between fails the removal rather than publishing the record again.
		if zone.Revision != revision {
			return ErrConflict
		}
		if proofErr := s.removeZoneProof(ctx, st, zone); proofErr != nil {
			return proofErr
		}
	}
	err = st.RemoveZone(ctx, p.OrgID, id, revision)
	if err == nil {
		s.domainAudit(ctx, p, "domain.zone.remove", id, "")
	}
	return err
}

type ProjectZone struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Mode     string `json:"mode"`
	Verified bool   `json:"verified"`
}

func (s *Service) ProjectZones(ctx context.Context, p security.Principal, project string) ([]ProjectZone, error) {
	if project == "" {
		return nil, ErrInvalid
	}
	if err := s.domainScope(ctx, p, project, false); err != nil {
		return nil, err
	}
	st, err := s.domainStore()
	if err != nil {
		return nil, err
	}
	zones, err := st.Zones(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	out := []ProjectZone{}
	for _, zone := range zones {
		if slices.Contains(zone.ProjectIDs, project) {
			out = append(out, ProjectZone{zone.ID, zone.Name, zone.Mode, zone.verified()})
		}
	}
	return out, nil
}

func (s *Service) domainAudit(ctx context.Context, p security.Principal, action, id, project string) {
	if s.Audit != nil {
		s.Audit.Record(ctx, projects.AuditEvent{Action: action, Severity: "info", Principal: p, Attributes: map[string]any{"resource_id": id, "project_id": project}})
	}
}
