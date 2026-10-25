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
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/graph"
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
	// consumers are the titles each check was made for.
	consumers []string
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
	f.consumers = append(f.consumers, catalogconnect.ConsumerFrom(ctx).Title)
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

	req, consumer := *recipestest.Sample().Dependencies[0].External, catalogconnect.ConsumerOf(recipestest.Sample(), "postgres")
	checks := service.checkEndpoints(t.Context(), plan.OrgID, plan.Environment, parse(t, doc))
	// Targets are dialed in ID order.
	if !slices.Equal(dialer.asked, []string{targetB, targetA}) || dialer.closed != 2 {
		t.Fatalf("dialer asked for %v, closed %d", dialer.asked, dialer.closed)
	}
	if !slices.Equal(endpoints.checks, []string{prodPG + " postgres " + targetB, prodPG + " postgres " + targetA}) {
		t.Fatalf("checks: %v", endpoints.checks)
	}
	// Each check is made for the kind that connects the endpoint.
	if !slices.Equal(endpoints.consumers, []string{"Sample", "Sample"}) {
		t.Fatalf("consumers: %v", endpoints.consumers)
	}
	want := map[string]EndpointCheck{
		endpointKey(prodPG, targetA, req, consumer): {ID: prodPG, Name: "prod-pg", Engine: "postgres", Found: true, Report: &catalogconnect.Report{Version: "16.4"}},
		endpointKey(prodPG, targetB, req, consumer): {ID: prodPG, Name: "prod-pg", Engine: "postgres", Found: true, Report: &catalogconnect.Report{Problems: []string{"db.example:5432 isn't reachable from the target: i/o timeout"}}},
		otherPG: {ID: otherPG},
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
		// A known endpoint is named by its name, an unknown one by its ID.
		if want := map[string]string{"far": "endpoint prod-pg ", "lost": "endpoint " + otherPG + " "}[p.Name]; !strings.HasPrefix(p.Message, want) {
			t.Fatalf("%s: %q doesn't start with %q", p.Name, p.Message, want)
		}
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

// edgeDetails are a checked server's, with the Docker daemon that
// verifies its account.
func edgeDetails(id string) map[string]string {
	return map[string]string{"host": id + ".example.com", "user": "deploy", "docker_socket": "/var/run/docker.sock", "architecture": "arm64", "docker_id": "daemon-" + id}
}

// edgeClouds are two ready, checked and verified servers.
type edgeClouds struct{}

func (edgeClouds) All(context.Context, string) ([]*connections.Connection, error) {
	out := make([]*connections.Connection, 0, 2)
	for _, id := range []string{targetA, targetB} {
		out = append(out, &connections.Connection{ID: id, OrgID: "org", Name: id, Cloud: connections.Server, Status: connections.StatusReady, Details: edgeDetails(id)})
	}
	return out, nil
}

// failingResolver can't read the environment's assignment.
type failingResolver struct{}

func (failingResolver) Resolve(context.Context, string, string, string) (*cloudprofiles.Assignment, error) {
	return nil, errors.New("cloud assignments are unavailable")
}

// edgeService plans doc as a catalog plan in an environment assigned
// cloud_1, whose one target is reached through targets.
func edgeService(t *testing.T, doc string, targets ...cloudprofiles.Target) (*Service, *Plan, *targetResolver) {
	t.Helper()
	resolver := &targetResolver{assignment: &cloudprofiles.Assignment{OrgID: "org", ProjectID: "project", EnvironmentID: "env", CloudID: "cloud_1", CloudRevision: 2, Revision: 3, GrantRevision: 4, Targets: targets}}
	service := NewService(Deps{
		Store: catalogStore{}, Manifests: catalogManifest{revision: &manifests.Revision{Manifest: parse(t, doc)}}, Providers: providers.Registry(),
		Connections: edgeClouds{}, Kinds: recipestest.Registry(t), CloudProfiles: resolver,
	})
	plan := &Plan{Catalog: true, OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production"}
	return service, plan, resolver
}

func TestCreatePlanPlacesComposedResourceOnTheMatchingCloudTarget(t *testing.T) {
	doc := "version: 1\nresources:\n  edge: {kind: sample, provider: server, target: " + targetA + "}\n"
	edgeHost := cloudprofiles.Target{ID: "t1", OrgID: "org", CloudID: "cloud_1", Name: "edge-host", Provider: "server", ConnectionID: targetA, Runtime: "server", Revision: 5}
	service, plan, resolver := edgeService(t, doc, edgeHost)
	result, _, err := service.compute(t.Context(), plan)
	if err != nil || !result.Executable {
		t.Fatalf("plan: %+v, %v", result, err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolved the assignment %d times", resolver.calls)
	}
	want := CloudPlacement{GrantRevision: 4, CloudID: "cloud_1", CloudRevision: 2, AssignmentRevision: 3, TargetID: "t1", TargetRevision: 5, TargetName: "edge-host", Provider: "server", ConnectionID: targetA,
		AccountIdentity: connections.AccountIdentity(connections.Server, "", edgeDetails(targetA)), RegionSource: "default"}
	if want.AccountIdentity == "" {
		t.Fatal("fixture account isn't verified")
	}
	for _, id := range []string{"resource:edge", "resource:edge--api"} {
		a := findAction(result, id)
		if a == nil || a.Resource.CloudPlacement == nil || *a.Resource.CloudPlacement != want {
			t.Fatalf("%s placement: %+v", id, a)
		}
	}
	if hasCode(result.Warnings, "catalog-cloud-placement-missing") {
		t.Fatalf("placed resource warned: %+v", result.Warnings)
	}

	missing := func(t *testing.T, result *Result) {
		t.Helper()
		for _, a := range result.Actions {
			if a.Resource != nil && a.Resource.CloudPlacement != nil {
				t.Fatalf("%s placed: %+v", a.ID, a.Resource.CloudPlacement)
			}
		}
		w := findCode(result.Warnings, "catalog-cloud-placement-missing")
		if w == nil || w.Name != "edge" || w.Message != "edge isn't on a target of this environment's Cloud, so it can't take domain routes." {
			t.Fatalf("missing placement warning: %+v", result.Warnings)
		}
	}
	other := edgeHost
	other.ConnectionID = targetB
	pooled := edgeHost
	pooled.PoolID = "pool_1"
	unreviewed := edgeHost
	unreviewed.Revision = 0
	for name, targets := range map[string][]cloudprofiles.Target{"no matching target": {other}, "a pool target": {pooled}, "no targets": nil, "an unreviewed target": {unreviewed}} {
		t.Run(name, func(t *testing.T) {
			s, p, _ := edgeService(t, doc, targets...)
			r, _, planErr := s.compute(t.Context(), p)
			if planErr != nil {
				t.Fatal(planErr)
			}
			missing(t, r)
		})
	}
	t.Run("no assigned Cloud", func(t *testing.T) {
		s, p, unassigned := edgeService(t, doc)
		unassigned.assignment = nil
		r, _, planErr := s.compute(t.Context(), p)
		if planErr != nil || !r.Executable {
			t.Fatalf("plan: %+v, %v", r, planErr)
		}
		missing(t, r)
	})

	// A plan without a composed resource doesn't look the assignment up.
	service, plan, resolver = edgeService(t, "version: 1\nresources:\n  db: {kind: postgres, provider: server, target: "+targetA+"}\n", edgeHost)
	result, _, err = service.compute(t.Context(), plan)
	if err != nil || !result.Executable || resolver.calls != 0 {
		t.Fatalf("engine plan: %+v, %v, %d lookups", result, err, resolver.calls)
	}
}

// A failed lookup only composed resources asked for is reported on the
// composed resource, not on services.
func TestCreatePlanReportsACloudLookupFailureOnTheComposedResource(t *testing.T) {
	doc := "version: 1\nresources:\n  edge: {kind: sample, provider: server, target: " + targetA + "}\n"
	service, plan, _ := edgeService(t, doc)
	service.cloudProfiles = failingResolver{}
	result, _, err := service.compute(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Executable || len(result.Problems) != 1 {
		t.Fatalf("lookup failure: %+v", result)
	}
	if f := result.Problems[0]; f.Code != "cloud-target" || f.Subject != ResourceSubject || f.Name != "edge" || f.Path != "resources.edge.target" || f.Message != "cloud assignments are unavailable" {
		t.Fatalf("finding: %+v", f)
	}
}

// polyglotAnalyses reads examples/polyglot's graph for every analysis.
type polyglotAnalyses struct {
	Analyses
	graph *graph.Graph
}

func (a polyglotAnalyses) Graph(context.Context, string, string) (*graph.Graph, error) {
	return a.graph, nil
}

// rampingStore is an environment where a canary may be ramping. It holds
// one plan and records how it was finished and settled.
type rampingStore struct {
	approvalStore
	ramping  string
	err      error
	note     string
	asked    []string
	finished *Plan
}

func (s *rampingStore) RampingCanary(_ context.Context, orgID, environmentID string) (string, error) {
	s.asked = append(s.asked, orgID+"/"+environmentID)
	return s.ramping, s.err
}

func (s *rampingStore) Decide(_ context.Context, _, _, status, _, note string, _ time.Time) (bool, error) {
	s.settled, s.note = status, note
	return true, nil
}

func (s *rampingStore) FinishPlanning(_ context.Context, p *Plan) (bool, error) {
	cp := *p
	s.finished = &cp
	return true, nil
}

// releaseService plans acme, with a release block for production, from
// examples/polyglot.
func releaseService(t *testing.T, store Store, release string) (*Service, *Plan) {
	t.Helper()
	doc := acme + "environments:\n  production:\n    release:\n" + release
	service := NewService(Deps{
		Store: store, Analyses: polyglotAnalyses{graph: polyglot(t)},
		Manifests: catalogManifest{revision: &manifests.Revision{Manifest: parse(t, doc)}}, Providers: providers.Registry(),
	})
	plan := &Plan{ID: "plan_1", OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production",
		Sources: []PlanSource{{Alias: "app", AnalysisID: "analysis", CommitSHA: "abc", InScope: true}}}
	return service, plan
}

// The release is part of what's reviewed: a longer hold is another
// revision, so approving one never approves the other.
func TestCanaryIsPartOfTheRevision(t *testing.T) {
	revisions := map[int64]string{}
	for _, hold := range []string{"10m", "15m"} {
		service, plan := releaseService(t, catalogStore{}, "      strategy: canary\n      hold: "+hold+"\n")
		result, revision, err := service.compute(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}
		if result.Release == nil || result.Release.Strategy != "canary" {
			t.Fatalf("hold %s: release %+v", hold, result.Release)
		}
		revisions[result.Release.HoldSeconds] = revision
	}
	if len(revisions) != 2 || revisions[600] == "" || revisions[900] == "" || revisions[600] == revisions[900] {
		t.Fatalf("revisions by hold: %v", revisions)
	}
}

// While a canary ramps in an environment, a new plan for it is blocked, and
// an older ready plan approved now settles stale: two deployments never
// fight over one service's traffic.
func TestPlanIsBlockedWhileACanaryRamps(t *testing.T) {
	store := &rampingStore{}
	service, plan := releaseService(t, store, "      strategy: canary\n")
	before, revision, err := service.compute(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if hasCode(before.Problems, "canary-in-progress") {
		t.Fatalf("blocked with nothing ramping: %+v", before.Problems)
	}
	ready := *plan
	ready.Status, ready.Revision = StatusReady, revision
	store.plan = &ready

	store.ramping = "dep_1"
	blocked, _, err := service.compute(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	f := findCode(blocked.Problems, "canary-in-progress")
	if f == nil || blocked.Executable {
		t.Fatalf("executable %v, problems %+v", blocked.Executable, blocked.Problems)
	}
	if f.Message != "A canary release is ramping in production (deployment dep_1). Plan again once it finishes." {
		t.Fatalf("message: %q", f.Message)
	}
	if store.asked[len(store.asked)-1] != "org/env" {
		t.Fatalf("asked about %v", store.asked)
	}

	status, err := service.ApplyDecision(t.Context(), "org", "plan_1", orchestration.Decision{Approve: true, Revision: revision, By: "usr_1"})
	if err != nil || status != StatusStale || store.settled != StatusStale || store.note != f.Message {
		t.Fatalf("approve during the ramp: %q, %v, settled %q with %q", status, err, store.settled, store.note)
	}

	planning := *plan
	planning.Status = StatusPlanning
	store.plan = &planning
	status, err = service.Plan(t.Context(), "org", "plan_1")
	if err != nil || status != StatusBlocked || store.finished == nil || store.finished.Executable || !hasCode(store.finished.Problems, "canary-in-progress") {
		t.Fatalf("plan during the ramp: %q, %v, finished %+v", status, err, store.finished)
	}

	// Planning can't tell whether a canary ramps, so it retries rather than
	// plan past one.
	store.err = errors.New("database unavailable")
	if _, _, err = service.compute(t.Context(), plan); !errors.Is(err, store.err) {
		t.Fatalf("compute with the store down: %v", err)
	}
}
