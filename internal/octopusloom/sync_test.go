package octopusloom

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
)

var errStoreNotUsed = errors.New("memStore: not used by these tests")

// memStore is a Store in memory with one gateway. busy makes
// WithGatewayLock answer ErrBusy, as when another sync holds the lock.
type memStore struct {
	mu      sync.Mutex
	gateway *Gateway
	routes  []*Route
	busy    bool
	saves   int
}

func (m *memStore) Gateway(_ context.Context, ref GatewayRef) (*Gateway, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gateway == nil || m.gateway.GatewayRef != ref {
		return nil, ErrNotFound
	}
	return cloneGateway(m.gateway), nil
}

func (m *memStore) Routes(_ context.Context, ref GatewayRef) ([]*Route, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Route
	for _, r := range m.routes {
		if r.GatewayRef == ref {
			c := *r
			out = append(out, &c)
		}
	}
	return out, nil
}

func (m *memStore) SaveSyncState(_ context.Context, ref GatewayRef, applied int64, state SyncState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gateway == nil || m.gateway.GatewayRef != ref {
		return ErrNotFound
	}
	m.saves++
	m.gateway.AppliedGeneration = applied
	m.gateway.State = state
	return nil
}

func (m *memStore) WithGatewayLock(_ context.Context, _ GatewayRef, run func() error) error {
	if m.busy {
		return ErrBusy
	}
	return run()
}

func (m *memStore) saved() *Gateway {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneGateway(m.gateway)
}

func (m *memStore) EnsureGateway(context.Context, *Gateway) error    { return errStoreNotUsed }
func (m *memStore) Gateways(context.Context) ([]*Gateway, error)     { return nil, errStoreNotUsed }
func (m *memStore) BumpGeneration(context.Context, GatewayRef) error { return errStoreNotUsed }
func (m *memStore) DeleteGateway(context.Context, GatewayRef) error  { return errStoreNotUsed }
func (m *memStore) CreateRoute(context.Context, *Route) error        { return errStoreNotUsed }
func (m *memStore) UpdateRoute(context.Context, *Route) error        { return errStoreNotUsed }
func (m *memStore) DeleteRoute(context.Context, GatewayRef, string, int64) error {
	return errStoreNotUsed
}
func (m *memStore) SetFarpHostnames(context.Context, GatewayRef, []string, int64) (*Gateway, error) {
	return nil, errStoreNotUsed
}

func cloneGateway(g *Gateway) *Gateway {
	raw, err := json.Marshal(g)
	if err != nil {
		panic(err)
	}
	var out Gateway
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	out.GatewayRef = g.GatewayRef // not in the JSON
	return &out
}

// fakeHostnames is a HostnameSource that records which services it was
// asked about. A reconcile asks from several goroutines at once.
type fakeHostnames struct {
	mu        sync.Mutex
	hostnames []ServiceHostname
	services  []string
}

func (f *fakeHostnames) ServiceHostnames(_ context.Context, _, _, _, service string) ([]ServiceHostname, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.services = append(f.services, service)
	return f.hostnames, nil
}

// syncFixture is a gateway "edge" on a server, the services auth (9000) and
// billing (8080) on its connection, and a fake Octopus behind the channel.
type syncFixture struct {
	t         *testing.T
	ref       GatewayRef
	store     *memStore
	octo      *fakeOctopus
	syncer    *Syncer
	bindings  []*planning.Binding
	hostnames *fakeHostnames
	opens     int
}

func newSyncFixture(t *testing.T, routes ...*Route) *syncFixture {
	t.Helper()
	ref := GatewayRef{OrgID: "org_x", ProjectID: "proj_x", EnvironmentID: "env_x", Resource: "edge"}
	for _, r := range routes {
		r.GatewayRef = ref
	}
	f := &syncFixture{
		t: t, ref: ref, octo: newFakeOctopus(t),
		store: &memStore{routes: routes, gateway: &Gateway{
			GatewayRef: ref, Provider: ProviderServer, FarpHostnames: []string{"api.acme.dev"},
			Generation: 3, AppliedGeneration: 2,
		}},
		bindings: []*planning.Binding{
			{Subject: planning.ResourceSubject, Name: "edge--gateway", ConnectionID: "conn_a", ExternalID: testInstance},
			serviceBinding(t, "auth", "conn_a", planning.ServiceConfig{Port: 9000}),
			serviceBinding(t, "billing", "conn_a", planning.ServiceConfig{Port: 8080}),
		},
		hostnames: &fakeHostnames{hostnames: []ServiceHostname{{RouteID: "route_1", ZoneID: "zone_1", Hostname: "api.acme.dev", Status: "active"}}},
	}
	f.syncer = &Syncer{
		Store: f.store,
		Open: func(_ context.Context, got GatewayRef) (*deployments.ComponentChannel, error) {
			if got != ref {
				t.Errorf("Open(%+v)", got)
			}
			f.opens++
			return f.octo.channel(), nil
		},
		Secret: func(_ context.Context, _ GatewayRef, name string) ([]byte, error) {
			switch name {
			case "admin-api-token":
				return []byte(fakeAdminToken), nil
			case "farp-token":
				return []byte(fakeFarpToken), nil
			}
			return nil, errors.New("no secret " + name)
		},
		Bindings: func(_ context.Context, orgID, environmentID string) ([]*planning.Binding, error) {
			if orgID != ref.OrgID || environmentID != ref.EnvironmentID {
				t.Errorf("Bindings(%s, %s)", orgID, environmentID)
			}
			return f.bindings, nil
		},
		Hostnames: f.hostnames,
		Now:       func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) },
	}
	return f
}

// desired is what the fixture's gateway should hold, for seeding Octopus.
func (f *syncFixture) desired() Desired {
	g, err := f.store.Gateway(context.Background(), f.ref)
	if err != nil {
		f.t.Fatal(err)
	}
	routes, _ := f.store.Routes(context.Background(), f.ref)
	return BuildDesired(DesiredInput{Gateway: *g, ConnectionID: "conn_a", Bindings: f.bindings, Routes: routes, Hostnames: []string{"api.acme.dev"}})
}

func (f *syncFixture) sync() (SyncResult, error) {
	return f.syncer.Sync(context.Background(), f.ref)
}

// authRoute sends api.acme.dev/auth/* to auth:9000 with the prefix stripped.
func authRoute() *Route {
	return &Route{ID: "octoroute_a", Hostname: "api.acme.dev", Path: "/auth/*", Methods: []string{}, Service: "auth", Port: 9000, StripPrefix: true}
}

// loginRoute sends POST api.acme.dev/login to auth:9000.
func loginRoute() *Route {
	return &Route{ID: "octoroute_b", Hostname: "api.acme.dev", Path: "/login", Methods: []string{"POST"}, Service: "auth", Port: 9000}
}

func TestSyncPutsMissingRoutesAndUpstreams(t *testing.T) {
	f := newSyncFixture(t, authRoute())
	f.octo.binding = []string{"api.acme.dev"}

	res, err := f.sync()
	if err != nil {
		t.Fatal(err)
	}
	if res.State != "applied" || res.Generation != 3 || res.Restarted {
		t.Fatalf("result = %+v", res)
	}
	writes := f.octo.writes()
	want := []string{
		"PUT /admin/api/upstreams/lp-auth-9000",
		"PUT /admin/api/routes/lp-octoroute_a",
		"PUT /admin/api/routes/lp-octoroute_a-base",
	}
	if !reflect.DeepEqual(writes, want) {
		t.Fatalf("writes = %v, want %v", writes, want)
	}
	if got, want := f.octo.body("PUT /admin/api/upstreams/lp-auth-9000"),
		`{"strategy":"round_robin","instances":[{"id":"lp-auth-0","address":"`+testContainer+`","port":9000}]}`; strings.TrimSpace(got) != want {
		t.Fatalf("upstream body = %s, want %s", got, want)
	}
	if got, want := f.octo.body("PUT /admin/api/routes/lp-octoroute_a"),
		`{"path":"/auth/*","methods":[],"hosts":["api.acme.dev"],"upstream":"lp-auth-9000","strip_prefix":"/auth","priority":0}`; strings.TrimSpace(got) != want {
		t.Fatalf("route body = %s, want %s", got, want)
	}
	if got, want := f.octo.body("PUT /admin/api/routes/lp-octoroute_a-base"),
		`{"path":"/auth","methods":[],"hosts":["api.acme.dev"],"upstream":"lp-auth-9000","strip_prefix":"/auth","priority":0}`; strings.TrimSpace(got) != want {
		t.Fatalf("base route body = %s, want %s", got, want)
	}

	g := f.store.saved()
	if g.AppliedGeneration != g.Generation {
		t.Fatalf("AppliedGeneration = %d, Generation = %d", g.AppliedGeneration, g.Generation)
	}
	if !reflect.DeepEqual(g.State.OwnedUpstreams, []string{"lp-auth-9000"}) || g.State.StartTime != "2026-10-21 10:00:00" ||
		!g.State.SyncedAt.Equal(f.syncer.Now()) || g.State.Error != "" || len(g.State.RouteErrors) != 0 {
		t.Fatalf("saved state = %+v", g.State)
	}
	if f.octo.closed != f.opens || f.opens != 1 {
		t.Fatalf("opened %d channels and closed %d", f.opens, f.octo.closed)
	}
	if !reflect.DeepEqual(f.hostnames.services, []string{"edge--gateway"}) {
		t.Fatalf("hostnames read for %v", f.hostnames.services)
	}
}

func TestSyncIsANoOpWhenOctopusMatches(t *testing.T) {
	f := newSyncFixture(t, authRoute(), loginRoute())
	if _, err := f.sync(); err != nil {
		t.Fatal(err)
	}
	f.octo.reset()

	res, err := f.sync()
	if err != nil || res.State != "applied" {
		t.Fatalf("second sync = %+v, %v", res, err)
	}
	if w := f.octo.writes(); len(w) != 0 {
		t.Fatalf("second sync wrote %v", w)
	}
	if len(f.octo.log()) == 0 {
		t.Fatal("second sync read nothing from Octopus")
	}
}

func TestSyncRestoresRoutesAfterRestart(t *testing.T) {
	// The last deploy's config had only the auth route; the login route was
	// added at runtime and is gone after the restart.
	f := newSyncFixture(t, authRoute())
	f.octo.seed(f.desired(), "config")
	f.store.routes = append(f.store.routes, loginRoute())
	f.store.routes[1].GatewayRef = f.ref
	f.store.gateway.State = SyncState{StartTime: "2026-10-21 10:00:00", OwnedUpstreams: []string{"lp-auth-9000"}}
	f.octo.startTime = "2026-10-21 11:00:00"

	res, err := f.sync()
	if err != nil {
		t.Fatal(err)
	}
	puts := 0
	for _, w := range f.octo.writes() {
		if strings.HasPrefix(w, "PUT /admin/api/routes/") || strings.HasPrefix(w, "PUT /admin/api/upstreams/") {
			puts++
		}
	}
	if !res.Restarted || puts == 0 || res.Restored != puts || res.State != "applied" {
		t.Fatalf("result = %+v with %d PUTs (%v)", res, puts, f.octo.writes())
	}
	if !slices.Contains(f.octo.writes(), "PUT /admin/api/routes/lp-octoroute_b") {
		t.Fatalf("login route not restored: %v", f.octo.writes())
	}
	if got := f.store.saved().State.StartTime; got != "2026-10-21 11:00:00" {
		t.Fatalf("saved StartTime = %q", got)
	}

	f.octo.reset()
	res, err = f.sync()
	if err != nil || res.Restarted || res.Restored != 0 {
		t.Fatalf("next sync = %+v, %v", res, err)
	}
}

func TestSyncTreatsConfigAndAdminOriginAsTheSameRoute(t *testing.T) {
	f := newSyncFixture(t, authRoute())
	f.octo.seed(f.desired(), "config")
	f.store.gateway.State = SyncState{StartTime: "2026-10-21 10:00:00", OwnedUpstreams: []string{"lp-auth-9000"}}

	for _, origin := range []string{"config", "admin"} {
		f.octo.setOrigin(origin)
		f.octo.reset()
		res, err := f.sync()
		if err != nil || res.State != "applied" {
			t.Fatalf("origin %s: %+v, %v", origin, res, err)
		}
		if w := f.octo.writes(); len(w) != 0 {
			t.Fatalf("origin %s: wrote %v", origin, w)
		}
	}
}

func TestSyncRemovesStaleConfigRoute(t *testing.T) {
	f := newSyncFixture(t, authRoute())
	f.octo.seed(f.desired(), "config")
	f.store.gateway.State = SyncState{StartTime: "2026-10-21 10:00:00", OwnedUpstreams: []string{"lp-auth-9000"}}
	f.octo.upstreams["lp-inst-01jbzfarp"] = UpstreamBody{Strategy: "round_robin", Instances: []UpstreamInstance{{ID: "i", Address: "billing", Port: 8080}}}
	f.octo.routes["lp-octoroute_gone"] = &fakeRoute{Origin: "config", RouteBody: RouteBody{Path: "/gone", Hosts: []string{"api.acme.dev"}, Upstream: "lp-auth-9000"}}
	// FARP routes on the same host, one with Loomplane's instance id as its
	// route id and one without a route id, and a route made in Octopus.
	f.octo.routes["lp-svc-billing"] = &fakeRoute{Origin: "farp", RouteBody: RouteBody{Path: "/billing/*", Hosts: []string{"api.acme.dev"}, Upstream: "lp-inst-01jbzfarp"}}
	f.octo.routes["farp-anon"] = &fakeRoute{Origin: "farp", NoRouteID: true, RouteBody: RouteBody{Path: "/invoices", Hosts: []string{"api.acme.dev"}, Upstream: "lp-inst-01jbzfarp"}}
	f.octo.routes["manual-1"] = &fakeRoute{Origin: "admin", RouteBody: RouteBody{Path: "/manual", Upstream: "lp-auth-9000"}}

	res, err := f.sync()
	if err != nil || res.State != "applied" {
		t.Fatalf("%+v, %v", res, err)
	}
	if w, want := f.octo.writes(), []string{"DELETE /admin/api/routes/lp-octoroute_gone"}; !reflect.DeepEqual(w, want) {
		t.Fatalf("writes = %v, want %v", w, want)
	}
}

func TestSyncDeletesOnlyUpstreamsItOwns(t *testing.T) {
	f := newSyncFixture(t, authRoute())
	f.octo.seed(f.desired(), "config")
	f.octo.upstreams["lp-inst-01jbzfarp"] = UpstreamBody{Strategy: "round_robin", Instances: []UpstreamInstance{{ID: "i", Address: "billing", Port: 8080}}}
	f.octo.upstreams["lp-billing-8080"] = UpstreamBody{Strategy: "round_robin", Instances: []UpstreamInstance{{ID: "lp-billing-0", Address: testContainer, Port: 8080}}}
	f.store.gateway.State = SyncState{StartTime: "2026-10-21 10:00:00", OwnedUpstreams: []string{"lp-billing-8080"}}

	res, err := f.sync()
	if err != nil || res.State != "applied" {
		t.Fatalf("%+v, %v", res, err)
	}
	if w, want := f.octo.writes(), []string{"DELETE /admin/api/upstreams/lp-billing-8080"}; !reflect.DeepEqual(w, want) {
		t.Fatalf("writes = %v, want %v", w, want)
	}
	if _, ok := f.octo.upstreams["lp-inst-01jbzfarp"]; !ok {
		t.Fatal("the FARP upstream is gone")
	}
	if got := f.store.saved().State.OwnedUpstreams; !reflect.DeepEqual(got, []string{"lp-auth-9000"}) {
		t.Fatalf("OwnedUpstreams = %v", got)
	}
}

func TestSyncReportsOctopusRouteConflict(t *testing.T) {
	f := newSyncFixture(t, authRoute(), loginRoute())
	f.octo.binding = []string{"api.acme.dev"}
	f.octo.conflicts["lp-octoroute_a"] = "manual-1"

	res, err := f.sync()
	if err == nil || res.State != "failed" {
		t.Fatalf("result = %+v, %v; want failed with an error", res, err)
	}
	if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrBusy) {
		t.Fatalf("a conflict isn't busy or unavailable: %v", err)
	}
	g := f.store.saved()
	want := "Octopus already has its own route for /auth/* on api.acme.dev (manual-1). Remove it in the Octopus admin, or change this route."
	if got := g.State.RouteErrors["octoroute_a"]; got != want {
		t.Fatalf("RouteErrors[octoroute_a] = %q, want %q", got, want)
	}
	if _, ok := g.State.RouteErrors["octoroute_b"]; ok {
		t.Fatalf("octoroute_b has an error: %v", g.State.RouteErrors)
	}
	if _, ok := f.octo.routes["lp-octoroute_b"]; !ok {
		t.Fatal("the login route wasn't applied")
	}
	if g.AppliedGeneration == g.Generation {
		t.Fatal("a failed sync recorded the generation as applied")
	}
	if g.State.Error == "" {
		t.Fatal("no State.Error for a failed sync")
	}
}

func TestSyncPushesFarpBinding(t *testing.T) {
	f := newSyncFixture(t)
	f.octo.binding = []string{}

	if _, err := f.sync(); err != nil {
		t.Fatal(err)
	}
	if w, want := f.octo.writes(), []string{"PUT /admin/api/farp/binding"}; !reflect.DeepEqual(w, want) {
		t.Fatalf("writes = %v, want %v", w, want)
	}
	if got := strings.TrimSpace(f.octo.body("PUT /admin/api/farp/binding")); got != `{"hostnames":["api.acme.dev"]}` {
		t.Fatalf("binding body = %s", got)
	}

	// No hostnames chosen makes FARP routes answer on any host.
	f.store.gateway.FarpHostnames = nil
	f.octo.reset()
	if _, err := f.sync(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(f.octo.body("PUT /admin/api/farp/binding")); got != `{"hostnames":[]}` {
		t.Fatalf("binding body = %s", got)
	}
}

func TestSyncUnreachableKeepsGenerationPending(t *testing.T) {
	const message = "Octopus isn't answering on its private port. Loomplane keeps the route and pushes it within 30 seconds."
	f := newSyncFixture(t, authRoute())
	f.store.gateway.State = SyncState{StartTime: "2026-10-21 10:00:00", OwnedUpstreams: []string{"lp-auth-9000"}}
	f.syncer.Open = func(context.Context, GatewayRef) (*deployments.ComponentChannel, error) {
		return nil, errors.New("dial tcp 10.0.0.7:9090: connect: connection refused")
	}

	res, err := f.sync()
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if res.State != "pending" || res.Message != message || res.Generation != 3 {
		t.Fatalf("result = %+v", res)
	}
	g := f.store.saved()
	if g.AppliedGeneration != 2 || g.State.Error == "" {
		t.Fatalf("saved %d, %+v", g.AppliedGeneration, g.State)
	}
	if !reflect.DeepEqual(g.State.OwnedUpstreams, []string{"lp-auth-9000"}) || g.State.StartTime != "2026-10-21 10:00:00" {
		t.Fatalf("an unreachable sync lost what was known: %+v", g.State)
	}

	// Octopus going away after the channel opened is the same.
	f = newSyncFixture(t, authRoute())
	f.octo.srv.Close()
	res, err = f.sync()
	if !errors.Is(err, ErrUnavailable) || res.State != "pending" || res.Message != message {
		t.Fatalf("closed Octopus: %+v, %v", res, err)
	}
	if f.store.saved().AppliedGeneration != 2 {
		t.Fatal("generation recorded as applied")
	}
	if f.octo.closed != 1 {
		t.Fatalf("channel closed %d times", f.octo.closed)
	}
}

func TestSyncBusyReturnsPending(t *testing.T) {
	f := newSyncFixture(t, authRoute())
	f.store.busy = true

	res, err := f.sync()
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if res.State != "pending" || res.Message == "" {
		t.Fatalf("result = %+v", res)
	}
	if f.opens != 0 || len(f.octo.log()) != 0 || f.store.saves != 0 {
		t.Fatalf("a busy sync opened %d channels, sent %d requests, saved %d times", f.opens, len(f.octo.log()), f.store.saves)
	}
}

func TestSyncWithUsesTheChannelAndSecretsGiven(t *testing.T) {
	f := newSyncFixture(t, authRoute())
	f.syncer.Open = nil
	f.syncer.Secret = nil
	var opened int
	var read []string
	open := func(context.Context, GatewayRef) (*deployments.ComponentChannel, error) {
		opened++
		return f.octo.channel(), nil
	}
	secret := func(_ context.Context, _ GatewayRef, name string) ([]byte, error) {
		read = append(read, name)
		if name == "admin-api-token" {
			return []byte(fakeAdminToken), nil
		}
		return []byte(fakeFarpToken), nil
	}

	res, err := f.syncer.SyncWith(context.Background(), f.ref, open, secret)
	if err != nil || res.State != "applied" {
		t.Fatalf("%+v, %v", res, err)
	}
	if opened != 1 || !slices.Contains(read, "admin-api-token") {
		t.Fatalf("opened %d, read %v", opened, read)
	}
}

func TestSyncPutsRouteThatDiffersInOctopus(t *testing.T) {
	f := newSyncFixture(t, authRoute(), loginRoute())
	f.octo.seed(f.desired(), "admin")
	f.store.gateway.State = SyncState{StartTime: "2026-10-21 10:00:00", OwnedUpstreams: []string{"lp-auth-9000"}}
	// Octopus holds the login route for GET instead of POST, the auth route
	// on any host, and the auth base route without its prefix stripped.
	f.octo.routes["lp-octoroute_b"].Methods = []string{"GET"}
	f.octo.routes["lp-octoroute_a"].Hosts = nil
	f.octo.routes["lp-octoroute_a-base"].StripPrefix = ""

	if _, err := f.sync(); err != nil {
		t.Fatal(err)
	}
	w := f.octo.writes()
	slices.Sort(w)
	want := []string{"PUT /admin/api/routes/lp-octoroute_a", "PUT /admin/api/routes/lp-octoroute_a-base", "PUT /admin/api/routes/lp-octoroute_b"}
	if !reflect.DeepEqual(w, want) {
		t.Fatalf("writes = %v, want %v", w, want)
	}
}

func TestSyncRecordsSkippedRoutes(t *testing.T) {
	gone := &Route{ID: "octoroute_c", Hostname: "api.acme.dev", Path: "/ledger", Methods: []string{}, Service: "ledger", Port: 7000}
	f := newSyncFixture(t, authRoute(), gone)
	f.octo.binding = []string{"api.acme.dev"}

	res, err := f.sync()
	if err != nil || res.State != "applied" {
		t.Fatalf("%+v, %v", res, err)
	}
	if got := f.store.saved().State.RouteErrors["octoroute_c"]; got != "ledger isn't deployed in this environment." {
		t.Fatalf("RouteErrors[octoroute_c] = %q", got)
	}
	if strings.Contains(strings.Join(f.octo.writes(), " "), "octoroute_c") {
		t.Fatalf("pushed a skipped route: %v", f.octo.writes())
	}
}

var _ http.RoundTripper = componentTransport{}

// desiredWith is what the fixture's gateway would hold with routes instead
// of its stored ones: an older deploy's config, for seeding Octopus.
func (f *syncFixture) desiredWith(routes ...*Route) Desired {
	g, err := f.store.Gateway(context.Background(), f.ref)
	if err != nil {
		f.t.Fatal(err)
	}
	return BuildDesired(DesiredInput{Gateway: *g, ConnectionID: "conn_a", Bindings: f.bindings, Routes: routes, Hostnames: []string{"api.acme.dev"}})
}

func TestSyncRetriesPastItsOwnStaleRoute(t *testing.T) {
	// The auth route was /auth/* when Octopus last loaded it, and is now
	// /auth. Its old base entry lp-octoroute_a-base holds /auth, so Octopus
	// refuses the new lp-octoroute_a until that entry is gone.
	edited := authRoute()
	edited.Path = "/auth"
	f := newSyncFixture(t, edited)
	f.octo.seed(f.desiredWith(authRoute()), "config")
	f.store.gateway.State = SyncState{StartTime: "2026-10-21 10:00:00", OwnedUpstreams: []string{"lp-auth-9000"}}

	res, err := f.sync()
	if err != nil || res.State != "applied" {
		t.Fatalf("%+v, %v", res, err)
	}
	want := []string{
		"PUT /admin/api/routes/lp-octoroute_a",
		"DELETE /admin/api/routes/lp-octoroute_a-base",
		"PUT /admin/api/routes/lp-octoroute_a",
	}
	if w := f.octo.writes(); !reflect.DeepEqual(w, want) {
		t.Fatalf("writes = %v, want %v", w, want)
	}
	if got := f.octo.routes["lp-octoroute_a"]; got == nil || got.Path != "/auth" {
		t.Fatalf("lp-octoroute_a = %+v", got)
	}
	if _, ok := f.octo.routes["lp-octoroute_a-base"]; ok {
		t.Fatal("the stale base entry is still there")
	}
	if g := f.store.saved(); len(g.State.RouteErrors) != 0 || g.AppliedGeneration != g.Generation {
		t.Fatalf("saved %d, %+v", g.AppliedGeneration, g.State)
	}

	// A route made in Octopus on the same path is still a conflict to report.
	f = newSyncFixture(t, edited)
	f.octo.binding = []string{"api.acme.dev"}
	f.octo.upstreams["lp-auth-9000"] = UpstreamBody{Strategy: "round_robin", Instances: []UpstreamInstance{{ID: "lp-auth-0", Address: testContainer, Port: 9000}}}
	f.octo.routes["manual-1"] = &fakeRoute{Origin: "admin", RouteBody: RouteBody{Path: "/auth", Hosts: []string{"api.acme.dev"}, Methods: []string{"GET"}, Upstream: "lp-auth-9000"}}
	res, err = f.sync()
	if err == nil || res.State != "failed" {
		t.Fatalf("%+v, %v", res, err)
	}
	if got := f.store.saved().State.RouteErrors["octoroute_a"]; !strings.Contains(got, "(manual-1)") {
		t.Fatalf("RouteErrors[octoroute_a] = %q", got)
	}
	if slices.Contains(f.octo.writes(), "DELETE /admin/api/routes/manual-1") {
		t.Fatal("deleted the route made in Octopus")
	}
}

func TestSyncKeepsUpstreamALiveRouteStillUses(t *testing.T) {
	// The auth route sent to billing:8080 and now sends to auth:9000.
	before := authRoute()
	before.Service, before.Port = "billing", 8080
	for name, setup := range map[string]func(f *syncFixture){
		"upstream refused": func(f *syncFixture) {
			f.octo.status["PUT /admin/api/upstreams/lp-auth-9000"] = http.StatusBadRequest
			f.octo.failBody = `{"error":"bad upstream"}`
		},
		"route refused": func(f *syncFixture) { f.octo.conflicts["lp-octoroute_a"] = "manual-1" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newSyncFixture(t, authRoute())
			f.octo.seed(f.desiredWith(before), "admin")
			f.store.gateway.State = SyncState{StartTime: "2026-10-21 10:00:00", OwnedUpstreams: []string{"lp-billing-8080"}}
			setup(f)

			res, err := f.sync()
			if err == nil || res.State != "failed" {
				t.Fatalf("%+v, %v", res, err)
			}
			if slices.Contains(f.octo.writes(), "DELETE /admin/api/upstreams/lp-billing-8080") {
				t.Fatalf("deleted the upstream a live route sends to: %v", f.octo.writes())
			}
			if got := f.store.saved().State.OwnedUpstreams; !slices.Contains(got, "lp-billing-8080") {
				t.Fatalf("OwnedUpstreams = %v, lost lp-billing-8080", got)
			}
		})
	}

	// Once the routes moved, the old upstream goes.
	f := newSyncFixture(t, authRoute())
	f.octo.seed(f.desiredWith(before), "admin")
	f.store.gateway.State = SyncState{StartTime: "2026-10-21 10:00:00", OwnedUpstreams: []string{"lp-billing-8080"}}
	if _, err := f.sync(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.octo.writes(), "DELETE /admin/api/upstreams/lp-billing-8080") {
		t.Fatalf("old upstream kept: %v", f.octo.writes())
	}
}

func TestSyncDeletesUpstreamsOfStaleConfigRoutesAfterRestart(t *testing.T) {
	// Octopus restarted on a config with a route deleted since: its upstream
	// was never recorded as owned. A stale route whose upstream a FARP route
	// also uses gives that upstream no owner.
	f := newSyncFixture(t, authRoute())
	f.octo.seed(f.desired(), "config")
	f.octo.startTime = "2026-10-21 11:00:00"
	f.store.gateway.State = SyncState{StartTime: "2026-10-21 10:00:00", OwnedUpstreams: []string{"lp-auth-9000"}}
	f.octo.upstreams["lp-gone-8080"] = UpstreamBody{Strategy: "round_robin", Instances: []UpstreamInstance{{ID: "lp-gone-0", Address: testContainer, Port: 8080}}}
	f.octo.routes["lp-octoroute_gone"] = &fakeRoute{Origin: "config", RouteBody: RouteBody{Path: "/gone", Hosts: []string{"api.acme.dev"}, Upstream: "lp-gone-8080"}}
	f.octo.upstreams["lp-inst-01jbzfarp"] = UpstreamBody{Strategy: "round_robin", Instances: []UpstreamInstance{{ID: "i", Address: "billing", Port: 8080}}}
	f.octo.routes["lp-octoroute_shared"] = &fakeRoute{Origin: "config", RouteBody: RouteBody{Path: "/shared", Upstream: "lp-inst-01jbzfarp"}}
	f.octo.routes["lp-svc-billing"] = &fakeRoute{Origin: "farp", RouteBody: RouteBody{Path: "/billing/*", Upstream: "lp-inst-01jbzfarp"}}

	res, err := f.sync()
	if err != nil || res.State != "applied" || !res.Restarted {
		t.Fatalf("%+v, %v", res, err)
	}
	want := []string{
		"DELETE /admin/api/routes/lp-octoroute_gone",
		"DELETE /admin/api/routes/lp-octoroute_shared",
		"DELETE /admin/api/upstreams/lp-gone-8080",
	}
	if w := f.octo.writes(); !reflect.DeepEqual(w, want) {
		t.Fatalf("writes = %v, want %v", w, want)
	}
	if got := f.store.saved().State.OwnedUpstreams; !reflect.DeepEqual(got, []string{"lp-auth-9000"}) {
		t.Fatalf("OwnedUpstreams = %v", got)
	}
}
