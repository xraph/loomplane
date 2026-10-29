package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

// fakePlatformDomains stands in for a provider's app platform. It logs each
// call and answers Records with records.
type fakePlatformDomains struct {
	records []PlatformRecord
	calls   []string
	cred    provider.InfrastructureCredential
}

func (f *fakePlatformDomains) Records(_ context.Context, cred provider.InfrastructureCredential, b RouteBinding, hostname string, apex bool) ([]PlatformRecord, error) {
	f.calls = append(f.calls, fmt.Sprintf("records %s %s %t", b.ExternalID, hostname, apex))
	f.cred = cred
	return f.records, nil
}
func (f *fakePlatformDomains) Attach(_ context.Context, _ provider.InfrastructureCredential, b RouteBinding, hostname, reviewedCommit string) error {
	f.calls = append(f.calls, "attach "+b.ExternalID+" "+hostname+" "+reviewedCommit)
	return nil
}
func (f *fakePlatformDomains) Status(_ context.Context, _ provider.InfrastructureCredential, b RouteBinding, hostname string) (PlatformDomainStatus, error) {
	f.calls = append(f.calls, "status "+b.ExternalID+" "+hostname)
	return PlatformDomainStatus{}, nil
}
func (f *fakePlatformDomains) Detach(_ context.Context, _ provider.InfrastructureCredential, b RouteBinding, hostname, reviewedCommit string) error {
	f.calls = append(f.calls, "detach "+b.ExternalID+" "+hostname+" "+reviewedCommit)
	return nil
}

func platformBinding() RouteBinding {
	return RouteBinding{Backend: RouteBackendPlatform, Provider: "digitalocean", BindingID: "binding_web", ExternalID: "app-1", ConnectionID: "conn_do", ConnectionGeneration: 1}
}

// platformRouteFixture previews routes to a public service on App Platform
// under example.com, a managed DigitalOcean zone.
func platformRouteFixture(t *testing.T) (*Service, *kubeRouteStore, *fakePlatformDomains, *routeDNSAdapter) {
	t.Helper()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	dns := &routeDNSAdapter{ownership: true}
	platform := &fakePlatformDomains{records: []PlatformRecord{{Type: "CNAME", Value: "shop-abc.ondigitalocean.app"}}}
	registry := NewAdapters()
	if err := registry.RegisterDNS("digitalocean", dns); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterPlatform("digitalocean", platform); err != nil {
		t.Fatal(err)
	}
	zone := &Zone{ID: "zone", OrgID: "org", Name: "example.com", Mode: "managed", ConnectionID: "dns", ConnectionGeneration: 1, Provider: "digitalocean", AccountID: "account",
		RecordOwnership: true, ProviderZone: provider.DNSZone{ID: "zone", Name: "example.com"}, ProjectIDs: []string{"shop"}, VerifiedAt: now,
		ProofName: "_loomplane.example.com", ProofValue: "proof", ProofExpiresAt: now.Add(24 * time.Hour)}
	st := &kubeRouteStore{zone: zone, binding: platformBinding()}
	svc := NewService(Deps{Store: st, Domains: st, Authz: cleanupTestAuth{}, Adapters: registry, Connections: routeDNSCredentials{"digitalocean"}, Resolver: addressResolver{}})
	svc.now = func() time.Time { return now }
	return svc, st, platform, dns
}

func platformInput(hostname string) RouteInput {
	return RouteInput{ZoneID: "zone", EnvironmentID: "prod", Service: "web", Hostname: hostname, Port: 8080}
}

func TestPlatformPreviewNeedsNoCloud(t *testing.T) {
	svc, st, platform, dns := platformRouteFixture(t)
	op, err := svc.PreviewRoute(t.Context(), kubeRouteManager, "shop", platformInput("www.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if op.Route.Binding.Backend != RouteBackendPlatform || op.Route.Input.CloudID != "" || op.DesiredRecord != (provider.DNSRecord{}) {
		t.Fatalf("preview: %+v", op)
	}
	want := []provider.DNSRecord{{ZoneID: "zone", Name: "www", Type: "CNAME", Value: "shop-abc.ondigitalocean.app", TTL: 60, Owner: "loomplane:route:" + op.Route.ID}}
	if !slices.Equal(op.DesiredRecords, want) {
		t.Fatalf("records: %+v", op.DesiredRecords)
	}
	if !slices.Equal(platform.calls, []string{"records app-1 www.example.com false"}) || platform.cred.Provider != "digitalocean" || platform.cred.AccountID != "account" {
		t.Fatalf("platform: %q %+v", platform.calls, platform.cred)
	}
	if st.op.ID != op.ID || dns.writes != 0 {
		t.Fatalf("stored %q, writes %d", st.op.ID, dns.writes)
	}
}

func TestPlatformPreviewApexGetsAddressRecords(t *testing.T) {
	svc, _, platform, _ := platformRouteFixture(t)
	platform.records = []PlatformRecord{{Type: "A", Value: "162.159.140.98"}, {Type: "A", Value: "172.66.0.96"}, {Type: "AAAA", Value: "2606:4700:7::60"}, {Type: "AAAA", Value: "2a06:98c1:58::60"}}
	op, err := svc.PreviewRoute(t.Context(), kubeRouteManager, "shop", platformInput("example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(platform.calls, []string{"records app-1 example.com true"}) {
		t.Fatal(platform.calls)
	}
	if len(op.DesiredRecords) != 4 {
		t.Fatalf("records: %+v", op.DesiredRecords)
	}
	owner := "loomplane:route:" + op.Route.ID
	for i, record := range op.DesiredRecords {
		want := provider.DNSRecord{ZoneID: "zone", Name: "", Type: platform.records[i].Type, Value: platform.records[i].Value, TTL: 60, Owner: owner}
		if i > 0 {
			want.Owner = fmt.Sprintf("%s:%d", owner, i)
		}
		if record != want {
			t.Fatalf("record %d: %+v, want %+v", i, record, want)
		}
	}
	// A CNAME can't sit at the apex.
	platform.records = []PlatformRecord{{Type: "CNAME", Value: "shop-abc.ondigitalocean.app"}}
	if _, err = svc.PreviewRoute(t.Context(), kubeRouteManager, "shop", platformInput("example.com")); !errors.Is(err, ErrInvalid) {
		t.Fatal("CNAME at the apex", err)
	}
}

func TestPlatformPreviewRefusesTakenApex(t *testing.T) {
	svc, st, platform, dns := platformRouteFixture(t)
	platform.records = []PlatformRecord{{Type: "A", Value: "162.159.140.98"}, {Type: "AAAA", Value: "2606:4700:7::60"}}
	dns.records = []provider.DNSRecord{{ID: "existing", ZoneID: "zone", Name: "", Type: "A", Value: "192.0.2.1", TTL: 300}}
	if _, err := svc.PreviewRoute(t.Context(), kubeRouteManager, "shop", platformInput("example.com")); !errors.Is(err, provider.ErrInfrastructureDrift) {
		t.Fatal(err)
	}
	if st.op.ID != "" || dns.writes != 0 {
		t.Fatalf("wrote %+v, writes %d", st.op, dns.writes)
	}
}

// refusingStore resolves no binding, as ResolveRouteBinding does for a
// platform service the route can't reach.
type refusingStore struct {
	*kubeRouteStore
	err error
}

func (s refusingStore) ResolveRouteBinding(context.Context, string, string, RouteInput) (RouteBinding, error) {
	return RouteBinding{}, s.err
}

func TestPlatformPreviewRefusesPrivateServicesAndOtherPorts(t *testing.T) {
	for _, c := range []struct {
		name, public, port, want string
	}{
		{"private", "false", "8080", "This service is private on App Platform, so no hostname can reach it. Make it public in the manifest and deploy it first."},
		{"no public flag", "", "8080", "This service is private on App Platform, so no hostname can reach it. Make it public in the manifest and deploy it first."},
		{"other port", "true", "3000", "App Platform routes a hostname only to this service's public port, 3000."},
	} {
		err := checkPlatformService(c.public, c.port, 8080)
		if !errors.Is(err, ErrInvalid) || err.Error() != ErrInvalid.Error()+": "+c.want {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	if err := checkPlatformService("true", "8080", 8080); err != nil {
		t.Fatal("public on its port", err)
	}

	// Preview stops at the refusal, before the platform or DNS is asked.
	svc, st, platform, dns := platformRouteFixture(t)
	refusal := checkPlatformService("false", "8080", 8080)
	refusing := refusingStore{st, refusal}
	svc.Store, svc.Domains = refusing, refusing
	if _, err := svc.PreviewRoute(t.Context(), kubeRouteManager, "shop", platformInput("www.example.com")); !errors.Is(err, refusal) {
		t.Fatal(err)
	}
	if len(platform.calls) != 0 || st.op.ID != "" || dns.writes != 0 {
		t.Fatalf("went on: %q %+v", platform.calls, st.op)
	}
}

func TestPlatformPreviewRefusesWildcardsAndBlockedPaths(t *testing.T) {
	svc, st, platform, _ := platformRouteFixture(t)
	in := platformInput("www.example.com")
	in.BlockedPathPrefixes = []string{"/admin"}
	_, err := svc.PreviewRoute(t.Context(), kubeRouteManager, "shop", in)
	if !errors.Is(err, ErrInvalid) || err.Error() != ErrInvalid.Error()+": Blocked paths aren't supported on App Platform yet." {
		t.Fatal("blocked paths", err)
	}

	// The zone takes wildcards, through a Cloudflare challenge token.
	zone := st.zone
	zone.Provider, zone.ChallengeConnectionID = "cloudflare", "cf-dns"
	cf := func(id string) *connections.Connection {
		return &connections.Connection{ID: id, OrgID: "org", Cloud: connections.Cloudflare, Generation: 1, Status: "ready", Details: map[string]string{"account_id": "account"}}
	}
	registry := NewAdapters()
	dns := &routeDNSAdapter{ownership: true}
	if err = registry.RegisterDNS("cloudflare", challengeRouteDNS{orderedDNS{dns, &[]string{}}, map[string]cloudflare.ChallengeScope{tokenFor("cf-dns", 1): zoneScoped("zone")}}); err != nil {
		t.Fatal(err)
	}
	if err = registry.RegisterPlatform("digitalocean", platform); err != nil {
		t.Fatal(err)
	}
	svc.Adapters, svc.Connections = registry, challengeConnections{"dns": cf("dns"), "cf-dns": cf("cf-dns")}
	_, err = svc.PreviewRoute(t.Context(), kubeRouteManager, "shop", platformInput("*.example.com"))
	if !errors.Is(err, ErrInvalid) || err.Error() != ErrInvalid.Error()+": Wildcard hostnames aren't supported on App Platform yet." {
		t.Fatal("wildcard", err)
	}
	if len(platform.calls) != 0 || st.op.ID != "" {
		t.Fatalf("went on: %q %+v", platform.calls, st.op)
	}
}

func TestRouteRecordListsReadLegacyFields(t *testing.T) {
	one := provider.DNSRecord{ID: "r1", Name: "www", Type: "A", Value: "192.0.2.10"}
	two := provider.DNSRecord{ID: "r2", Name: "www", Type: "AAAA", Value: "2001:db8::1"}
	legacy := &RouteOperation{DesiredRecord: one, Receipt: &one}
	if !slices.Equal(legacy.desiredRecords(), []provider.DNSRecord{one}) || !slices.Equal(legacy.receipts(), []provider.DNSRecord{one}) {
		t.Fatalf("legacy: %+v %+v", legacy.desiredRecords(), legacy.receipts())
	}
	lists := &RouteOperation{DesiredRecords: []provider.DNSRecord{one, two}, Receipts: []provider.DNSRecord{one, two}}
	if !slices.Equal(lists.desiredRecords(), []provider.DNSRecord{one, two}) || !slices.Equal(lists.receipts(), []provider.DNSRecord{one, two}) {
		t.Fatalf("lists: %+v %+v", lists.desiredRecords(), lists.receipts())
	}
	empty := &RouteOperation{}
	if empty.desiredRecords() != nil || empty.receipts() != nil {
		t.Fatalf("empty: %+v %+v", empty.desiredRecords(), empty.receipts())
	}
	if got := (Route{Receipt: &one}).receipts(); !slices.Equal(got, []provider.DNSRecord{one}) {
		t.Fatal("legacy route", got)
	}
	if got := (Route{Receipts: []provider.DNSRecord{one, two}}).receipts(); !slices.Equal(got, []provider.DNSRecord{one, two}) {
		t.Fatal("route lists", got)
	}
	if got := (Route{}).receipts(); got != nil {
		t.Fatal("empty route", got)
	}
	if routeRecordOwner("route_1", 0) != "loomplane:route:route_1" || routeRecordOwner("route_1", 2) != "loomplane:route:route_1:2" {
		t.Fatal(routeRecordOwner("route_1", 0), routeRecordOwner("route_1", 2))
	}

	// The zone's records list each receipt of a route.
	zone := &Zone{ID: "zone", Name: "example.com", ProviderZone: provider.DNSZone{Name: "example.com"}}
	route := &Route{ID: "route_p", Input: RouteInput{ZoneID: "zone", Hostname: "www.example.com"}, Receipts: []provider.DNSRecord{one, two}}
	records := managedRecords(map[string]*Zone{"zone": zone}, []*Route{route}, nil)
	if len(records) != 2 || records[0].Type != "A" || records[1].Type != "AAAA" || records[1].Name != "www.example.com" || records[1].RouteID != "route_p" {
		t.Fatalf("managed records: %+v", records)
	}
	// A platform operation that wrote its records reads its checks.
	now := time.Now()
	verified := &Zone{Name: "example.com", Mode: "managed", VerifiedAt: now, ProofName: "_loomplane.example.com", ProofValue: "proof", ProofExpiresAt: now.Add(time.Hour)}
	running := &RouteOperation{Kind: "apply", Status: "running", DesiredRecords: []provider.DNSRecord{one}, Receipts: []provider.DNSRecord{one}}
	checked := Route{Verification: RouteVerification{DNS: true}}
	if status := DeriveRouteStatus(checked, verified, running, now); status.DNS != DomainReady {
		t.Fatalf("written: %+v", status)
	}
	running.Receipts = nil
	if status := DeriveRouteStatus(checked, verified, running, now); status.DNS != DomainProvisioning || status.Route != DomainProvisioning {
		t.Fatalf("writing: %+v", status)
	}
}
