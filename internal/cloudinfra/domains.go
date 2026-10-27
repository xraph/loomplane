package cloudinfra

import (
	"cmp"
	"context"
	"errors"
	"fmt"
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
	// parent is the nearest zone of the same organization whose name this
	// one's sits strictly under. LinkZoneParents sets it; it isn't stored.
	parent *Zone
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
	zones, err := st.Zones(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	// A subdomain is verified through its domain, so it gets no proof of
	// its own: nothing is published for it or checked in the background.
	ownProof := nearestAncestor(name, p.OrgID, zones) == nil
	var zone *Zone
	if in.Mode == "managed" {
		zone, err = s.createManagedZone(ctx, p, name, in.ConnectionID, in.ProjectIDs, ownProof)
	} else {
		zone, err = s.externalZone(ctx, st, p, name, in, ownProof)
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
// DNS provider hosts. With ownProof it waits on its own TXT proof like an
// external zone, and the caller stores it, then publishes the proof with
// publishZoneProof. Without, it is verified through its domain.
func (s *Service) createManagedZone(ctx context.Context, p security.Principal, name, connectionID string, projectIDs []string, ownProof bool) (*Zone, error) {
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
	if !ownProof {
		return zone, nil
	}
	if proofErr := s.renewZoneProof(zone); proofErr != nil {
		return nil, proofErr
	}
	return zone, nil
}

// externalZone builds a zone whose DNS its owner runs. With ownProof it
// waits on its own TXT proof; without, it is verified through its domain.
func (s *Service) externalZone(ctx context.Context, st DomainStore, p security.Principal, name string, in ZoneInput, ownProof bool) (*Zone, error) {
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
	if !ownProof {
		return zone, nil
	}
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

// verifyZoneControl rechecks a zone before a route uses it: the zone's own
// hosting, then a public TXT record of a zone already checked against it.
// That is the zone's own record if the zone is verified and the record is
// still there, else the record of its nearest verified domain. A proof that
// is only pending never counts, even when public DNS shows it.
func (s *Service) verifyZoneControl(ctx context.Context, zone *Zone) error {
	if err := s.verifyZoneHosting(ctx, zone); err != nil {
		return err
	}
	err := ErrConflict
	if zone.ownVerified() {
		// Any failure here, a transient DNS error included, deliberately
		// falls over to the verified domain's record below.
		if err = s.verifyZoneProof(ctx, zone); err == nil {
			return nil
		}
	}
	if domain := zone.verifiedAncestor(); domain != nil {
		return s.verifyZoneProof(ctx, domain)
	}
	return err
}

// verifyOwnZoneControl is verifyZoneControl against the zone's own proof
// only. Check again uses it, so a zone is never stamped verified from its
// domain's record: that would outlive the domain's verification.
func (s *Service) verifyOwnZoneControl(ctx context.Context, zone *Zone) error {
	if err := s.verifyZoneHosting(ctx, zone); err != nil {
		return err
	}
	return s.verifyZoneProof(ctx, zone)
}

// verifyZoneHosting checks a managed zone's hosting zone is still the one
// it was added in, in the same account, and still holds the zone's name.
func (s *Service) verifyZoneHosting(ctx context.Context, zone *Zone) error {
	if zone.Mode != "managed" {
		return nil
	}
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
	_, err = zoneRelative(zone.Name, current.Name)
	return err
}

// verifyZoneProof looks for the zone's own TXT proof in public DNS.
func (s *Service) verifyZoneProof(ctx context.Context, zone *Zone) error {
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
		if zone.ownVerified() {
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

// BlockingHostname is a hostname that keeps a zone from being removed.
type BlockingHostname struct {
	RouteID     string `json:"route_id"`
	Hostname    string `json:"hostname"`
	ZoneID      string `json:"zone_id"`
	ZoneName    string `json:"zone_name"`
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
}

// ZoneInUseError refuses removing a zone while hostnames use it or, when
// Subdomains, the zones under it.
type ZoneInUseError struct {
	Zone       string
	Subdomains bool
	Hostnames  []BlockingHostname
}

func (e *ZoneInUseError) Error() string {
	if e.Subdomains {
		return "remove the hostnames that use " + e.Zone + " and its subdomains first"
	}
	return "remove the hostnames that use " + e.Zone + " first"
}

func (e *ZoneInUseError) Unwrap() error { return ErrConflict }

// RemoveZone removes a zone and every zone under it, at the revision the
// caller saw. Nothing goes while any hostname uses one of them. The
// subdomains go first, deepest name first, each with its own proof record,
// and the first one that can't be cleaned stops the rest: zones already
// removed stay removed, and the others are kept whole.
func (s *Service) RemoveZone(ctx context.Context, p security.Principal, id string, revision int64) error {
	if err := s.domainScope(ctx, p, "", true); err != nil {
		return err
	}
	st, err := s.domainStore()
	if err != nil {
		return err
	}
	zones, err := st.Zones(ctx, p.OrgID)
	if err != nil {
		return err
	}
	LinkZoneParents(zones)
	i := slices.IndexFunc(zones, func(z *Zone) bool { return z.ID == id })
	if i < 0 {
		return ErrNotFound
	}
	zone := zones[i]
	// The proof records go before their zones, so nothing is touched
	// unless the caller saw this revision.
	if zone.Revision != revision {
		return ErrConflict
	}
	subdomains := subdomainsOf(zone, zones)
	if inUse := s.zoneInUse(ctx, st, zone, subdomains); inUse != nil {
		return inUse
	}
	// Longest name first: a zone is never left without the domain that
	// proves it while it remains.
	slices.SortStableFunc(subdomains, func(a, b *Zone) int { return cmp.Compare(len(b.Name), len(a.Name)) })
	for _, z := range append(subdomains, zone) {
		err = s.removeZoneProof(ctx, st, z)
		if err == nil {
			err = st.RemoveZone(ctx, p.OrgID, z.ID, z.Revision)
			// Only the zone you asked for can be not found: a subdomain
			// that changed or went since you looked is a conflict.
			if z != zone && errors.Is(err, ErrNotFound) {
				return fmt.Errorf("%w: %s changed since you looked, so %s was kept. Try again", ErrConflict, z.Name, zone.Name)
			}
		}
		if err != nil {
			return s.racedRemoval(ctx, st, zone, subdomains, err)
		}
		s.domainAudit(ctx, p, "domain.zone.remove", z.ID, "")
	}
	return nil
}

// racedRemoval is the refusal for a cascade that stopped at a conflict:
// the hostnames that use zone or its subdomains when a route landed after
// the first check, or else err itself.
func (s *Service) racedRemoval(ctx context.Context, st DomainStore, zone *Zone, subdomains []*Zone, err error) error {
	if !errors.Is(err, ErrConflict) || errors.Is(err, provider.ErrInfrastructureAmbiguous) {
		return err
	}
	var inUse *ZoneInUseError
	if errors.As(s.zoneInUse(ctx, st, zone, subdomains), &inUse) {
		return inUse
	}
	return err
}

// zoneInUse is the refusal for removing zone and its subdomains, or nil
// when no route uses any of them. A failed read refuses too.
func (s *Service) zoneInUse(ctx context.Context, st DomainStore, zone *Zone, subdomains []*Zone) error {
	family := map[string]*Zone{zone.ID: zone}
	for _, sub := range subdomains {
		family[sub.ID] = sub
	}
	routes, err := st.OrgRoutes(ctx, zone.OrgID)
	if err != nil {
		return err
	}
	inUse := &ZoneInUseError{Zone: zone.Name, Subdomains: len(subdomains) > 0}
	for _, route := range routes {
		if z, ok := family[route.Input.ZoneID]; ok && route.Status != "removed" {
			inUse.Hostnames = append(inUse.Hostnames, BlockingHostname{RouteID: route.ID, Hostname: route.Input.Hostname, ZoneID: z.ID, ZoneName: z.Name, ProjectID: route.ProjectID})
		}
	}
	if len(inUse.Hostnames) == 0 {
		return nil
	}
	names, err := st.ProjectNames(ctx, zone.OrgID)
	if err != nil {
		return err
	}
	for i := range inUse.Hostnames {
		inUse.Hostnames[i].ProjectName = names[inUse.Hostnames[i].ProjectID]
	}
	slices.SortFunc(inUse.Hostnames, func(a, b BlockingHostname) int {
		return cmp.Or(cmp.Compare(a.Hostname, b.Hostname), cmp.Compare(a.RouteID, b.RouteID))
	})
	return inUse
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
	// Linked before the filter: a subdomain's domain may not be granted.
	LinkZoneParents(zones)
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
