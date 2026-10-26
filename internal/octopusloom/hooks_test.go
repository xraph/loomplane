package octopusloom

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

// hookStore is a Store in memory with any number of gateways. events
// records the calls that change something, in order, as "Call resource".
// Its writes honour the context, as Postgres does, and saveErr makes
// SaveSyncState fail.
type hookStore struct {
	mu       sync.Mutex
	gateways map[GatewayRef]*Gateway
	routes   []*Route
	events   *[]string
	saveErr  error
}

func newHookStore(events *[]string, gateways ...*Gateway) *hookStore {
	s := &hookStore{gateways: map[GatewayRef]*Gateway{}, events: events}
	for _, g := range gateways {
		s.gateways[g.GatewayRef] = g
	}
	return s
}

func (s *hookStore) record(call string, ref GatewayRef) {
	*s.events = append(*s.events, call+" "+ref.Resource)
}

func (s *hookStore) EnsureGateway(_ context.Context, g *Gateway) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("EnsureGateway", g.GatewayRef)
	if old, ok := s.gateways[g.GatewayRef]; ok {
		old.Provider, old.Namespace = g.Provider, g.Namespace
		*g = *cloneGateway(old)
		return nil
	}
	stored := cloneGateway(g)
	stored.Generation, stored.Revision = 1, 1
	s.gateways[g.GatewayRef] = stored
	*g = *cloneGateway(stored)
	return nil
}

func (s *hookStore) Gateway(_ context.Context, ref GatewayRef) (*Gateway, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.gateways[ref]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneGateway(g), nil
}

func (s *hookStore) Gateways(context.Context) ([]*Gateway, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Gateway, 0, len(s.gateways))
	for _, g := range s.gateways {
		out = append(out, cloneGateway(g))
	}
	slices.SortFunc(out, func(a, b *Gateway) int { return strings.Compare(a.Resource, b.Resource) })
	return out, nil
}

func (s *hookStore) SaveSyncState(ctx context.Context, ref GatewayRef, applied int64, state SyncState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	g, ok := s.gateways[ref]
	if !ok {
		return ErrNotFound
	}
	s.record("SaveSyncState", ref)
	g.AppliedGeneration, g.State = applied, state
	return nil
}

func (s *hookStore) BumpGeneration(ctx context.Context, ref GatewayRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.gateways[ref]
	if !ok {
		return ErrNotFound
	}
	s.record("BumpGeneration", ref)
	g.Generation++
	return nil
}

func (s *hookStore) DeleteGateway(_ context.Context, ref GatewayRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("DeleteGateway", ref)
	delete(s.gateways, ref)
	return nil
}

func (s *hookStore) Routes(_ context.Context, ref GatewayRef) ([]*Route, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Route
	for _, r := range s.routes {
		if r.GatewayRef == ref {
			c := *r
			out = append(out, &c)
		}
	}
	return out, nil
}

func (s *hookStore) WithGatewayLock(_ context.Context, _ GatewayRef, run func() error) error {
	return run()
}

func (s *hookStore) SetFarpHostnames(context.Context, GatewayRef, []string, int64) (*Gateway, error) {
	return nil, errStoreNotUsed
}
func (s *hookStore) CreateRoute(context.Context, *Route) error { return errStoreNotUsed }
func (s *hookStore) UpdateRoute(context.Context, *Route) error { return errStoreNotUsed }
func (s *hookStore) DeleteRoute(context.Context, GatewayRef, string, int64) error {
	return errStoreNotUsed
}

var _ Store = (*hookStore)(nil)

// hookFixture is an environment with the gateways edge and inner on a
// server, each with its own fake Octopus, the services auth and billing,
// and a gateway "edge" of the same name in another environment.
type hookFixture struct {
	t        *testing.T
	events   []string
	store    *hookStore
	octo     map[string]*fakeOctopus
	bindings []*planning.Binding
	syncer   *Syncer
	revoked  []string
	hooks    *Hooks
	// openErr makes Open fail for the resource it names.
	openErr map[string]error
	// block makes Open wait until its context is done.
	block bool
	// delay makes Open wait that long first; inFlight and maxInFlight
	// count the Opens running at once.
	delay       time.Duration
	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	opens       []string
	warns       []string
}

func hookRef(resource string) GatewayRef {
	return GatewayRef{OrgID: "org_x", ProjectID: "proj_x", EnvironmentID: "env_x", Resource: resource}
}

func newHookFixture(t *testing.T) *hookFixture {
	t.Helper()
	f := &hookFixture{t: t, octo: map[string]*fakeOctopus{}, openErr: map[string]error{}}
	elsewhere := hookRef("edge")
	elsewhere.EnvironmentID = "env_other"
	f.store = newHookStore(&f.events,
		&Gateway{GatewayRef: hookRef("edge"), Provider: ProviderServer, Generation: 2, AppliedGeneration: 2},
		&Gateway{GatewayRef: hookRef("inner"), Provider: ProviderServer, Generation: 5, AppliedGeneration: 5},
		&Gateway{GatewayRef: elsewhere, Provider: ProviderServer, Generation: 7, AppliedGeneration: 7},
	)
	for _, name := range []string{"edge", "inner"} {
		f.octo[name] = newFakeOctopus(t)
	}
	f.bindings = []*planning.Binding{
		{Subject: planning.ResourceSubject, Name: "edge--gateway", ConnectionID: "conn_a", ExternalID: testInstance},
		{Subject: planning.ResourceSubject, Name: "inner--gateway", ConnectionID: "conn_a", ExternalID: testInstance},
		serviceBinding(t, "auth", "conn_a", planning.ServiceConfig{Port: 9000}),
		serviceBinding(t, "billing", "conn_a", planning.ServiceConfig{Port: 8080}),
	}
	f.syncer = &Syncer{
		Store: f.store,
		Open: func(ctx context.Context, ref GatewayRef) (*deployments.ComponentChannel, error) {
			f.mu.Lock()
			f.opens = append(f.opens, ref.Resource)
			f.inFlight++
			f.maxInFlight = max(f.maxInFlight, f.inFlight)
			octo, openErr := f.octo[ref.Resource], f.openErr[ref.Resource]
			f.mu.Unlock()
			defer func() {
				f.mu.Lock()
				f.inFlight--
				f.mu.Unlock()
			}()
			if f.delay > 0 {
				time.Sleep(f.delay)
			}
			if f.block {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			if openErr != nil {
				return nil, openErr
			}
			if octo == nil {
				return nil, fmt.Errorf("no Octopus for %s", ref.Resource)
			}
			return octo.channel(), nil
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
		Bindings: func(_ context.Context, _, environmentID string) ([]*planning.Binding, error) {
			if environmentID != "env_x" {
				return nil, nil
			}
			return f.bindings, nil
		},
		Hostnames: NoHostnames(),
		Now:       func() time.Time { return t0 },
		Warn: func(msg string, err error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.warns = append(f.warns, fmt.Sprintf("%s: %v", msg, err))
		},
	}
	f.hooks = NewHooks(HookDeps{
		Store: f.store, Syncer: f.syncer,
		Revoker: revokerFunc(func(_ context.Context, orgID, environmentID, resource string) error {
			f.revoked = append(f.revoked, orgID+"/"+environmentID+"/"+resource)
			f.events = append(f.events, "RevokeResource "+resource)
			return nil
		}),
	})
	return f
}

type revokerFunc func(ctx context.Context, orgID, environmentID, resource string) error

func (r revokerFunc) RevokeResource(ctx context.Context, orgID, environmentID, resource string) error {
	return r(ctx, orgID, environmentID, resource)
}

func (f *hookFixture) gateway(resource string) *Gateway {
	f.t.Helper()
	g, err := f.store.Gateway(context.Background(), hookRef(resource))
	if err != nil {
		f.t.Fatal(err)
	}
	return g
}

func (f *hookFixture) run(resource string) deployments.ComponentRun {
	return deployments.ComponentRun{
		OrgID: "org_x", ProjectID: "proj_x", EnvironmentID: "env_x", Environment: "production",
		Resource: resource, Kind: recipes.OctopusKind, Provider: ProviderServer,
		Component: recipes.ResolvedComponent{Component: recipes.Component{Name: recipes.OctopusGateway}},
		Secrets:   map[string][]byte{"admin-api-token": []byte(fakeAdminToken), "farp-token": []byte(fakeFarpToken)},
		Channel: func(context.Context, string, string) (*deployments.ComponentChannel, error) {
			return nil, errors.New("this test opens no run channel")
		},
	}
}

func (f *hookFixture) countEvents(event string) int {
	n := 0
	for _, e := range f.events {
		if e == event {
			n++
		}
	}
	return n
}

func TestFilesRendersConfigAndEnsuresGateway(t *testing.T) {
	f := newHookFixture(t)
	run := f.run("shopedge")
	run.Provider = ProviderKubernetes
	run.TargetSettings = map[string]string{"namespace": "shop"}
	run.Secrets = map[string][]byte{"admin-api-token": []byte("admin-tok-0123"), "farp-token": []byte("farp-tok-4567")}
	files, err := f.hooks.Files(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[recipes.OctopusConfigPath] == nil {
		t.Fatalf("files = %v, want only %s", slices.Collect(maps.Keys(files)), recipes.OctopusConfigPath)
	}
	g := f.gateway("shopedge")
	if g.Provider != ProviderKubernetes || g.Namespace != "shop" {
		t.Fatalf("stored gateway = %+v", g)
	}
	cfg := string(files[recipes.OctopusConfigPath])
	if !strings.Contains(cfg, "namespace: shop") || !strings.Contains(cfg, "require_auth: true") {
		t.Fatalf("config =\n%s", cfg)
	}
	if strings.Contains(cfg, "admin-tok-0123") || strings.Contains(cfg, "farp-tok-4567") {
		t.Fatalf("the config holds a token:\n%s", cfg)
	}
}

func TestFilesRendersManualRoutesOnTheGatewaysConnection(t *testing.T) {
	f := newHookFixture(t)
	route := authRoute()
	route.GatewayRef = hookRef("edge")
	f.store.routes = []*Route{route}
	files, err := f.hooks.Files(context.Background(), f.run("edge"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(files[recipes.OctopusConfigPath])
	if !strings.Contains(cfg, "lp-octoroute_a") || !strings.Contains(cfg, "lp-auth-9000") {
		t.Fatalf("the route isn't in the config:\n%s", cfg)
	}
}

func TestFilesBeforeTheGatewayBindingExists(t *testing.T) {
	f := newHookFixture(t)
	f.bindings = f.bindings[2:] // a first deploy: no gateway binding yet
	files, err := f.hooks.Files(context.Background(), f.run("fresh"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files[recipes.OctopusConfigPath]) == 0 {
		t.Fatal("no config rendered")
	}
}

func TestFilesBumpsTheGenerationWhenTheTargetMoves(t *testing.T) {
	f := newHookFixture(t)
	if _, err := f.hooks.Files(context.Background(), f.run("edge")); err != nil {
		t.Fatal(err)
	}
	if got := f.gateway("edge").Generation; got != 2 {
		t.Fatalf("an unchanged gateway's generation = %d, want 2", got)
	}
	run := f.run("edge")
	run.Provider = ProviderKubernetes
	run.TargetSettings = map[string]string{"namespace": "shop"}
	if _, err := f.hooks.Files(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if got := f.gateway("edge").Generation; got != 3 {
		t.Fatalf("a moved gateway's generation = %d, want 3", got)
	}
}

func TestFilesOnlyRendersTheGatewayComponent(t *testing.T) {
	f := newHookFixture(t)
	run := f.run("edge")
	run.Component.Name = "other"
	files, err := f.hooks.Files(context.Background(), run)
	if err != nil || len(files) != 0 {
		t.Fatalf("files = %v, %v", files, err)
	}
	if len(f.events) != 0 {
		t.Fatalf("store calls %v", f.events)
	}
}

func TestReadySyncsOverTheRunChannel(t *testing.T) {
	f := newHookFixture(t)
	f.syncer.Open = func(context.Context, GatewayRef) (*deployments.ComponentChannel, error) {
		t.Error("Ready opened the internal channel")
		return nil, errors.New("not here")
	}
	f.syncer.Secret = func(context.Context, GatewayRef, string) ([]byte, error) {
		t.Error("Ready read an internal secret")
		return nil, errors.New("not here")
	}
	octo := f.octo["edge"]
	run := f.run("edge")
	// The fake Octopus takes only these tokens, so a sync that applies
	// read them from the run: the Syncer's own secrets fail the test.
	run.Secrets = map[string][]byte{"admin-api-token": []byte(fakeAdminToken), "farp-token": []byte(fakeFarpToken)}
	var channels []string
	run.Channel = func(_ context.Context, component, port string) (*deployments.ComponentChannel, error) {
		channels = append(channels, component+"/"+port)
		return octo.channel(), nil
	}
	if err := f.hooks.Ready(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || channels[0] != "gateway/admin" {
		t.Fatalf("run channels %v, want [gateway/admin]", channels)
	}
	reqs := octo.log()
	if len(reqs) == 0 {
		t.Fatal("Ready sent Octopus nothing")
	}
	for _, r := range reqs {
		if strings.HasPrefix(r.Path, "/admin/") && r.Header.Get("X-Octopus-Admin-Token") != fakeAdminToken {
			t.Fatalf("%s %s sent token %q, want the run's", r.Method, r.Path, r.Header.Get("X-Octopus-Admin-Token"))
		}
	}
	if g := f.gateway("edge"); g.State.Error != "" || g.AppliedGeneration != g.Generation {
		t.Fatalf("sync error %q, applied %d of %d", g.State.Error, g.AppliedGeneration, g.Generation)
	}
}

func TestReadyIsNotAnErrorWhileTheGatewayIsPending(t *testing.T) {
	f := newHookFixture(t)
	f.bindings = f.bindings[2:] // a first deploy: no gateway binding yet
	if err := f.hooks.Ready(context.Background(), f.run("edge")); err != nil {
		t.Fatalf("Ready on a first deploy: %v", err)
	}
	if len(f.warns) != 1 || !strings.Contains(f.warns[0], msgNotRunning) {
		t.Fatalf("warnings %q", f.warns)
	}
	for _, w := range f.warns {
		if strings.Contains(w, fakeAdminToken) || strings.Contains(w, fakeFarpToken) {
			t.Fatalf("a warning holds a token: %q", w)
		}
	}

	// Octopus not answering doesn't fail the deploy either.
	f = newHookFixture(t)
	run := f.run("edge")
	run.Channel = func(context.Context, string, string) (*deployments.ComponentChannel, error) {
		return nil, errors.New("connection refused")
	}
	if err := f.hooks.Ready(context.Background(), run); err != nil {
		t.Fatalf("Ready with Octopus down: %v", err)
	}
}

func TestReadyFailsWhenLoomplaneCantReadTheSecrets(t *testing.T) {
	f := newHookFixture(t)
	run := f.run("edge")
	run.Secrets = map[string][]byte{}
	err := f.hooks.Ready(context.Background(), run)
	if err == nil {
		t.Fatal("Ready without the admin token succeeded")
	}
}

func TestRemovedRevokesAdminSessionsAndDeletesGateway(t *testing.T) {
	f := newHookFixture(t)
	err := f.hooks.Removed(context.Background(), deployments.ComposedResource{
		OrgID: "org_x", ProjectID: "proj_x", EnvironmentID: "env_x", Resource: "edge", Kind: recipes.OctopusKind,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.revoked, []string{"org_x/env_x/edge"}) {
		t.Fatalf("revoked %v", f.revoked)
	}
	if !slices.Equal(f.events, []string{"RevokeResource edge", "DeleteGateway edge"}) {
		t.Fatalf("events %v, want the sessions revoked before the gateway goes", f.events)
	}
	if _, err := f.store.Gateway(context.Background(), hookRef("edge")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("gateway still there: %v", err)
	}
}

func TestRemovedDeletesTheGatewayWhenSessionsCantBeEnded(t *testing.T) {
	f := newHookFixture(t)
	f.hooks.d.Revoker = revokerFunc(func(context.Context, string, string, string) error { return errors.New("db down") })
	err := f.hooks.Removed(context.Background(), deployments.ComposedResource{OrgID: "org_x", ProjectID: "proj_x", EnvironmentID: "env_x", Resource: "edge"})
	if err != nil {
		t.Fatalf("Removed: %v, want only a warning", err)
	}
	if f.countEvents("DeleteGateway edge") != 1 {
		t.Fatalf("events %v, want the gateway deleted", f.events)
	}
	if len(f.warns) != 1 || !strings.Contains(f.warns[0], "edge") || !strings.Contains(f.warns[0], "db down") {
		t.Fatalf("warnings %q", f.warns)
	}
}

func farpBilling(t *testing.T, gateway string) *planning.Binding {
	t.Helper()
	return serviceBinding(t, "billing", "conn_a", planning.ServiceConfig{Port: 8080, Farp: &planning.FarpConfig{
		Gateway: gateway, Port: 8080, Metadata: map[string]string{"farp.openapi.path": "/openapi.json"},
	}})
}

func release(b *planning.Binding, removed bool) deployments.Release {
	return deployments.Release{OrgID: "org_x", ProjectID: "proj_x", EnvironmentID: "env_x", Binding: b, Removed: removed}
}

func TestReleasedSyncsAffectedGatewaysOnly(t *testing.T) {
	f := newHookFixture(t)
	billing := farpBilling(t, "edge")
	f.bindings[3] = billing
	if err := f.hooks.Released(context.Background(), release(billing, false)); err != nil {
		t.Fatal(err)
	}
	edge, inner := f.gateway("edge"), f.gateway("inner")
	if edge.Generation != 3 || edge.AppliedGeneration != 3 {
		t.Fatalf("edge = generation %d, applied %d; want both 3", edge.Generation, edge.AppliedGeneration)
	}
	if _, ok := edge.State.Registrations["billing"]; !ok {
		t.Fatalf("billing wasn't registered: %+v", edge.State.Registrations)
	}
	if inner.Generation != 5 || f.countEvents("BumpGeneration inner") != 0 || f.countEvents("SaveSyncState inner") != 0 {
		t.Fatalf("inner was touched: %v", f.events)
	}
	if len(f.octo["inner"].log()) != 0 {
		t.Fatal("inner's Octopus was called")
	}
	if !slices.Equal(f.opens, []string{"edge"}) {
		t.Fatalf("opened %v", f.opens)
	}
	other := hookRef("edge")
	other.EnvironmentID = "env_other"
	if g, _ := f.store.Gateway(context.Background(), other); g.Generation != 7 {
		t.Fatal("the gateway of another environment was bumped")
	}
}

func TestReleasedSyncsGatewaysWithARouteOrRegistrationForTheService(t *testing.T) {
	f := newHookFixture(t)
	route := authRoute()
	route.GatewayRef = hookRef("inner")
	f.store.routes = []*Route{route}
	f.store.gateways[hookRef("edge")].State.Registrations = map[string]Registered{"billing": {InstanceID: "lp-svc-billing"}}

	// auth has a manual route on inner.
	if err := f.hooks.Released(context.Background(), release(f.bindings[2], false)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.opens, []string{"inner"}) {
		t.Fatalf("auth's release synced %v, want [inner]", f.opens)
	}

	// billing opted out and was removed, and edge still has it registered:
	// syncing edge deregisters it.
	f.opens = nil
	removed := f.bindings[3]
	f.bindings = f.bindings[:3]
	if err := f.hooks.Released(context.Background(), release(removed, true)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.opens, []string{"edge"}) {
		t.Fatalf("billing's removal synced %v, want [edge]", f.opens)
	}
	if _, ok := f.gateway("edge").State.Registrations["billing"]; ok {
		t.Fatal("billing is still registered")
	}
}

func TestReleasedIgnoresWhatIsntAService(t *testing.T) {
	f := newHookFixture(t)
	if err := f.hooks.Released(context.Background(), release(nil, false)); err != nil {
		t.Fatal(err)
	}
	resource := &planning.Binding{Subject: planning.ResourceSubject, Name: "edge"}
	if err := f.hooks.Released(context.Background(), release(resource, false)); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 0 || len(f.opens) != 0 {
		t.Fatalf("events %v, opens %v", f.events, f.opens)
	}
}

func TestReleasedIsBoundedWhenOctopusIsDown(t *testing.T) {
	f := newHookFixture(t)
	if f.hooks.releaseTimeout != 10*time.Second {
		t.Fatalf("release bound = %s, want 10s", f.hooks.releaseTimeout)
	}
	f.hooks.releaseTimeout = 50 * time.Millisecond
	f.block = true
	billing := farpBilling(t, "edge")
	f.bindings[3] = billing
	start := time.Now()
	err := f.hooks.Released(context.Background(), release(billing, false))
	if err == nil {
		t.Fatal("Released succeeded with Octopus down")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Released took %s", took)
	}
	if strings.Contains(err.Error(), fakeAdminToken) || strings.Contains(err.Error(), fakeFarpToken) {
		t.Fatalf("the error holds a token: %v", err)
	}
	if g := f.gateway("edge"); g.Generation != 3 || g.AppliedGeneration != 2 {
		t.Fatalf("edge = generation %d, applied %d; want 3 and 2, for the cron", g.Generation, g.AppliedGeneration)
	}
}

type fakeAccess struct {
	channels []string
	secrets  []string
}

func (a *fakeAccess) InternalChannel(_ context.Context, orgID string, ref deployments.ComponentRef) (*deployments.ComponentChannel, error) {
	a.channels = append(a.channels, fmt.Sprintf("%s/%s/%s/%s/%s/%s", orgID, ref.ProjectID, ref.EnvironmentID, ref.Resource, ref.Component, ref.Port))
	return &deployments.ComponentChannel{}, nil
}

func (a *fakeAccess) InternalSecret(_ context.Context, orgID string, ref deployments.ResourceRef, name string) ([]byte, error) {
	a.secrets = append(a.secrets, fmt.Sprintf("%s/%s/%s/%s/%s", orgID, ref.ProjectID, ref.EnvironmentID, ref.Resource, name))
	return []byte("v"), nil
}

func TestInternalAccessReachesTheGatewaysAdminPort(t *testing.T) {
	a := &fakeAccess{}
	ref := hookRef("edge")
	if _, err := InternalOpener(a)(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := InternalSecrets(a)(context.Background(), ref, "admin-api-token"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.channels, []string{"org_x/proj_x/env_x/edge/gateway/admin"}) {
		t.Fatalf("channels %v", a.channels)
	}
	if !slices.Equal(a.secrets, []string{"org_x/proj_x/env_x/edge/admin-api-token"}) {
		t.Fatalf("secrets %v", a.secrets)
	}
}

func TestReleasedBumpsEveryAffectedGatewayBeforeSyncing(t *testing.T) {
	f := newHookFixture(t)
	f.hooks.releaseTimeout = 50 * time.Millisecond
	f.block = true
	// billing feeds edge over FARP and has a manual route on inner.
	billing := farpBilling(t, "edge")
	f.bindings[3] = billing
	route := authRoute()
	route.GatewayRef, route.Service, route.Port = hookRef("inner"), "billing", 8080
	f.store.routes = []*Route{route}
	if err := f.hooks.Released(context.Background(), release(billing, false)); err == nil {
		t.Fatal("Released succeeded with Octopus down")
	}
	for name, want := range map[string]int64{"edge": 3, "inner": 6} {
		g := f.gateway(name)
		if g.Generation != want {
			t.Errorf("%s's generation = %d, want %d: the hung sync kept it from being marked", name, g.Generation, want)
		}
		// What the sync did is recorded after the deadline passed.
		if g.State.Error != msgUnavailable {
			t.Errorf("%s's recorded sync error = %q, want %q", name, g.State.Error, msgUnavailable)
		}
	}
	if i, j := slices.Index(f.events, "BumpGeneration inner"), slices.Index(f.events, "SaveSyncState edge"); i < 0 || j < 0 || i > j {
		t.Fatalf("events %v, want every bump before the first sync records", f.events)
	}
}

func TestReadyFailsWhenTheSyncCantBeRecorded(t *testing.T) {
	f := newHookFixture(t)
	f.store.saveErr = errors.New("db down")
	run := f.run("edge")
	run.Channel = func(context.Context, string, string) (*deployments.ComponentChannel, error) {
		return nil, errors.New("connection refused")
	}
	// Octopus down alone is pending and not an error; the store failing to
	// record it is Loomplane's failure.
	err := f.hooks.Ready(context.Background(), run)
	if err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("Ready = %v, want the store's failure", err)
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Ready's error %v no longer says Octopus was unavailable", err)
	}
}
