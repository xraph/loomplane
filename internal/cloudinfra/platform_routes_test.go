package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

// fakePlatformDomains stands in for a provider's app platform. It logs each
// call, answers Records with records and Status with status, and keeps the
// app's hostnames in spec.
type fakePlatformDomains struct {
	mu      sync.Mutex
	records []PlatformRecord
	calls   []string
	cred    provider.InfrastructureCredential
	status  PlatformDomainStatus
	// spec is read when an attach starts and written back hold later, as
	// App Platform's whole-spec update is. Attaches that overlap set
	// overlapped, and one of their hostnames would be lost.
	spec       []string
	hold       time.Duration
	inside     int
	overlapped bool
}

func (f *fakePlatformDomains) Records(_ context.Context, cred provider.InfrastructureCredential, b RouteBinding, hostname string, apex bool) ([]PlatformRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("records %s %s %t", b.ExternalID, hostname, apex))
	f.cred = cred
	return f.records, nil
}
func (f *fakePlatformDomains) Attach(_ context.Context, _ provider.InfrastructureCredential, b RouteBinding, hostname, reviewedCommit string) error {
	f.mu.Lock()
	f.calls = append(f.calls, "attach "+b.ExternalID+" "+hostname+" "+reviewedCommit)
	f.inside++
	f.overlapped = f.overlapped || f.inside > 1
	spec := slices.Clone(f.spec)
	f.mu.Unlock()
	time.Sleep(f.hold)
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(spec, hostname) {
		spec = append(spec, hostname)
	}
	f.spec, f.inside = spec, f.inside-1
	return nil
}
func (f *fakePlatformDomains) Status(_ context.Context, _ provider.InfrastructureCredential, b RouteBinding, hostname string) (PlatformDomainStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "status "+b.ExternalID+" "+hostname)
	return f.status, nil
}
func (f *fakePlatformDomains) Detach(_ context.Context, _ provider.InfrastructureCredential, b RouteBinding, hostname, reviewedCommit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "detach "+b.ExternalID+" "+hostname+" "+reviewedCommit)
	f.spec = slices.DeleteFunc(f.spec, func(h string) bool { return h == hostname })
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
		{"no port", "true", "", "This service has no public port on App Platform. Set port in the manifest and deploy it first."},
		{"private without a port", "false", "", "This service is private on App Platform, so no hostname can reach it. Make it public in the manifest and deploy it first."},
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

// platformCommit is the commit a platform binding was reviewed at.
const platformCommit = "abc123"

// cnameResolver answers CNAME lookups from cnames, beside addressResolver's
// addresses.
type cnameResolver struct {
	addressResolver
	cnames map[string]string
}

func (r cnameResolver) LookupCNAME(_ context.Context, host string) (string, error) {
	if target, ok := r.cnames[host]; ok {
		return target + ".", nil
	}
	return "", &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// platformRecordDNS logs record writes and deletes beside the platform's
// calls, with how many receipts the operation had saved before each write.
// Each record it creates gets an ID of its own.
type platformRecordDNS struct {
	*routeDNSAdapter
	platform *fakePlatformDomains
	st       *kubeRouteStore
	saved    *[]int
}

func (a platformRecordDNS) UpsertRecord(ctx context.Context, c provider.InfrastructureCredential, req provider.DNSUpsertRequest) (provider.DNSRecord, error) {
	a.platform.calls = append(a.platform.calls, "upsert "+req.Record.Type+" "+req.Record.Value)
	*a.saved = append(*a.saved, len(a.st.op.Receipts))
	record, err := a.routeDNSAdapter.UpsertRecord(ctx, c, req)
	if err == nil && record.ID == "created" {
		record.ID = fmt.Sprintf("rec_%d", a.writes)
		a.records[len(a.records)-1] = record
	}
	return record, err
}
func (a platformRecordDNS) DeleteRecord(ctx context.Context, c provider.InfrastructureCredential, req provider.DNSDeleteRequest) error {
	a.platform.calls = append(a.platform.calls, "delete "+req.Expected.ID)
	return a.routeDNSAdapter.DeleteRecord(ctx, c, req)
}

// platformApplyFixture holds an accepted apply operation for hostname, a
// route to the web service's app on App Platform. Its certificate is still
// issuing and HTTPS doesn't answer yet.
func platformApplyFixture(t *testing.T, hostname string, records []PlatformRecord) (*Service, *kubeRouteStore, *fakePlatformDomains, *routeDNSAdapter, *[]int) {
	t.Helper()
	svc, st, platform, dns := platformRouteFixture(t)
	saved := []int{}
	registry := NewAdapters()
	if err := registry.RegisterDNS("digitalocean", platformRecordDNS{dns, platform, st, &saved}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterPlatform("digitalocean", platform); err != nil {
		t.Fatal(err)
	}
	svc.Adapters = registry
	svc.Resolver = cnameResolver{}
	svc.routeHTTPS = func(context.Context, string) (time.Time, int, error) {
		return time.Time{}, 0, errors.New("certificate not issued yet")
	}
	st.commit = platformCommit
	platform.records = records
	platform.status = PlatformDomainStatus{Attached: true, CertState: "issuing"}
	if _, err := svc.PreviewRoute(t.Context(), kubeRouteManager, "shop", platformInput(hostname)); err != nil {
		t.Fatal(err)
	}
	st.op.Status, st.op.Route.OperationID = "pending", st.op.ID
	platform.calls = nil
	return svc, st, platform, dns, &saved
}

func apexRecords() []PlatformRecord {
	return []PlatformRecord{{Type: "A", Value: "162.159.140.98"}, {Type: "A", Value: "172.66.0.96"}, {Type: "AAAA", Value: "2606:4700:7::60"}, {Type: "AAAA", Value: "2a06:98c1:58::60"}}
}

func subdomainRecords() []PlatformRecord {
	return []PlatformRecord{{Type: "CNAME", Value: "shop-abc.ondigitalocean.app"}}
}

func TestPlatformRouteAppliesAttachThenEveryRecord(t *testing.T) {
	svc, st, platform, dns, saved := platformApplyFixture(t, "example.com", apexRecords())
	err := svc.ReconcileRoute(t.Context(), "org", st.op.ID, nil)
	if !errors.Is(err, ErrPending) {
		t.Fatal(err, st.op.Message)
	}
	want := []string{"attach app-1 example.com " + platformCommit,
		"upsert A 162.159.140.98", "upsert A 172.66.0.96", "upsert AAAA 2606:4700:7::60", "upsert AAAA 2a06:98c1:58::60",
		"status app-1 example.com"}
	if !slices.Equal(platform.calls, want) {
		t.Fatalf("calls:\n%q\nwant\n%q", platform.calls, want)
	}
	// Each receipt was saved before the next record was written.
	if !slices.Equal(*saved, []int{0, 1, 2, 3}) {
		t.Fatal("receipts saved before each write", *saved)
	}
	if len(st.op.Receipts) != 4 || st.op.Receipt != nil || st.op.DNSAttempted || dns.writes != 4 {
		t.Fatalf("operation: %+v", st.op)
	}
	for i, receipt := range st.op.Receipts {
		if receipt.ID != fmt.Sprintf("rec_%d", i+1) || receipt.Owner != routeRecordOwner(st.op.Route.ID, i) {
			t.Fatalf("receipt %d: %+v", i, receipt)
		}
	}
	if st.route == nil || len(st.route.Receipts) != 4 || st.route.Status != "waiting" || st.op.Status != "waiting" {
		t.Fatalf("route: %+v", st.route)
	}
	if !slices.Equal(st.locks, []string{"ingress/org/platform:conn_do/app-1"}) {
		t.Fatal("lock", st.locks)
	}

	// A retry writes nothing again.
	st.op.Status = "pending"
	platform.calls = nil
	if err = svc.ReconcileRoute(t.Context(), "org", st.op.ID, nil); !errors.Is(err, ErrPending) || dns.writes != 4 || len(st.op.Receipts) != 4 {
		t.Fatal("retry", err, dns.writes, st.op.Receipts)
	}
	if !slices.Equal(platform.calls, []string{"attach app-1 example.com " + platformCommit, "status app-1 example.com"}) {
		t.Fatal("retry calls", platform.calls)
	}
}

func TestPlatformRouteWaitsWhileTheCertificateIssues(t *testing.T) {
	svc, st, _, dns, _ := platformApplyFixture(t, "www.example.com", subdomainRecords())
	svc.Resolver = cnameResolver{cnames: map[string]string{"www.example.com": "shop-abc.ondigitalocean.app"}}
	err := svc.ReconcileRoute(t.Context(), "org", st.op.ID, nil)
	if !errors.Is(err, ErrPending) || st.op.Status != "waiting" || dns.writes != 1 {
		t.Fatal(err, st.op.Status, dns.writes)
	}
	v := st.route.Verification
	if st.route.Status != "waiting" || !v.DNS || v.TLS || v.CertState != "issuing" || v.Backend || v.TLSError == "" {
		t.Fatalf("route: %+v", st.route)
	}
	status := DeriveRouteStatus(*st.route, st.zone, &st.op, svc.now())
	if status.DNS != DomainReady || status.Certificate == DomainReady || status.Route == DomainReady {
		t.Fatalf("status: %+v", status)
	}
}

func TestPlatformRouteIsReadyWhenDNSCertificateAndBackendAgree(t *testing.T) {
	svc, st, platform, dns, _ := platformApplyFixture(t, "www.example.com", subdomainRecords())
	svc.Resolver = cnameResolver{cnames: map[string]string{"www.example.com": "shop-abc.ondigitalocean.app"}}
	expires := svc.now().AddDate(0, 3, 0)
	svc.routeHTTPS = func(_ context.Context, host string) (time.Time, int, error) {
		if host != "www.example.com" {
			t.Fatal("probed", host)
		}
		return expires, 200, nil
	}
	platform.status = PlatformDomainStatus{Attached: true, CertState: "ready", CertExpires: expires}
	if err := svc.ReconcileRoute(t.Context(), "org", st.op.ID, nil); err != nil {
		t.Fatal(err, st.op.Message)
	}
	v := st.route.Verification
	if st.op.Status != "succeeded" || st.route.Status != "ready" || !v.DNS || !v.TLS || !v.Backend || v.CertState != "ready" || !v.CertExpires.Equal(expires) {
		t.Fatalf("route: %+v", st.route)
	}
	if status := DeriveRouteStatus(*st.route, st.zone, &st.op, svc.now()); status.DNS != DomainReady || status.Certificate != DomainReady || status.Route != DomainReady {
		t.Fatalf("status: %+v", status)
	}

	// A check carries the route's receipts and asks the platform for its
	// records again, writing nothing.
	route := *st.route
	op, err := svc.VerifyRoute(t.Context(), kubeRouteManager, "shop", route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(op.Receipts, route.Receipts) || len(op.Receipts) != 1 {
		t.Fatalf("verify receipts: %+v", op.Receipts)
	}
	platform.calls = nil
	if err = svc.ReconcileRoute(t.Context(), "org", op.ID, nil); err != nil || st.op.Status != "succeeded" || dns.writes != 1 {
		t.Fatal("verify", err, st.op.Status, st.op.Message, dns.writes)
	}
	if !slices.Equal(platform.calls, []string{"records app-1 www.example.com false", "status app-1 www.example.com"}) {
		t.Fatal("verify calls", platform.calls)
	}

	// The backend fails on a gateway error or a hostname the app dropped.
	cname := []provider.DNSRecord{{Name: "www", Type: "CNAME", Value: "shop-abc.ondigitalocean.app"}}
	for name, tc := range map[string]struct {
		code   int
		status PlatformDomainStatus
	}{
		"gateway error": {503, PlatformDomainStatus{Attached: true, CertState: "ready"}},
		"detached":      {200, PlatformDomainStatus{CertState: "ready"}},
	} {
		svc.routeHTTPS = func(context.Context, string) (time.Time, int, error) { return expires, tc.code, nil }
		v = svc.verifyPlatformRoute(t.Context(), *st.route, cname, tc.status)
		if !v.DNS || !v.TLS || v.Backend || v.BackendError == "" {
			t.Fatalf("%s: %+v", name, v)
		}
	}
	svc.routeHTTPS = func(context.Context, string) (time.Time, int, error) {
		return time.Time{}, 0, errors.New("handshake failed")
	}
	v = svc.verifyPlatformRoute(t.Context(), *st.route, cname, PlatformDomainStatus{Attached: true, CertState: "failed", Message: "CNAME points elsewhere"})
	if v.TLS || v.CertState != "failed" || !strings.Contains(v.TLSError, "CNAME points elsewhere") {
		t.Fatalf("failed certificate: %+v", v)
	}

	// An apex passes once every address it asked for resolves.
	svc.routeHTTPS = func(context.Context, string) (time.Time, int, error) { return expires, 200, nil }
	apex := *st.route
	apex.Input.Hostname = "example.com"
	records := []provider.DNSRecord{{Type: "A", Value: "162.159.140.98"}, {Type: "A", Value: "172.66.0.96"}, {Type: "AAAA", Value: "2606:4700:7::60"}}
	svc.Resolver = addressResolver{[]string{"162.159.140.98", "172.66.0.96"}}
	if v = svc.verifyPlatformRoute(t.Context(), apex, records, PlatformDomainStatus{Attached: true, CertState: "ready"}); v.DNS || v.DNSError == "" {
		t.Fatalf("a missing address: %+v", v)
	}
	svc.Resolver = addressResolver{[]string{"2606:4700:7::60", "172.66.0.96", "162.159.140.98"}}
	if v = svc.verifyPlatformRoute(t.Context(), apex, records, PlatformDomainStatus{Attached: true, CertState: "ready"}); !v.DNS {
		t.Fatalf("every address: %+v", v)
	}
	if v = svc.verifyPlatformRoute(t.Context(), apex, nil, PlatformDomainStatus{Attached: true, CertState: "ready"}); v.DNS {
		t.Fatalf("no records: %+v", v)
	}
}

func TestPlatformRouteRemoveDetachesThenDeletesEveryRecord(t *testing.T) {
	svc, st, platform, dns, _ := platformApplyFixture(t, "example.com", apexRecords())
	route := st.op.Route
	route.Status, route.Revision = "ready", 3
	for i, r := range apexRecords() {
		receipt := provider.DNSRecord{ID: fmt.Sprintf("rec_%d", i+1), ZoneID: "zone", Name: "", Type: r.Type, Value: r.Value, TTL: 60, Owner: routeRecordOwner(route.ID, i)}
		route.Receipts = append(route.Receipts, receipt)
		// The second is already gone.
		if i != 1 {
			dns.records = append(dns.records, receipt)
		}
	}
	dns.records = append(dns.records, provider.DNSRecord{ID: "unrelated", ZoneID: "zone", Name: "mail", Type: "TXT", Value: "keep", TTL: 60})
	st.route = &route
	op, err := svc.RemoveRoute(t.Context(), kubeRouteManager, "shop", route.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(op.Receipts) != 4 {
		t.Fatalf("removal receipts: %+v", op.Receipts)
	}
	// An accepted removal keeps its authority after access is revoked.
	svc.Authz = overviewAuth{}
	if err = svc.ReconcileRoute(t.Context(), "org", op.ID, nil); err != nil {
		t.Fatal(err, st.op.Message)
	}
	want := []string{"detach app-1 example.com " + platformCommit, "delete rec_1", "delete rec_2", "delete rec_3", "delete rec_4"}
	if !slices.Equal(platform.calls, want) {
		t.Fatalf("calls %q", platform.calls)
	}
	if st.route.Status != "removed" || st.op.Status != "succeeded" || len(dns.records) != 1 || dns.records[0].ID != "unrelated" {
		t.Fatalf("after removal: %+v %+v", st.route, dns.records)
	}
	if !slices.Equal(st.locks, []string{"ingress/org/platform:conn_do/app-1", "ingress/org/platform:conn_do/app-1"}) {
		t.Fatal("locks", st.locks)
	}
}

// keyLocks is WithOperationLock's mutual exclusion, one lock per key.
type keyLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (k *keyLocks) lock(key string) *sync.Mutex {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.locks[key] == nil {
		k.locks[key] = &sync.Mutex{}
	}
	return k.locks[key]
}

// lockingRouteStore holds its key's lock while run runs, as the database
// store's advisory lock does.
type lockingRouteStore struct {
	*kubeRouteStore
	keys *keyLocks
}

func (s lockingRouteStore) WithOperationLock(ctx context.Context, key string, run func() error) error {
	m := s.keys.lock(key)
	m.Lock()
	defer m.Unlock()
	return s.kubeRouteStore.WithOperationLock(ctx, key, run)
}

func TestPlatformRoutesOnOneAppSerialize(t *testing.T) {
	keys := &keyLocks{locks: map[string]*sync.Mutex{}}
	svc, st, platform, _, _ := platformApplyFixture(t, "www.example.com", subdomainRecords())
	other := &kubeRouteStore{zone: st.zone, binding: platformBinding(), commit: platformCommit}
	second := NewService(Deps{Store: other, Domains: other, Authz: cleanupTestAuth{}, Adapters: svc.Adapters, Connections: svc.Connections, Resolver: svc.Resolver})
	second.now, second.routeHTTPS = svc.now, svc.routeHTTPS
	if _, err := second.PreviewRoute(t.Context(), kubeRouteManager, "shop", platformInput("shop.example.com")); err != nil {
		t.Fatal(err)
	}
	other.op.Status = "pending"
	if routeLockKey(st.op.Route) != routeLockKey(other.op.Route) {
		t.Fatal("lock keys", routeLockKey(st.op.Route), routeLockKey(other.op.Route))
	}
	svc.Store, svc.Domains = lockingRouteStore{st, keys}, lockingRouteStore{st, keys}
	second.Store, second.Domains = lockingRouteStore{other, keys}, lockingRouteStore{other, keys}
	platform.hold = 50 * time.Millisecond

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, run := range []struct {
		svc *Service
		id  string
	}{{svc, st.op.ID}, {second, other.op.ID}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = run.svc.ReconcileRoute(context.Background(), "org", run.id, nil)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if !errors.Is(err, ErrPending) {
			t.Fatal(i, err)
		}
	}
	platform.mu.Lock()
	defer platform.mu.Unlock()
	if platform.overlapped {
		t.Fatal("two attaches on one app overlapped")
	}
	if len(platform.spec) != 2 || !slices.Contains(platform.spec, "www.example.com") || !slices.Contains(platform.spec, "shop.example.com") {
		t.Fatal("spec", platform.spec)
	}
}

func TestPlatformRouteRefusesHandoff(t *testing.T) {
	const moving = "Moving a hostname between App Platform apps comes later. Remove it and add it again."
	svc, st, platform, _, _ := platformApplyFixture(t, "www.example.com", subdomainRecords())
	route := st.op.Route
	route.Status, route.Revision = "ready", 2
	st.route = &route
	_, err := svc.PrepareRouteHandoff(t.Context(), kubeRouteManager, "shop", route.ID, HandoffInput{Revision: 2})
	if !errors.Is(err, ErrInvalid) || err.Error() != ErrInvalid.Error()+": "+moving {
		t.Fatal("from a platform route", err)
	}
	// A server route can't move onto App Platform either.
	route.Binding = RouteBinding{ServerID: "srv_1", Address: "192.0.2.10"}
	st.route = &route
	_, err = svc.PrepareRouteHandoff(t.Context(), kubeRouteManager, "shop", route.ID, HandoffInput{Revision: 2, DestinationCloudID: "cloud_1"})
	if !errors.Is(err, ErrInvalid) || err.Error() != ErrInvalid.Error()+": "+moving {
		t.Fatal("onto a platform binding", err)
	}

	// Only apply, verify and remove run on a platform route.
	for _, kind := range []string{"prepare", "cutover", "rollback", "release"} {
		op := st.op
		op.Kind = kind
		var unsupported *provider.UnsupportedCapabilityError
		if err = svc.executePlatformRoute(t.Context(), st, &op, st.zone); !errors.As(err, &unsupported) || unsupported.Capability != provider.IngressManagement {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	if len(platform.calls) != 0 {
		t.Fatal("an unsupported kind reached the platform", platform.calls)
	}

	// A release or a compensation leaves a platform route alone.
	untouched := untouchedRouteStore{t: t}
	skipping := NewService(Deps{Domains: untouched, Authz: cleanupTestAuth{}})
	route = st.op.Route
	route.Status = "ready"
	in := ReleaseRouteRequest{Key: "plan/action", ProjectID: "shop", EnvironmentID: "prod", Service: "web", Routes: []Route{route}, DrainSeconds: 60}
	if err = skipping.ReconcileReleaseRoutes(t.Context(), kubeRouteManager, in); err != nil {
		t.Fatal("release", err)
	}
	if err = skipping.ReconcileCompensationRoutes(t.Context(), kubeRouteManager, in); err != nil {
		t.Fatal("compensation", err)
	}
}

func TestPlatformRouteOnAnExternalZoneWritesNoRecords(t *testing.T) {
	svc, st, _, dns, _ := platformRouteFixtureExternal(t)
	err := svc.ReconcileRoute(t.Context(), "org", st.op.ID, nil)
	if !errors.Is(err, ErrPending) || dns.writes != 0 || len(st.op.DesiredRecords) != 1 || st.op.Receipts != nil {
		t.Fatal(err, dns.writes, st.op.DesiredRecords, st.op.Receipts)
	}
	if status := DeriveRouteStatus(*st.route, st.zone, &st.op, svc.now()); status.DNS != DomainAwaitingVerification {
		t.Fatalf("before the owner adds the record: %+v", status)
	}
	// The zone's owner adds the record and the certificate issues.
	svc.Resolver = cnameResolver{cnames: map[string]string{"www.example.com": "shop-abc.ondigitalocean.app"}}
	svc.routeHTTPS = func(context.Context, string) (time.Time, int, error) { return svc.now().AddDate(0, 3, 0), 200, nil }
	st.op.Status = "pending"
	if err = svc.ReconcileRoute(t.Context(), "org", st.op.ID, nil); err != nil || dns.writes != 0 {
		t.Fatal(err, st.op.Message, dns.writes)
	}
	if status := DeriveRouteStatus(*st.route, st.zone, &st.op, svc.now()); status.DNS != DomainReady || status.Route != DomainReady {
		t.Fatalf("after: %+v", status)
	}
}

// platformRouteFixtureExternal is platformApplyFixture for www.example.com
// in a zone whose records only its owner can write.
func platformRouteFixtureExternal(t *testing.T) (*Service, *kubeRouteStore, *fakePlatformDomains, *routeDNSAdapter, *[]int) {
	t.Helper()
	svc, st, platform, dns, saved := platformApplyFixture(t, "www.example.com", subdomainRecords())
	st.zone.Mode = "external"
	platform.status = PlatformDomainStatus{Attached: true, CertState: "ready"}
	return svc, st, platform, dns, saved
}

func TestRouteStatusReadsAZeroCertificateExpiryAsReady(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	zone := &Zone{Name: "example.com", Mode: "managed", VerifiedAt: now, ProofName: "_loomplane.example.com", ProofValue: "proof", ProofExpiresAt: now.Add(time.Hour)}
	route := Route{Status: "ready", Verification: RouteVerification{DNS: true, TLS: true, Backend: true, CertState: "ready"}}
	if status := DeriveRouteStatus(route, zone, nil, now); status.Certificate != DomainReady {
		t.Fatalf("no expiry: %+v", status)
	}
	route.Verification.CertExpires = now.Add(time.Hour)
	if status := DeriveRouteStatus(route, zone, nil, now); status.Certificate != DomainRenewing {
		t.Fatalf("about to expire: %+v", status)
	}
}
