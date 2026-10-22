package planning

import (
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

// unpinnedStore holds one plan and records how it was settled. Every
// other Store method panics through the nil embedded interface, which
// proves compute refuses before it reads anything else.
type unpinnedStore struct {
	Store
	plan     *Plan
	failed   string
	settled  string
	decision string
}

func (s *unpinnedStore) GetPlan(context.Context, string, string) (*Plan, error) {
	p := *s.plan
	return &p, nil
}

func (s *unpinnedStore) FailPlanning(_ context.Context, _, _, reason string, _ time.Time) error {
	s.failed = reason
	return nil
}

func (s *unpinnedStore) Decide(_ context.Context, _, _, status, _, note string, _ time.Time) (bool, error) {
	s.settled, s.decision = status, note
	return true, nil
}

// A plan from before plans pinned repositories, which the backfill
// couldn't give one, must not plan from an empty graph.
func TestPlan_refusesAPlanThatPinsNoRepositories(t *testing.T) {
	ctx := context.Background()

	store := &unpinnedStore{plan: &Plan{ID: "plan_1", OrgID: "org_1", Status: StatusPlanning, Sources: []PlanSource{}}}
	svc := NewService(Deps{Store: store})
	status, err := svc.Plan(ctx, "org_1", "plan_1")
	if err != nil || status != StatusFailed || !strings.Contains(store.failed, "this plan pins no repositories") {
		t.Fatalf("plan: %q, %v, failed with %q", status, err, store.failed)
	}

	store = &unpinnedStore{plan: &Plan{ID: "plan_1", OrgID: "org_1", Status: StatusReady, Revision: "r1", Sources: []PlanSource{}}}
	svc = NewService(Deps{Store: store})
	status, err = svc.ApplyDecision(ctx, "org_1", "plan_1", orchestration.Decision{Approve: true, Revision: "r1", By: "usr_1"})
	if err != nil || status != StatusStale || store.settled != StatusStale || !strings.Contains(store.decision, "this plan pins no repositories") {
		t.Fatalf("approve: %q, %v, settled %q with %q", status, err, store.settled, store.decision)
	}
}

// serverClouds are two ready, checked servers.
type serverClouds struct{}

func (serverClouds) All(context.Context, string) ([]*connections.Connection, error) {
	out := make([]*connections.Connection, 0, 2)
	for _, id := range []string{targetA, targetB} {
		out = append(out, &connections.Connection{ID: id, OrgID: "org", Name: id, Cloud: connections.Server, Status: connections.StatusReady,
			Details: map[string]string{"host": id + ".example.com", "user": "deploy", "docker_socket": "/var/run/docker.sock", "architecture": "arm64"}})
	}
	return out, nil
}

// fakeEndpoints knows prodPG and checks it through whatever dial it's
// given, recording which target each check went through.
type fakeEndpoints struct {
	mu     sync.Mutex
	checks []string
	// down is a target the endpoint isn't reachable from.
	down string
}

func (f *fakeEndpoints) Endpoint(_ context.Context, orgID, id string) (*catalogconnect.Endpoint, error) {
	if orgID != "org" || id != prodPG {
		return nil, catalogconnect.ErrNotFound
	}
	return &catalogconnect.Endpoint{ID: prodPG, OrgID: "org", Name: "prod-pg", Engine: "postgres", Host: "db.example", Port: 5432}, nil
}

func (f *fakeEndpoints) Check(ctx context.Context, orgID, id string, req recipes.Requirement, dial catalogconnect.DialFunc) (*catalogconnect.Report, error) {
	if orgID != "org" || id != prodPG {
		return nil, catalogconnect.ErrNotFound
	}
	_, err := dial(ctx, "tcp", "db.example:5432")
	through := strings.TrimPrefix(err.Error(), "through ")
	f.mu.Lock()
	f.checks = append(f.checks, id+" "+req.Engine+" "+through)
	f.mu.Unlock()
	if through == f.down {
		return &catalogconnect.Report{Problems: []string{"db.example:5432 isn't reachable from the target: i/o timeout"}}, nil
	}
	return &catalogconnect.Report{Version: "16.4"}, nil
}

// fakeDialer hands out a dial that names the target it's for.
type fakeDialer struct {
	asked  []string
	closed int
}

func (d *fakeDialer) TargetDial(_ context.Context, orgID, connectionID string) (catalogconnect.DialFunc, io.Closer, error) {
	if orgID != "org" {
		return nil, nil, errors.New("wrong organization")
	}
	d.asked = append(d.asked, connectionID)
	dial := func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("through " + connectionID)
	}
	return dial, closerFunc(func() error { d.closed++; return nil }), nil
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

func TestCreatePlanChecksEndpointsThroughTheTarget(t *testing.T) {
	doc := "version: 1\nresources:\n" +
		"  near: {kind: sample, provider: server, target: " + targetA + ", uses: {postgres: {external: " + prodPG + "}}}\n" +
		"  twin: {kind: sample, provider: server, target: " + targetA + ", uses: {postgres: {external: " + prodPG + "}}}\n" +
		"  far: {kind: sample, provider: server, target: " + targetB + ", uses: {postgres: {external: " + prodPG + "}}}\n" +
		"  lost: {kind: sample, provider: server, target: " + targetA + ", uses: {postgres: {external: " + otherPG + "}}}\n" +
		"  own: {kind: sample, provider: server, target: " + targetB + "}\n" +
		"  db: {kind: postgres, provider: server, target: " + targetA + "}\n"
	endpoints, dialer := &fakeEndpoints{down: targetB}, &fakeDialer{}
	service := NewService(Deps{
		Store: catalogStore{}, Manifests: catalogManifest{revision: &manifests.Revision{Manifest: parse(t, doc)}}, Providers: providers.Registry(),
		Connections: serverClouds{}, Kinds: recipestest.Registry(t), Endpoints: endpoints, Dialer: dialer,
	})
	plan := &Plan{Catalog: true, OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production"}

	req := *recipestest.Sample().Dependencies[0].External
	checks := service.checkEndpoints(t.Context(), plan.OrgID, plan.Environment, parse(t, doc))
	// Targets are dialed in ID order.
	if !slices.Equal(dialer.asked, []string{targetB, targetA}) || dialer.closed != 2 {
		t.Fatalf("dialer asked for %v, closed %d", dialer.asked, dialer.closed)
	}
	if !slices.Equal(endpoints.checks, []string{prodPG + " postgres " + targetB, prodPG + " postgres " + targetA}) {
		t.Fatalf("checks: %v", endpoints.checks)
	}
	want := map[string]EndpointCheck{
		endpointKey(prodPG, targetA, req): {ID: prodPG, Engine: "postgres", Found: true, Report: &catalogconnect.Report{Version: "16.4"}},
		endpointKey(prodPG, targetB, req): {ID: prodPG, Engine: "postgres", Found: true, Report: &catalogconnect.Report{Problems: []string{"db.example:5432 isn't reachable from the target: i/o timeout"}}},
		otherPG:                           {ID: otherPG},
	}
	if !reflect.DeepEqual(checks, want) {
		t.Fatalf("checks: %+v", checks)
	}

	result, _, err := service.compute(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	unavailable := make([]string, 0, len(result.Problems))
	for _, p := range result.Problems {
		if p.Code != "catalog-endpoint-unavailable" {
			t.Fatalf("unexpected problem: %+v", p)
		}
		unavailable = append(unavailable, p.Name)
	}
	slices.Sort(unavailable)
	if !slices.Equal(unavailable, []string{"far", "lost"}) {
		t.Fatalf("unavailable: %v", unavailable)
	}

	// Without a checker nothing is dialed, and no endpoint can be used.
	dialer = &fakeDialer{}
	service.endpoints, service.dialer = nil, dialer
	result, _, err = service.compute(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(dialer.asked) != 0 || result.Executable || len(result.Problems) != 4 {
		t.Fatalf("unchecked: dialed %v, problems %+v", dialer.asked, result.Problems)
	}
}

// An approval plans again, so it checks the endpoints again: one that
// stopped answering since makes the plan stale.
func TestApprovalChecksEndpointsAgain(t *testing.T) {
	doc := "version: 1\nresources:\n  app: {kind: sample, provider: server, target: " + targetA + ", uses: {postgres: {external: " + prodPG + "}}}\n"
	endpoints := &fakeEndpoints{}
	store := &approvalStore{}
	service := NewService(Deps{
		Store: store, Manifests: catalogManifest{revision: &manifests.Revision{Manifest: parse(t, doc)}}, Providers: providers.Registry(),
		Connections: serverClouds{}, Kinds: recipestest.Registry(t), Endpoints: endpoints, Dialer: &fakeDialer{},
	})
	plan := &Plan{ID: "plan_1", Catalog: true, OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production", Status: StatusReady}
	result, revision, err := service.compute(t.Context(), plan)
	if err != nil || !result.Executable {
		t.Fatalf("plan: %+v, %v", result, err)
	}
	plan.Revision = revision
	store.plan = plan

	status, err := service.ApplyDecision(t.Context(), "org", "plan_1", orchestration.Decision{Approve: true, Revision: revision, By: "usr_1"})
	if err != nil || status != StatusApproved || len(endpoints.checks) != 2 {
		t.Fatalf("approve: %q, %v, checks %v", status, err, endpoints.checks)
	}
	endpoints.down = targetA
	status, err = service.ApplyDecision(t.Context(), "org", "plan_1", orchestration.Decision{Approve: true, Revision: revision, By: "usr_1"})
	if err != nil || status != StatusStale || len(endpoints.checks) != 3 {
		t.Fatalf("approve after the endpoint went away: %q, %v, checks %v", status, err, endpoints.checks)
	}
}

// approvalStore holds one ready catalog plan and records its decision.
type approvalStore struct {
	catalogStore
	plan    *Plan
	settled string
}

func (s *approvalStore) GetPlan(context.Context, string, string) (*Plan, error) {
	p := *s.plan
	return &p, nil
}

func (s *approvalStore) Decide(_ context.Context, _, _, status, _, _ string, _ time.Time) (bool, error) {
	s.settled = status
	return true, nil
}
