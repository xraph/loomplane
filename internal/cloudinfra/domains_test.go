package cloudinfra

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestNormalizeDomainClaims(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{" Example.COM. ", "example.com"}, {"APP.Example.com", "app.example.com"}, {"BÜCHER.example", "xn--bcher-kva.example"},
	} {
		got, err := NormalizeHostname(tc.input)
		if err != nil || got != tc.want {
			t.Fatalf("%q: %q %v", tc.input, got, err)
		}
	}
	for _, input := range []string{"*.example.com", "example.com..", "https://example.com", "example.com:443", "127.0.0.1", "example", "-bad.example", "bad_.example", "example.com/path", "evil\n.example"} {
		if _, err := NormalizeHostname(input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid exact hostname %q accepted: %v", input, err)
		}
	}
	for _, tc := range []struct{ host, zone, want string }{{"example.com", "example.com", ""}, {"a.example.com", "example.com", "a"}, {"a.b.example.com", "example.com", "a.b"}} {
		got, err := zoneRelative(tc.host, tc.zone)
		if err != nil || got != tc.want {
			t.Fatalf("zone containment: %q %v", got, err)
		}
	}
	if _, err := zoneRelative("otherexample.com", "example.com"); err == nil {
		t.Fatal("suffix collision accepted")
	}
}

type proofResolver struct{ values []string }

func (r proofResolver) LookupTXT(context.Context, string) ([]string, error)      { return r.values, nil }
func (proofResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) { return nil, nil }

func TestDomainProofExpirationAndContinuedControl(t *testing.T) {
	now := time.Now().UTC()
	svc := NewService(Deps{Resolver: proofResolver{[]string{"expected"}}})
	svc.now = func() time.Time { return now }
	zone := &Zone{Mode: "external", Name: "example.com", ProofName: "_loomplane.example.com", ProofValue: "expected", ProofExpiresAt: now.Add(-time.Second)}
	// Check again reads an unverified proof until it expires.
	if err := svc.verifyOwnZoneControl(t.Context(), zone); !errors.Is(err, ErrConflict) {
		t.Fatal("expired unverified proof accepted", err)
	}
	zone.ProofExpiresAt = now.Add(time.Minute)
	if err := svc.verifyOwnZoneControl(t.Context(), zone); err != nil {
		t.Fatal(err)
	}
	// A route never passes on a proof that is only pending.
	if err := svc.verifyZoneControl(t.Context(), zone); !errors.Is(err, ErrConflict) {
		t.Fatal("pending proof authorized a route", err)
	}
	zone.VerifiedAt, zone.ProofExpiresAt = now.Add(-time.Hour), now.Add(-time.Minute)
	if err := svc.verifyZoneControl(t.Context(), zone); err != nil {
		t.Fatal("established control should remain valid while TXT exists", err)
	}
	svc.Resolver = proofResolver{[]string{"another-organization-proof"}}
	if err := svc.verifyZoneControl(t.Context(), zone); !errors.Is(err, ErrConflict) {
		t.Fatal("foreign TXT established control", err)
	}
}

type scopedZoneStore struct {
	DomainStore
	zone *Zone
}

func (*scopedZoneStore) DomainProject(context.Context, string, string) error { return nil }
func (s *scopedZoneStore) Zones(context.Context, string) ([]*Zone, error) {
	if s.zone == nil {
		return []*Zone{}, nil
	}
	return []*Zone{s.zone}, nil
}
func (s *scopedZoneStore) CreateZone(_ context.Context, z *Zone) error { s.zone = z; return nil }
func (s *scopedZoneStore) SaveZone(_ context.Context, z *Zone) error   { s.zone = z; return nil }

type scopedZoneAdapter struct{ routeDNSAdapter }

func (*scopedZoneAdapter) ListZones(_ context.Context, c provider.InfrastructureCredential) ([]provider.DNSZone, error) {
	return []provider.DNSZone{{ID: "parent", Name: "example.com", AccountID: c.AccountID, Verified: true}, {ID: "child", Name: "child.example.com", AccountID: c.AccountID, Verified: true}}, nil
}
func (*scopedZoneAdapter) GetZone(_ context.Context, c provider.InfrastructureCredential, name string) (provider.DNSZone, error) {
	return provider.DNSZone{ID: "child", Name: name, AccountID: c.AccountID, Verified: true}, nil
}
func TestManagedProofNamespaceAndHostedZoneRemainDistinct(t *testing.T) {
	registry := NewAdapters()
	if err := registry.RegisterDNS("vercel", &scopedZoneAdapter{}); err != nil {
		t.Fatal(err)
	}
	store := &scopedZoneStore{}
	svc := NewService(Deps{Domains: store, Authz: cleanupTestAuth{}, Connections: routeDNSCredentials{"vercel"}, Adapters: registry})
	zone, err := svc.CreateZone(t.Context(), security.Principal{OrgID: "org", UserID: "manager"}, ZoneInput{Name: "app.child.example.com", Mode: "managed", ConnectionID: "dns", ProjectIDs: []string{"project"}})
	if err != nil {
		t.Fatal(err)
	}
	if zone.ProviderZone.ID != "child" || zone.ProviderZone.Name != "child.example.com" || zone.Name != "app.child.example.com" || zone.ProofName != "_loomplane.app.child.example.com" || !zone.VerifiedAt.IsZero() {
		t.Fatalf("scope widened: %+v", zone)
	}
	// Checked, the way Check again would stamp it.
	zone.VerifiedAt = time.Now()
	svc.Resolver = proofResolver{[]string{zone.ProofValue}}
	if err = svc.verifyZoneControl(t.Context(), zone); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"child.example.com", "sibling.child.example.com", "example.com"} {
		if _, err = routeDNSRecord(zone, host, "192.0.2.1", "route"); err == nil {
			t.Fatalf("child proof authorized %s", host)
		}
	}
	record, err := routeDNSRecord(zone, "sub.app.child.example.com", "192.0.2.1", "route")
	if err != nil || record.Name != "sub.app" || record.ZoneID != "child" {
		t.Fatalf("provider-relative name: %+v %v", record, err)
	}
	zone.Name = "child.example.com"
	if err = svc.verifyZoneControl(t.Context(), zone); err == nil {
		t.Fatal("child challenge verified parent namespace")
	}
	zone.Name = "app.child.example.com"
	zone.ConnectionGeneration++
	if err = svc.verifyZoneControl(t.Context(), zone); err == nil {
		t.Fatal("changed account generation admitted proof")
	}
	// Cleanup checks the exact hosted zone/account even after proof loss or rotation.
	zone.ProofValue = ""
	if _, _, err = svc.zoneCredentialFor(t.Context(), zone, true); err != nil {
		t.Fatal("retained hosted-zone cleanup denied", err)
	}
}
