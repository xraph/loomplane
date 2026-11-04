package platformcloud

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
)

// hostnameRandom is how many base32 characters follow the service in a
// generated hostname: 60 bits, too many to guess or squat.
const hostnameRandom = 12

// hostnameEncoding is lowercase base32 without padding.
var hostnameEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// GeneratedHostname names a service under the platform apex:
// <service>-<12 lowercase base32 characters>.<apex>. The service part is the
// service's name as a DNS label, cut short so the whole first label is at
// most 63 characters.
func GeneratedHostname(service, apex string, rnd io.Reader) (string, error) {
	apex = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(apex)), ".")
	if apex == "" || strings.HasPrefix(apex, ".") || strings.Contains(apex, "..") || len(apex) > 253-64 {
		return "", errors.New("platformcloud: the platform apex is missing or invalid")
	}
	if rnd == nil {
		rnd = rand.Reader
	}
	raw := make([]byte, 8)
	if _, err := io.ReadFull(rnd, raw); err != nil {
		return "", fmt.Errorf("platformcloud: generate a hostname: %w", err)
	}
	label := hostnameLabel(service, 63-hostnameRandom-1)
	return label + "-" + hostnameEncoding.EncodeToString(raw)[:hostnameRandom] + "." + apex, nil
}

// hostnameLabel turns name into a DNS label of at most limit characters:
// lowercase letters, digits and single hyphens, never starting or ending
// with one. A name with nothing usable in it is "app".
func hostnameLabel(name string, limit int) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			if hyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			hyphen = false
			b.WriteRune(r)
		default:
			hyphen = true
		}
	}
	out := b.String()
	if len(out) > limit {
		out = strings.TrimRight(out[:limit], "-")
	}
	if out == "" {
		return "app"
	}
	return out
}

// DNSZone is the zone in the platform owner's DNS provider that holds the
// apex, and the credential that writes it.
type DNSZone struct {
	Provider   string
	Zone       string
	Credential provider.InfrastructureCredential
}

// DNSWriter writes the generated hostnames' records in the platform apex
// zone. Upsert creates a record when expected is nil and otherwise replaces
// the one expected names, returning the provider's receipt. Delete removes
// exactly the record expected names. cloudinfra builds one over its DNS
// adapters.
type DNSWriter interface {
	Upsert(ctx context.Context, zone DNSZone, record provider.DNSRecord, expected *provider.DNSRecord) (provider.DNSRecord, error)
	Delete(ctx context.Context, zone DNSZone, expected provider.DNSRecord) error
}

// WithDNS sets what writes the generated hostnames' records and returns the
// service.
func (s *Service) WithDNS(w DNSWriter) *Service {
	s.dns = w
	return s
}

const dnsSecret = "platform-cloud/dns"

type dnsCredential struct {
	AccountID string `json:"account_id"`
	TeamID    string `json:"team_id,omitempty"`
	Token     string `json:"token"`
}

// SetDNSSecret stores the credential that writes the platform apex zone in
// the DNS provider the instance settings name. It replaces the previous one.
func (s *Service) SetDNSSecret(ctx context.Context, _ /* actor */, accountID, teamID, token string) error {
	if accountID == "" || token == "" {
		return errors.New("platformcloud: a DNS account and token are required")
	}
	vault, err := s.secretStore()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(dnsCredential{AccountID: accountID, TeamID: teamID, Token: token})
	if err != nil {
		return err
	}
	return vault.Put(ctx, dnsSecret, raw)
}

// dnsZone is the platform apex zone and its credential.
func (s *Service) dnsZone(ctx context.Context) (DNSZone, error) {
	if s.dns == nil {
		return DNSZone{}, errors.New("platformcloud: no DNS writer is configured")
	}
	cfg, err := s.settings.PlatformCloud(ctx)
	if err != nil {
		return DNSZone{}, err
	}
	switch cfg.DNS.Provider {
	case "cloudflare", "digitalocean", "vercel":
	default:
		return DNSZone{}, fmt.Errorf("platformcloud: the platform DNS provider %q isn't supported", cfg.DNS.Provider)
	}
	if cfg.DNS.Zone == "" {
		return DNSZone{}, errors.New("platformcloud: the platform DNS zone is not configured")
	}
	vault, err := s.secretStore()
	if err != nil {
		return DNSZone{}, err
	}
	raw, err := vault.Reveal(ctx, dnsSecret)
	if err != nil {
		return DNSZone{}, fmt.Errorf("platformcloud: read DNS credential: %w", err)
	}
	var cred dnsCredential
	if err = json.Unmarshal(raw, &cred); err != nil || cred.Token == "" {
		return DNSZone{}, errors.New("platformcloud: the stored DNS credential is unreadable")
	}
	return DNSZone{Provider: cfg.DNS.Provider, Zone: strings.ToLower(cfg.DNS.Zone),
		Credential: provider.InfrastructureCredential{Provider: cfg.DNS.Provider, AccountID: cred.AccountID, TeamID: cred.TeamID, Token: cred.Token}}, nil
}

// hostname is a service's generated name and what was last published for it.
type hostname struct {
	OrgID, EnvironmentID, Resource, ProjectID, Name string
	HostID                                          *string
	Address                                         string
	Receipt                                         *provider.DNSRecord
}

func (s *Service) hostnameOf(ctx context.Context, orgID, envID, resource string) (*hostname, error) {
	h := &hostname{}
	var receipt []byte
	err := s.store.db.QueryRow(ctx, `SELECT org_id,environment_id,resource,project_id,hostname,host_id,address,receipt FROM loomplane_platform_hostnames
WHERE org_id=$1 AND environment_id=$2 AND resource=$3`, orgID, envID, resource).
		Scan(&h.OrgID, &h.EnvironmentID, &h.Resource, &h.ProjectID, &h.Name, &h.HostID, &h.Address, &receipt)
	if err = storeError(err); err != nil {
		return nil, err
	}
	if len(receipt) > 0 && string(receipt) != "null" {
		h.Receipt = &provider.DNSRecord{}
		if err = json.Unmarshal(receipt, h.Receipt); err != nil {
			return nil, err
		}
	}
	return h, nil
}

// ensureHostname is the service's generated name, made the first time it is
// asked for. A name another service already has is drawn again.
func (s *Service) ensureHostname(ctx context.Context, p *Placement, apex string) (*hostname, error) {
	h, err := s.hostnameOf(ctx, p.OrgID, p.EnvironmentID, p.Resource)
	if !errors.Is(err, ErrNotFound) {
		return h, err
	}
	for range 3 {
		name, genErr := GeneratedHostname(p.Resource, apex, nil)
		if genErr != nil {
			return nil, genErr
		}
		_, err = s.store.db.Exec(ctx, `INSERT INTO loomplane_platform_hostnames(org_id,environment_id,resource,project_id,hostname,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,now(),now()) ON CONFLICT (org_id,environment_id,resource) DO NOTHING`, p.OrgID, p.EnvironmentID, p.Resource, p.ProjectID, name)
		if err = storeError(err); errors.Is(err, ErrConflict) {
			continue // The name is taken; draw another.
		}
		if err != nil {
			return nil, err
		}
		return s.hostnameOf(ctx, p.OrgID, p.EnvironmentID, p.Resource)
	}
	return nil, err
}

// errHostnameUnavailable stands in for anything that went wrong publishing
// a generated hostname. Its text never names the host.
var errHostnameUnavailable = &workspaceError{msg: "Loomplane Cloud couldn't publish this service's hostname right now."}

// Apex is the domain the platform names services under, or "" when it
// names none.
func (s *Service) Apex(ctx context.Context) (string, error) {
	cfg, err := s.settings.PlatformCloud(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "platform cloud settings failed", "error", err)
		return "", errHostnameUnavailable
	}
	return cfg.Apex, nil
}

// Publish gives the organization's running service placement its generated
// hostname, made the first time, and points the name's record at the public
// ingress address of the host the placement is on: the first time it runs,
// and again after it moves. It returns the hostname, or "" when the
// platform has no apex to name services under.
func (s *Service) Publish(ctx context.Context, orgID, placementID string) (string, error) {
	if err := checkTenant(ctx, orgID); err != nil {
		return "", err
	}
	p, err := scanPlacement(s.store.db.QueryRow(ctx, `SELECT `+placementColumns+` FROM loomplane_platform_placements WHERE id=$1 AND org_id=$2`, placementID, orgID))
	if errors.Is(err, ErrNotFound) {
		return "", errNoWorkload
	}
	if err != nil {
		return "", sessionError(ctx, "publish", orgID, placementID, err)
	}
	if p.Kind != kindService || p.State == stateReleased || p.HostID == nil {
		return "", errNoWorkload
	}
	cfg, err := s.settings.PlatformCloud(ctx)
	if err != nil {
		return "", sessionError(ctx, "publish", orgID, placementID, err)
	}
	if cfg.Apex == "" {
		return "", nil
	}
	h, err := s.ensureHostname(ctx, p, cfg.Apex)
	if err != nil {
		return "", s.hostnameError(ctx, orgID, placementID, err)
	}
	if err = s.publishHostname(ctx, *p, h.Name); err != nil {
		return "", s.hostnameError(ctx, orgID, placementID, err)
	}
	return h.Name, nil
}

func (s *Service) hostnameError(ctx context.Context, orgID, placementID string, err error) error {
	slog.ErrorContext(ctx, "platform cloud hostname failed", "org", orgID, "placement", placementID, "error", err)
	return errHostnameUnavailable
}

// publishHostname points hostname's A (or, for IPv6, AAAA) record at the public ingress address
// of the host p is on, unless it already points there. The provider's
// receipt is kept, so the next write replaces exactly that record.
func (s *Service) publishHostname(ctx context.Context, p Placement, name string) error {
	h, err := s.hostnameOf(ctx, p.OrgID, p.EnvironmentID, p.Resource)
	if err != nil {
		return err
	}
	if h.Name != name {
		return fmt.Errorf("%w: the service has another hostname", ErrConflict)
	}
	host, err := s.store.GetHost(ctx, *p.HostID)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host.Evidence.IngressAddress)
	if ip == nil {
		return errors.New("platformcloud: the host's ingress address isn't an IP address")
	}
	address, kind := ip.String(), "AAAA"
	if ip.To4() != nil {
		address, kind = ip.To4().String(), "A"
	}
	if h.Receipt != nil && h.HostID != nil && *h.HostID == host.ID && h.Address == address {
		return nil
	}
	zone, err := s.dnsZone(ctx)
	if err != nil {
		return err
	}
	relative, err := zoneRelative(name, zone.Zone)
	if err != nil {
		return err
	}
	// The owner names the record by its first label, unique under the
	// apex, so it stays inside providers' comment limits.
	label, _, _ := strings.Cut(name, ".")
	record := provider.DNSRecord{Name: relative, Type: kind, Value: address, TTL: 60, Owner: "loomplane:hostname:" + label}
	expected := h.Receipt
	if expected != nil && expected.Type != kind {
		// Providers replace a record only with one of the same type, so a
		// move between an IPv4 and an IPv6 host deletes the old one first.
		if err = s.dns.Delete(ctx, zone, *expected); err != nil {
			return err
		}
		if _, err = s.store.db.Exec(ctx, `UPDATE loomplane_platform_hostnames SET host_id=NULL,address='',receipt=NULL,updated_at=now()
WHERE org_id=$1 AND environment_id=$2 AND resource=$3`, p.OrgID, p.EnvironmentID, p.Resource); err != nil {
			return storeError(err)
		}
		expected = nil
	}
	receipt, err := s.dns.Upsert(ctx, zone, record, expected)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	_, err = s.store.db.Exec(ctx, `UPDATE loomplane_platform_hostnames SET host_id=$4,address=$5,receipt=$6,updated_at=now()
WHERE org_id=$1 AND environment_id=$2 AND resource=$3`, p.OrgID, p.EnvironmentID, p.Resource, host.ID, address, raw)
	return storeError(err)
}

// zoneRelative is name relative to zone, "" for the zone's apex.
func zoneRelative(name, zone string) (string, error) {
	switch {
	case name == zone:
		return "", nil
	case strings.HasSuffix(name, "."+zone):
		return strings.TrimSuffix(name, "."+zone), nil
	}
	return "", fmt.Errorf("platformcloud: %s is outside the platform DNS zone", name)
}

// Unpublish deletes the service's generated hostname and its record, if it
// has one, and returns the name it had. A service that goes private, or
// away, stops answering on it.
func (s *Service) Unpublish(ctx context.Context, orgID, envID, resource string) (string, error) {
	if err := checkTenant(ctx, orgID); err != nil {
		return "", err
	}
	name, err := s.unpublish(ctx, orgID, envID, resource)
	if err != nil {
		return "", s.hostnameError(ctx, orgID, "", err)
	}
	return name, nil
}

func (s *Service) unpublish(ctx context.Context, orgID, envID, resource string) (string, error) {
	h, err := s.hostnameOf(ctx, orgID, envID, resource)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if h.Receipt != nil {
		zone, zoneErr := s.dnsZone(ctx)
		if zoneErr != nil {
			return "", zoneErr
		}
		if err = s.dns.Delete(ctx, zone, *h.Receipt); err != nil {
			return "", err
		}
	}
	_, err = s.store.db.Exec(ctx, `DELETE FROM loomplane_platform_hostnames WHERE org_id=$1 AND environment_id=$2 AND resource=$3`, orgID, envID, resource)
	return h.Name, storeError(err)
}

// serviceGone unpublishes the hostname of the service p ran, once p is
// released and nothing else runs the service: it was removed, not moved. A
// failure is logged; the name and its record stay for the next removal or
// the platform cleanup.
func (s *Service) serviceGone(ctx context.Context, p *Placement) {
	if p == nil || p.Kind != kindService {
		return
	}
	live, err := livePlacements(ctx, s.store.db, Request{OrgID: p.OrgID, EnvironmentID: p.EnvironmentID, Resource: p.Resource})
	if err == nil && live.counted() != nil {
		return // Moved: the live placement keeps the name.
	}
	if err == nil {
		_, err = s.unpublish(ctx, p.OrgID, p.EnvironmentID, p.Resource)
	}
	if err != nil {
		slog.ErrorContext(ctx, "platform cloud hostname removal failed", "org", p.OrgID, "placement", p.ID, "error", err)
	}
}
