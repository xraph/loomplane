package octopusloom

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// serviceStore is a Store in memory with the Postgres store's revision and
// generation rules, for one gateway.
type serviceStore struct {
	mu      sync.Mutex
	gateway *Gateway
	routes  []*Route
	next    int
}

func (s *serviceStore) find(ref GatewayRef) error {
	if s.gateway == nil || s.gateway.OrgID != ref.OrgID || s.gateway.EnvironmentID != ref.EnvironmentID || s.gateway.Resource != ref.Resource {
		return ErrNotFound
	}
	return nil
}

func (s *serviceStore) EnsureGateway(context.Context, *Gateway) error { return errStoreNotUsed }

func (s *serviceStore) Gateway(_ context.Context, ref GatewayRef) (*Gateway, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.find(ref); err != nil {
		return nil, err
	}
	return cloneGateway(s.gateway), nil
}

func (s *serviceStore) Gateways(context.Context) ([]*Gateway, error) { return nil, errStoreNotUsed }

func (s *serviceStore) SetFarpHostnames(_ context.Context, ref GatewayRef, hostnames []string, revision int64) (*Gateway, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.find(ref); err != nil {
		return nil, err
	}
	if s.gateway.Revision != revision {
		return nil, ErrStale
	}
	s.gateway.FarpHostnames = slices.Clone(hostnames)
	s.gateway.Generation++
	s.gateway.Revision++
	return cloneGateway(s.gateway), nil
}

func (s *serviceStore) SaveSyncState(_ context.Context, ref GatewayRef, applied int64, state SyncState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.find(ref); err != nil {
		return err
	}
	s.gateway.AppliedGeneration, s.gateway.State = applied, state
	return nil
}

func (s *serviceStore) BumpGeneration(context.Context, GatewayRef) error { return errStoreNotUsed }
func (s *serviceStore) DeleteGateway(context.Context, GatewayRef) error  { return errStoreNotUsed }

func (s *serviceStore) Routes(_ context.Context, ref GatewayRef) ([]*Route, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.find(ref); err != nil {
		return nil, err
	}
	out := make([]*Route, 0, len(s.routes))
	for _, r := range s.routes {
		c := *r
		out = append(out, &c)
	}
	return out, nil
}

func (s *serviceStore) CreateRoute(_ context.Context, r *Route) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.find(r.GatewayRef); err != nil {
		return err
	}
	s.next++
	r.ID, r.Revision = fmt.Sprintf("octoroute_%d", s.next), 1
	c := *r
	s.routes = append(s.routes, &c)
	s.gateway.Generation++
	return nil
}

func (s *serviceStore) UpdateRoute(_ context.Context, r *Route) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.find(r.GatewayRef); err != nil {
		return err
	}
	for i, stored := range s.routes {
		if stored.ID != r.ID {
			continue
		}
		if stored.Revision != r.Revision {
			return ErrStale
		}
		r.Revision++
		c := *r
		s.routes[i] = &c
		s.gateway.Generation++
		return nil
	}
	return ErrNotFound
}

func (s *serviceStore) DeleteRoute(_ context.Context, ref GatewayRef, id string, revision int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.find(ref); err != nil {
		return err
	}
	for i, stored := range s.routes {
		if stored.ID != id {
			continue
		}
		if stored.Revision != revision {
			return ErrStale
		}
		s.routes = slices.Delete(s.routes, i, i+1)
		s.gateway.Generation++
		return nil
	}
	return ErrNotFound
}

func (s *serviceStore) WithGatewayLock(_ context.Context, _ GatewayRef, run func() error) error {
	return run()
}

func (s *serviceStore) stored() []*Route {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.routes)
}

var _ Store = (*serviceStore)(nil)

// svcAuthz lets everyone read and admins modify, and never lets a
// read-only key write, as security.Authorizer does.
type svcAuthz struct {
	mu     sync.Mutex
	checks []string
}

func (a *svcAuthz) Require(_ context.Context, p security.Principal, perm security.Permission, resourceID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.checks = append(a.checks, p.UserID+" "+perm.Name()+" "+resourceID)
	switch {
	case p.ReadOnly && !perm.Reads():
		return fmt.Errorf("%w (%s): this API key can only read", security.ErrForbidden, perm.Name())
	case p.UserID == "outsider":
		return fmt.Errorf("%w (%s)", security.ErrForbidden, perm.Name())
	case !perm.Reads() && p.UserID != "admin":
		return fmt.Errorf("%w (%s)", security.ErrForbidden, perm.Name())
	}
	return nil
}

// svcAccess reaches the fake Octopus as a principal. down makes the
// channel fail as an unreachable gateway does.
type svcAccess struct {
	octo  *fakeOctopus
	down  bool
	calls []string
}

func (a *svcAccess) Channel(_ context.Context, p security.Principal, need security.Permission, ref deployments.ComponentRef) (*deployments.ComponentChannel, error) {
	a.calls = append(a.calls, fmt.Sprintf("channel %s %s %s/%s/%s:%s", p.UserID, need.Name(), ref.ProjectID, ref.Resource, ref.Component, ref.Port))
	if a.down {
		return nil, errors.New("dial lp-inst-01h455vb4pex5vsknk084sn02q:9090: connection refused")
	}
	return a.octo.channel(), nil
}

func (a *svcAccess) GeneratedSecret(_ context.Context, p security.Principal, need security.Permission, ref deployments.ResourceRef, name string) ([]byte, error) {
	a.calls = append(a.calls, fmt.Sprintf("secret %s %s %s/%s %s", p.UserID, need.Name(), ref.ProjectID, ref.Resource, name))
	switch name {
	case "admin-api-token":
		return []byte(fakeAdminToken), nil
	case "farp-token":
		return []byte(fakeFarpToken), nil
	}
	return nil, errors.New("no secret " + name)
}

type svcDomains struct{ calls []string }

func (d *svcDomains) RouteStatus(_ context.Context, p security.Principal, project, id string) (cloudinfra.RouteStatus, error) {
	d.calls = append(d.calls, p.UserID+" "+project+" "+id)
	return cloudinfra.RouteStatus{RouteID: id, Route: cloudinfra.DomainReady}, nil
}

// countingSyncer is a GatewaySyncer that answers from results in turn,
// repeating the last.
type countingSyncer struct {
	mu      sync.Mutex
	calls   int
	results []func() (SyncResult, error)
}

func (s *countingSyncer) Sync(context.Context, GatewayRef) (SyncResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := min(s.calls, len(s.results)-1)
	s.calls++
	return s.results[i]()
}

func (s *countingSyncer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func applied() (SyncResult, error) { return SyncResult{State: SyncApplied}, nil }

func busy() (SyncResult, error) {
	return SyncResult{State: SyncPending, Message: msgBusy}, fmt.Errorf("sync: %w", ErrBusy)
}

type serviceFixture struct {
	t        *testing.T
	ref      GatewayRef
	store    *serviceStore
	octo     *fakeOctopus
	access   *svcAccess
	authz    *svcAuthz
	domains  *svcDomains
	syncer   *countingSyncer
	bindings []*planning.Binding
	hosts    *fakeHostnames
	svc      *Service
}

var (
	admin     = security.Principal{OrgID: "org_x", UserID: "admin"}
	developer = security.Principal{OrgID: "org_x", UserID: "developer"}
	readOnly  = security.Principal{OrgID: "org_x", UserID: "admin", KeyID: "key_1", ReadOnly: true}
)

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	ref := GatewayRef{OrgID: "org_x", ProjectID: "proj_x", EnvironmentID: "env_x", Resource: "edge"}
	f := &serviceFixture{
		t: t, ref: ref, octo: newFakeOctopus(t), authz: &svcAuthz{}, domains: &svcDomains{},
		store: &serviceStore{gateway: &Gateway{GatewayRef: ref, Provider: ProviderServer, Generation: 2, AppliedGeneration: 2, Revision: 3}},
		bindings: []*planning.Binding{
			gatewayBinding(t),
			serviceBinding(t, "auth", "conn_a", planning.ServiceConfig{Port: 9000}),
			serviceBinding(t, "billing", "conn_a", planning.ServiceConfig{Port: 8080}),
			serviceBinding(t, "jobs", "conn_b", planning.ServiceConfig{Port: 8080}),
		},
		hosts:  &fakeHostnames{hostnames: []ServiceHostname{{RouteID: "route_1", ZoneID: "zone_1", Hostname: "api.acme.dev", Status: "ready"}}},
		syncer: &countingSyncer{results: []func() (SyncResult, error){applied}},
	}
	f.access = &svcAccess{octo: f.octo}
	f.svc = NewService(ServiceDeps{
		Store: f.store, Syncer: f.syncer, Access: f.access, Hostnames: f.hosts, Authz: f.authz, Domains: f.domains,
		Bindings: func(_ context.Context, orgID, environmentID string) ([]*planning.Binding, error) {
			if orgID != ref.OrgID || environmentID != ref.EnvironmentID {
				t.Errorf("Bindings(%s, %s)", orgID, environmentID)
			}
			return f.bindings, nil
		},
	})
	f.svc.busyEvery, f.svc.busyFor = time.Millisecond, 20*time.Millisecond
	return f
}

func authInput(host string) RouteInput {
	return RouteInput{Hostname: host, Path: "/auth/*", Service: "auth", Port: 9000, StripPrefix: true}
}

func TestCreateRouteNeedsInfrastructureModify(t *testing.T) {
	f := newServiceFixture(t)
	for _, p := range []security.Principal{developer, readOnly} {
		if _, err := f.svc.CreateRoute(context.Background(), p, f.ref, authInput("api.acme.dev")); !errors.Is(err, security.ErrForbidden) {
			t.Fatalf("%+v created a route: %v", p, err)
		}
	}
	if len(f.store.stored()) != 0 || f.syncer.count() != 0 {
		t.Fatalf("stored %v, %d syncs", f.store.stored(), f.syncer.count())
	}
	if !slices.Contains(f.authz.checks, "developer infrastructure:modify proj_x") {
		t.Fatalf("checks %v", f.authz.checks)
	}
	// Every other write needs it too.
	ctx := context.Background()
	for name, write := range map[string]func(security.Principal) error{
		"update": func(p security.Principal) error {
			_, err := f.svc.UpdateRoute(ctx, p, f.ref, "octoroute_1", authInput(""))
			return err
		},
		"remove": func(p security.Principal) error {
			_, err := f.svc.RemoveRoute(ctx, p, f.ref, "octoroute_1", 1)
			return err
		},
		"farp": func(p security.Principal) error {
			_, err := f.svc.SetFarpHostnames(ctx, p, f.ref, []string{"api.acme.dev"}, 3)
			return err
		},
		"sync": func(p security.Principal) error { _, err := f.svc.Sync(ctx, p, f.ref); return err },
	} {
		for _, p := range []security.Principal{developer, readOnly} {
			if err := write(p); !errors.Is(err, security.ErrForbidden) {
				t.Fatalf("%s as %+v: %v", name, p, err)
			}
		}
	}
	if f.syncer.count() != 0 {
		t.Fatalf("%d syncs", f.syncer.count())
	}
}

func TestCreateRouteRejectsUnattachedHostname(t *testing.T) {
	f := newServiceFixture(t)
	_, err := f.svc.CreateRoute(context.Background(), admin, f.ref, authInput("docs.acme.dev"))
	if !errors.Is(err, ErrInvalid) || err.Error() != "Attach docs.acme.dev to edge first." {
		t.Fatalf("docs.acme.dev: %v", err)
	}
	if !slices.Equal(f.hosts.services, []string{"edge--gateway"}) {
		t.Fatalf("hostnames read for %v", f.hosts.services)
	}
	res, err := f.svc.CreateRoute(context.Background(), admin, f.ref, authInput(""))
	if err != nil || res.Route.Hostname != "" || res.Route.CreatedBy != "admin" || res.Sync.State != SyncApplied {
		t.Fatalf("any host: %+v %v", res, err)
	}
	if _, err = f.svc.CreateRoute(context.Background(), admin, f.ref, authInput("API.acme.dev")); err != nil {
		t.Fatalf("api.acme.dev: %v", err)
	}
	if got := f.store.stored(); len(got) != 2 || got[1].Hostname != "api.acme.dev" {
		t.Fatalf("stored %+v", got)
	}
}

func TestCreateRouteRejectsBadTargets(t *testing.T) {
	f := newServiceFixture(t)
	for _, c := range []struct {
		in   RouteInput
		want string
	}{
		{RouteInput{Path: "/x", Service: "nope", Port: 8080}, "nope isn't a service in this environment."},
		{RouteInput{Path: "/x", Service: "auth", Port: 9999}, "auth listens on 9000."},
		{RouteInput{Path: "/x", Service: "jobs", Port: 8080}, "jobs runs on a different server or cluster, so Octopus can't reach it."},
	} {
		if _, err := f.svc.CreateRoute(context.Background(), admin, f.ref, c.in); !errors.Is(err, ErrInvalid) || err.Error() != c.want {
			t.Fatalf("%+v: %v, want %q", c.in, err, c.want)
		}
	}
	// The brief's wording, with auth itself on another server.
	f.bindings[1] = serviceBinding(t, "auth", "conn_b", planning.ServiceConfig{Port: 9000})
	if _, err := f.svc.CreateRoute(context.Background(), admin, f.ref, authInput("")); err == nil || err.Error() != "auth runs on a different server or cluster, so Octopus can't reach it." {
		t.Fatalf("auth on conn_b: %v", err)
	}
	if len(f.store.stored()) != 0 {
		t.Fatalf("stored %+v", f.store.stored())
	}
	// Conflicts and bad input come from the model's own checks.
	f.bindings[1] = serviceBinding(t, "auth", "conn_a", planning.ServiceConfig{Port: 9000})
	if _, err := f.svc.CreateRoute(context.Background(), admin, f.ref, authInput("")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateRoute(context.Background(), admin, f.ref, authInput("")); !errors.Is(err, ErrConflict) {
		t.Fatalf("second /auth/*: %v", err)
	}
	if _, err := f.svc.CreateRoute(context.Background(), admin, f.ref, RouteInput{Path: "auth", Service: "auth", Port: 9000}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("path auth: %v", err)
	}
}

func TestCreateRouteWhileOctopusDownSavesAndReportsPending(t *testing.T) {
	f := newServiceFixture(t)
	down := &Syncer{
		Store: f.store, Hostnames: f.hosts, Now: time.Now,
		Bindings: func(context.Context, string, string) ([]*planning.Binding, error) { return f.bindings, nil },
		Open: func(context.Context, GatewayRef) (*deployments.ComponentChannel, error) {
			return nil, errors.New("dial lp-inst-01h455vb4pex5vsknk084sn02q:9090: connection refused")
		},
		Secret: func(context.Context, GatewayRef, string) ([]byte, error) { return []byte(fakeAdminToken), nil },
	}
	f.svc.d.Syncer = down
	res, err := f.svc.CreateRoute(context.Background(), admin, f.ref, authInput("api.acme.dev"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Route == nil || res.Route.ID == "" || len(f.store.stored()) != 1 {
		t.Fatalf("route %+v, stored %+v", res.Route, f.store.stored())
	}
	if res.Sync.State != "pending" || res.Sync.Message != "Octopus isn't answering on its private port. Loomplane keeps the route and pushes it within 30 seconds." {
		t.Fatalf("sync %+v", res.Sync)
	}
	if strings.Contains(fmt.Sprint(res), fakeAdminToken) {
		t.Fatalf("the result carries the admin token: %+v", res)
	}
}

func TestWritesRetryABusySyncThenReportPending(t *testing.T) {
	f := newServiceFixture(t)
	f.syncer.results = []func() (SyncResult, error){busy, busy, applied}
	res, err := f.svc.Sync(context.Background(), admin, f.ref)
	if err != nil || res.State != SyncApplied || f.syncer.count() != 3 {
		t.Fatalf("sync %+v %v after %d tries", res, err, f.syncer.count())
	}
	f.syncer.calls, f.syncer.results = 0, []func() (SyncResult, error){busy}
	res, err = f.svc.Sync(context.Background(), admin, f.ref)
	if err != nil || res.State != SyncPending || res.Message != msgBusy || f.syncer.count() < 2 {
		t.Fatalf("sync %+v %v after %d tries", res, err, f.syncer.count())
	}
}

func TestUpdateRouteRejectsStaleRevision(t *testing.T) {
	f := newServiceFixture(t)
	created, err := f.svc.CreateRoute(context.Background(), admin, f.ref, authInput("api.acme.dev"))
	if err != nil {
		t.Fatal(err)
	}
	in := authInput("api.acme.dev")
	in.Note, in.Revision = "Sign-in", created.Route.Revision+1
	if _, err = f.svc.UpdateRoute(context.Background(), admin, f.ref, created.Route.ID, in); !errors.Is(err, ErrStale) {
		t.Fatalf("stale update: %v", err)
	}
	in.Revision = created.Route.Revision
	res, err := f.svc.UpdateRoute(context.Background(), admin, f.ref, created.Route.ID, in)
	if err != nil || res.Route.Note != "Sign-in" || res.Route.Revision != 2 || res.Sync.State != SyncApplied {
		t.Fatalf("update %+v %v", res, err)
	}
	if _, err = f.svc.UpdateRoute(context.Background(), admin, f.ref, "octoroute_missing", in); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing route: %v", err)
	}
	if _, err = f.svc.RemoveRoute(context.Background(), admin, f.ref, created.Route.ID, 1); !errors.Is(err, ErrStale) {
		t.Fatalf("stale remove: %v", err)
	}
	if sync, err := f.svc.RemoveRoute(context.Background(), admin, f.ref, created.Route.ID, 2); err != nil || sync.State != SyncApplied || len(f.store.stored()) != 0 {
		t.Fatalf("remove %+v %v", sync, err)
	}
}

func TestUpdateRouteKeepsADetachedHostname(t *testing.T) {
	f := newServiceFixture(t)
	f.store.routes = []*Route{{ID: "octoroute_old", GatewayRef: f.ref, Hostname: "old.acme.dev", Path: "/", Methods: []string{}, Service: "auth", Port: 9000, Revision: 4}}
	in := RouteInput{Hostname: "old.acme.dev", Path: "/", Service: "auth", Port: 9000, Note: "Moving soon", Revision: 4}
	if _, err := f.svc.UpdateRoute(context.Background(), admin, f.ref, "octoroute_old", in); err != nil {
		t.Fatalf("note on a detached route: %v", err)
	}
	in.Hostname, in.Revision = "docs.acme.dev", 5
	if _, err := f.svc.UpdateRoute(context.Background(), admin, f.ref, "octoroute_old", in); err == nil || err.Error() != "Attach docs.acme.dev to edge first." {
		t.Fatalf("move to docs.acme.dev: %v", err)
	}
}

func TestSetFarpHostnamesOnlyAcceptsAttachedHostnames(t *testing.T) {
	f := newServiceFixture(t)
	if _, err := f.svc.SetFarpHostnames(context.Background(), admin, f.ref, []string{"api.acme.dev", "docs.acme.dev"}, 3); !errors.Is(err, ErrInvalid) || err.Error() != "Attach docs.acme.dev to edge first." {
		t.Fatalf("docs.acme.dev: %v", err)
	}
	if f.store.gateway.Generation != 2 || f.syncer.count() != 0 {
		t.Fatalf("generation %d, %d syncs", f.store.gateway.Generation, f.syncer.count())
	}
	st, err := f.svc.SetFarpHostnames(context.Background(), admin, f.ref, []string{" API.acme.dev", "api.acme.dev"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.store.gateway.FarpHostnames, []string{"api.acme.dev"}) || f.store.gateway.Generation != 3 || f.syncer.count() != 1 {
		t.Fatalf("stored %+v after %d syncs", f.store.gateway, f.syncer.count())
	}
	if !slices.Equal(st.FarpHostnames, []string{"api.acme.dev"}) || st.Revision != 4 || st.Generation != 3 {
		t.Fatalf("status %+v", st)
	}
	if _, err = f.svc.SetFarpHostnames(context.Background(), admin, f.ref, nil, 3); !errors.Is(err, ErrStale) {
		t.Fatalf("stale revision: %v", err)
	}
}

func TestBoardReadsOctopusAsThePrincipal(t *testing.T) {
	f := newServiceFixture(t)
	f.store.routes = []*Route{{ID: "octoroute_a", GatewayRef: f.ref, Hostname: "api.acme.dev", Path: "/auth/*", Methods: []string{}, Service: "auth", Port: 9000, Revision: 1}}
	viewer := security.Principal{OrgID: "org_x", UserID: "viewer"}
	b, err := f.svc.Board(context.Background(), viewer, f.ref)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Live || b.Gateway.Version != "0.5.0" || len(b.Hosts) != 1 || b.Hosts[0].Hostname != "api.acme.dev" || b.Hosts[0].Status == nil || len(b.Hosts[0].Routes) != 1 {
		t.Fatalf("board %+v", b)
	}
	want := []string{
		"secret viewer infrastructure:read proj_x/edge admin-api-token",
		"channel viewer infrastructure:read proj_x/edge/gateway:admin",
	}
	if !slices.Equal(f.access.calls, want) {
		t.Fatalf("access %v, want %v", f.access.calls, want)
	}
	if !slices.Equal(f.domains.calls, []string{"viewer proj_x route_1"}) {
		t.Fatalf("route statuses %v", f.domains.calls)
	}
	if f.syncer.count() != 0 {
		t.Fatalf("a read synced %d times", f.syncer.count())
	}
	if writes := f.octo.writes(); len(writes) != 0 {
		t.Fatalf("a read wrote %v", writes)
	}

	f.access.down = true
	b, err = f.svc.Board(context.Background(), viewer, f.ref)
	if err != nil || b.Live || b.LiveError == "" || len(b.Hosts[0].Routes) != 1 {
		t.Fatalf("board with Octopus down %+v %v", b, err)
	}
}

func TestReadsNeedInfrastructureRead(t *testing.T) {
	f := newServiceFixture(t)
	outsider := security.Principal{OrgID: "org_x", UserID: "outsider"}
	ctx := context.Background()
	if _, err := f.svc.Board(ctx, outsider, f.ref); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("board: %v", err)
	}
	if _, err := f.svc.Status(ctx, outsider, f.ref); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("status: %v", err)
	}
	if _, err := f.svc.Discovery(ctx, outsider, f.ref); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("discovery: %v", err)
	}
	if len(f.access.calls) != 0 {
		t.Fatalf("access %v", f.access.calls)
	}
	// A read-only key reads.
	if _, err := f.svc.Board(ctx, readOnly, f.ref); err != nil {
		t.Fatalf("read-only board: %v", err)
	}
}

func TestReadsOfAnotherProjectsGatewayAreNotFound(t *testing.T) {
	f := newServiceFixture(t)
	other := f.ref
	other.ProjectID = "proj_other"
	if _, err := f.svc.Board(context.Background(), admin, other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("board: %v", err)
	}
	if _, err := f.svc.CreateRoute(context.Background(), admin, other, authInput("")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("create: %v", err)
	}
	if len(f.store.stored()) != 0 {
		t.Fatalf("stored %+v", f.store.stored())
	}
}

func TestStatusReportsTheGatewayAndWhereToAttachIt(t *testing.T) {
	f := newServiceFixture(t)
	f.store.gateway.FarpHostnames = []string{"api.acme.dev"}
	f.store.gateway.State = SyncState{StartTime: "2026-10-21 09:00:00", SyncedAt: time.Date(2026, 10, 21, 9, 1, 0, 0, time.UTC), Error: msgUnavailable}
	st, err := f.svc.Status(context.Background(), readOnly, f.ref)
	if err != nil {
		t.Fatal(err)
	}
	want := AttachDefaults{CloudID: "cloud_1", EnvironmentID: "env_x", Service: "edge--gateway", Port: 8080, BlockedPathPrefixes: []string{"/__farp", "/_farp", "/admin"}}
	if st.Resource != "edge" || st.Provider != ProviderServer || st.PrivateURL != "http://"+testContainer+":8080" || st.Version != "0.5.0" ||
		st.StartTime != "2026-10-21 10:00:00" || st.SyncError != msgUnavailable || !st.SyncedAt.Equal(f.store.gateway.State.SyncedAt) ||
		st.Generation != 2 || st.AppliedGeneration != 2 || st.Revision != 3 || !slices.Equal(st.FarpHostnames, []string{"api.acme.dev"}) ||
		fmt.Sprint(st.Attach) != fmt.Sprint(want) {
		t.Fatalf("status %+v", st)
	}
	// Before the gateway's binding is written there's no address yet.
	f.bindings = f.bindings[1:]
	if st, err = f.svc.Status(context.Background(), readOnly, f.ref); err != nil || st.PrivateURL != "" || st.Attach.CloudID != "" {
		t.Fatalf("status without a binding %+v %v", st, err)
	}
}
