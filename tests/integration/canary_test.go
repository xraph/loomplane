//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/dispatch"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/engine"
	dispatchpg "github.com/xraph/dispatch/store/postgres"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/security"
)

// canaryRepository is the repository every canary fixture plans: one Go
// HTTP service.
var canaryRepository = fstest.MapFS{
	"services/api/go.mod":  {Data: []byte("module example.com/acme/api\n\ngo 1.26\n")},
	"services/api/main.go": {Data: []byte("package main\n\nimport \"net/http\"\n\nfunc main() { _ = http.ListenAndServe(\":8080\", nil) }\n")},
}

// canaryManifest deploys api to Cloud Run, and releases production as a
// canary at 50% then 100%, holding each step a minute.
const canaryManifest = `version: 1
services:
  api:
    source: go:services/api
    provider: cloud-run
    port: 8080
environments:
  production:
    release:
      strategy: canary
      steps: [50, 100]
      hold: 1m
`

// canaryWeighted is Cloud Run as a canary sees it: one service, a
// revision serving it, and a candidate revision beside it while one ramps.
type canaryWeighted struct {
	mu sync.Mutex
	// serving is the revision taking the traffic.
	serving   string
	revisions int
	log       []string
	// revertErr and promoteErr fail every revert and promote while set.
	revertErr, promoteErr error
	// While hold is set, the first move to holdAt percent waits for it to
	// close, and closes entered once it's waiting.
	holdAt        int
	hold, entered chan struct{}
}

func (e *canaryWeighted) record(format string, args ...any) {
	e.log = append(e.log, fmt.Sprintf(format, args...))
}

func (e *canaryWeighted) Apply(_ context.Context, j *deployments.Job) (*deployments.Outcome, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.revisions++
	e.serving = fmt.Sprintf("api-%05d", e.revisions)
	image := "registry/api:" + j.Action.Service.CommitSHA
	e.record("apply %s %s", e.serving, image)
	return &deployments.Outcome{ExternalID: "projects/acme-prod/services/api", Image: image, Revision: image}, nil
}

func (e *canaryWeighted) ApplyCandidate(_ context.Context, j *deployments.Job) (*deployments.CanaryTarget, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.revisions++
	t := &deployments.CanaryTarget{Baseline: e.serving, Candidate: fmt.Sprintf("api-%05d", e.revisions), Image: "registry/api:" + j.Action.Service.CommitSHA, Refs: map[string]string{"service": "api"}}
	e.record("candidate %s %s", t.Candidate, t.Image)
	return t, nil
}

func (e *canaryWeighted) SetWeights(ctx context.Context, _ *deployments.Job, t deployments.CanaryTarget, percent int) error {
	e.mu.Lock()
	if e.hold != nil && percent == e.holdAt {
		hold := e.hold
		e.hold = nil
		close(e.entered)
		e.mu.Unlock()
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
		e.mu.Lock()
	}
	defer e.mu.Unlock()
	e.record("weigh %s %d", t.Candidate, percent)
	return nil
}

func (e *canaryWeighted) Promote(_ context.Context, _ *deployments.Job, t deployments.CanaryTarget) (*deployments.Outcome, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.promoteErr != nil {
		e.record("promote %s refused", t.Candidate)
		return nil, e.promoteErr
	}
	e.serving = t.Candidate
	e.record("promote %s", t.Candidate)
	return &deployments.Outcome{ExternalID: "projects/acme-prod/services/api", Image: t.Image, Revision: t.Image}, nil
}

func (e *canaryWeighted) Revert(_ context.Context, _ *deployments.Job, t deployments.CanaryTarget) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.revertErr != nil {
		e.record("revert %s refused", t.Candidate)
		return e.revertErr
	}
	e.record("revert %s", t.Candidate)
	return nil
}

func (*canaryWeighted) Remove(context.Context, *deployments.Job, *planning.Binding) error { return nil }

func (*canaryWeighted) Observe(context.Context, *connections.Connection, []byte, *planning.Binding) (*deployments.Observation, error) {
	return &deployments.Observation{Exists: true}, nil
}

func (e *canaryWeighted) entries() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.log...)
}

func (e *canaryWeighted) logged(entry string) bool {
	return slices.Contains(e.entries(), entry)
}

// holdMove makes the next move to percent wait until the returned release
// is called, and returns a channel closed once it waits.
func (e *canaryWeighted) holdMove(t *testing.T, percent int) (entered <-chan struct{}, release func()) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	hold := make(chan struct{})
	e.holdAt, e.hold, e.entered = percent, hold, make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(hold) }) }
	t.Cleanup(release)
	return e.entered, release
}

func (e *canaryWeighted) failReverts(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.revertErr = err
}

func (e *canaryWeighted) failPromotes(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.promoteErr = err
}

// canaryHealth is the cloud's monitoring: every revision serves 1000
// requests with one 5xx in a window, and while bad is set, a revision that
// isn't serving answers 80 of them with a 5xx.
type canaryHealth struct {
	mu    sync.Mutex
	exec  *canaryWeighted
	bad   bool
	reads []canaryRead
}

type canaryRead struct {
	binding, revision string
	window            releasehealth.Window
}

func (h *canaryHealth) Health(_ context.Context, _, bindingID, revision string, w releasehealth.Window) (releasehealth.Health, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reads = append(h.reads, canaryRead{binding: bindingID, revision: revision, window: w})
	h.exec.mu.Lock()
	serving := h.exec.serving
	h.exec.mu.Unlock()
	health := releasehealth.Health{Requests: 1000, Errors: 1, LatencyMS: 120, Statistic: "p95", Available: true}
	if h.bad && revision != serving {
		health.Errors = 80
	}
	return health, nil
}

func (h *canaryHealth) failCandidates(bad bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.bad = bad
}

func (h *canaryHealth) read() []canaryRead {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]canaryRead(nil), h.reads...)
}

// canarySources hands every job the same tarball.
type canarySources struct{}

func (canarySources) Store(context.Context, string, string, string, string) error { return nil }
func (canarySources) Open(context.Context, string) (io.ReadCloser, int64, error) {
	return io.NopCloser(strings.NewReader("tarball")), int64(len("tarball")), nil
}

// shortHolds is the runner with each canary hold cut to a millisecond. A
// manifest can't hold less than a minute, and the durable runtime refuses
// a timer that fires before its deadline, so the ramp is shortened where
// the workflow reads it. It zeroes the ramp's Lag too, so the wait for
// late metrics before each step's first judgement isn't exercised on
// Postgres: orchestration's TestCanaryFirstHoldWaitsForTheMetrics covers
// it. It keeps every ramp the runner read.
type shortHolds struct {
	*deployments.Runner
	mu    sync.Mutex
	ramps []orchestration.CanaryRamp
}

func (s *shortHolds) CanaryRamp(ctx context.Context, orgID, deploymentID string) (orchestration.CanaryRamp, error) {
	ramp, err := s.Runner.CanaryRamp(ctx, orgID, deploymentID)
	if err != nil || len(ramp.Steps) == 0 {
		return ramp, err
	}
	s.mu.Lock()
	s.ramps = append(s.ramps, ramp)
	s.mu.Unlock()
	ramp.Hold, ramp.Lag = time.Millisecond, 0
	return ramp, nil
}

func (s *shortHolds) read() []orchestration.CanaryRamp {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]orchestration.CanaryRamp(nil), s.ramps...)
}

// canaryFixture is Loomplane's planning, deployment runner and durable
// deployment workflow on a fresh Postgres, with Cloud Run and its
// monitoring faked. Its worker runs in the background, and the workflow
// reads each canary's ramp with its holds cut short (shortHolds).
type canaryFixture struct {
	sql       *pgx.Conn
	db        *pgdriver.PgDB
	plans     *planning.PgStore
	store     *deployments.PgStore
	service   *planning.Service
	runner    *deployments.Runner
	ramp      *shortHolds
	workflows *orchestration.Deployments
	exec      *canaryWeighted
	health    *canaryHealth
	actor     security.Principal
	commits   int
}

// newCanaryFixture plans project proj from canaryRepository and yaml. With
// yaml empty, the project has no source or manifest yet.
func newCanaryFixture(t *testing.T, yaml string) *canaryFixture {
	t.Helper()
	clouds, sql, db := cloudSyncDatabase(t)
	ctx := t.Context()
	now := time.Now().UTC()
	if err := connections.NewPgStore(db).CreateConnection(ctx, &connections.Connection{ID: "gcp", OrgID: "org", Cloud: "gcp", Name: "Prod", Status: connections.StatusReady, Account: "acme-prod", Details: map[string]string{"project_id": "acme-prod"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if yaml != "" {
		if _, err := sql.Exec(ctx, `INSERT INTO loomplane_project_sources(id,org_id,project_id,alias,repository,ref,created_by,created_at,updated_at) VALUES('canary-source','org','proj','app','acme/app','main','usr',now(),now())`); err != nil {
			t.Fatal(err)
		}
		if _, err := sql.Exec(ctx, `INSERT INTO loomplane_manifests(id,org_id,project_id,revision,body,digest,created_by,created_at) VALUES('canary-manifest','org','proj',1,$1,'canary-manifest','usr',now())`, yaml); err != nil {
			t.Fatal(err)
		}
	}
	if err := ops.Migrate(ctx, sql.Config().ConnString(), []*migrate.Group{dispatchpg.Migrations}); err != nil {
		t.Fatal(err)
	}
	gdb, err := grove.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	ds := dispatchpg.New(gdb)

	f := &canaryFixture{sql: sql, db: db, plans: planning.NewPgStore(db), store: deployments.NewPgStore(db), actor: security.Principal{OrgID: "org", UserID: "usr"}}
	f.exec = &canaryWeighted{}
	f.health = &canaryHealth{exec: f.exec}
	accounts := &syncAccounts{store: connections.NewPgStore(db)}
	f.workflows = orchestration.NewDeployments()
	f.service = planning.NewService(planning.Deps{
		Store: f.plans, Projects: projects.NewPgStore(db), Analyses: discovery.NewPgStore(db),
		Manifests: manifests.NewService(manifests.NewPgStore(db), nil, nil, nil), Providers: providers.Registry(),
		Connections: accounts, Authz: &syncAuth{}, Workflow: f.workflows, CloudProfiles: clouds, Window: time.Hour,
	})
	f.runner = deployments.NewRunner(deployments.RunnerDeps{
		Store: f.store, Plans: f.plans, CloudProfiles: clouds, Credentials: accounts, Variables: poolVars{}, Sources: canarySources{},
		Providers: providers.Registry(), Executors: map[string]deployments.Executor{"cloud-run": f.exec}, Health: f.health,
	})
	f.ramp = &shortHolds{Runner: f.runner}
	dispatcher, err := dispatch.New(dispatch.WithStore(ds))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Build(dispatcher, engine.WithDurableWorkflows(f.workflows.Options("canary-legacy", 1)))
	if err != nil {
		t.Fatal(err)
	}
	f.workflows.Bind(f.service, f.ramp, func() *engine.Engine { return eng })
	f.workflows.BindRunStore(orchestration.NewPgRunStore(db))
	// More than one task of a kind at once, so a revert can run while a
	// weigh the test holds is still in flight.
	options := f.workflows.CanaryOptions("canary", 4)
	options.PollInterval = 10 * time.Millisecond
	worker, err := drt.NewWorker(ds, options)
	if err != nil {
		t.Fatal(err)
	}
	f.workflows.BindWorker(orchestration.CurrentBuild, worker)
	f.run(t, worker)
	return f
}

// run works the durable tasks in the background, as the app's worker
// does.
func (f *canaryFixture) run(t *testing.T, worker *drt.Worker) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := worker.Run(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("worker: %v", err)
		}
	})
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
}

// release analyzes a new commit of the repository, which the next plan
// moves to, and returns it.
func (f *canaryFixture) release(t *testing.T) string {
	t.Helper()
	f.commits++
	commit := fmt.Sprintf("c%039d", f.commits)
	r, err := discovery.Analyze(t.Context(), canaryRepository)
	if err != nil {
		t.Fatal(err)
	}
	g, err := json.Marshal(graph.Build(r))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_analyses(id,org_id,project_id,source_id,alias,status,repository,ref,commit_sha,graph,created_by,created_at)
		VALUES($1,'org','proj','canary-source','app','succeeded','acme/app','main',$2,$3,'usr',now())`, fmt.Sprintf("analysis-%d", f.commits), commit, g); err != nil {
		t.Fatal(err)
	}
	return commit
}

// plan plans production and waits for planning to finish.
func (f *canaryFixture) plan(t *testing.T) *planning.Plan {
	t.Helper()
	p, err := f.service.Create(t.Context(), f.actor, "proj", planning.CreatePlanInput{Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	return f.waitPlan(t, p.ID, planning.StatusPlanning)
}

// waitPlan waits for a plan to leave status, and returns it.
func (f *canaryFixture) waitPlan(t *testing.T, id, status string) *planning.Plan {
	t.Helper()
	var p *planning.Plan
	eventually(t, "plan "+id+" leaves "+status, func() bool {
		var err error
		if p, err = f.plans.GetPlan(t.Context(), "org", id); err != nil {
			t.Fatal(err)
		}
		return p.Status != status
	})
	return p
}

// approve approves a ready plan and waits for its deployment to finish.
func (f *canaryFixture) approve(t *testing.T, p *planning.Plan) *deployments.Deployment {
	t.Helper()
	if _, err := f.service.Approve(t.Context(), f.actor, p.ID, planning.DecideInput{Revision: p.Revision}); err != nil {
		t.Fatal(err)
	}
	if decided := f.waitPlan(t, p.ID, planning.StatusReady); decided.Status != planning.StatusApproved {
		t.Fatalf("plan %s was approved and went %s: %q", p.ID, decided.Status, decided.DecisionNote)
	}
	return f.waitDeployment(t, p.ID, func(d *deployments.Deployment) bool { return d.Status != deployments.StatusRunning })
}

// waitDeployment waits until a plan's deployment is done, and returns it.
func (f *canaryFixture) waitDeployment(t *testing.T, planID string, done func(*deployments.Deployment) bool) *deployments.Deployment {
	t.Helper()
	var d *deployments.Deployment
	eventually(t, "the deployment of "+planID, func() bool {
		var err error
		d, err = f.store.ForPlan(t.Context(), "org", planID)
		if errors.Is(err, deployments.ErrNotFound) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		return done(d)
	})
	return d
}

// binding is what runs api in production.
func (f *canaryFixture) binding(t *testing.T) *planning.Binding {
	t.Helper()
	bs, err := f.plans.ListBindings(t.Context(), "org", "env")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bs {
		if b.Name == "api" {
			return b
		}
	}
	t.Fatal("api isn't bound")
	return nil
}

// deployment stores a running deployment of api in production with canary
// c, for an approved plan of its own.
func (f *canaryFixture) deployment(t *testing.T, id string, c *deployments.Canary) *deployments.Deployment {
	t.Helper()
	plan := "plan-" + id
	if _, err := f.sql.Exec(t.Context(), `INSERT INTO loomplane_plans(id,org_id,project_id,environment_id,environment,status,revision,manifest_id,manifest_revision,result,executable,created_by,created_at)
		VALUES($1,'org','proj','env','production','approved','review','canary-manifest',1,'{}',true,'usr',now())`, plan); err != nil {
		t.Fatal(err)
	}
	d, err := f.store.CreateDeployment(t.Context(), &deployments.Deployment{
		ID: id, OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Environment: "production", PlanID: plan,
		Status: deployments.StatusRunning, StartedAt: time.Now().UTC(), StartedBy: "usr", Canary: c,
		Steps: []deployments.Step{{ActionID: "service:api", Name: "api", Subject: "service", Op: "update", Provider: "cloud-run", Status: deployments.StepRamping}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// rampingCanary is a canary of api at 50%, its candidate up beside the
// baseline.
func rampingCanary(bindingID string) *deployments.Canary {
	return &deployments.Canary{
		Status: deployments.CanaryRamping, Steps: []int{50, 100}, HoldSeconds: 60, Thresholds: releasehealth.DefaultThresholds(), Step: 0, Weight: 50,
		Services: []deployments.CanaryService{{ActionID: "service:api", Name: "api", Provider: "cloud-run", BindingID: bindingID, Baseline: "api-00001", Candidate: "api-00002", Image: "registry/api:next", Refs: map[string]string{"service": "api"}}},
	}
}

// eventually polls done until it's true, for up to two minutes.
func eventually(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// canaryInProgress is the finding that blocks a plan while a canary ramps,
// or nil.
func canaryInProgress(fs []planning.Finding) *planning.Finding {
	for i := range fs {
		if fs[i].Code == "canary-in-progress" {
			return &fs[i]
		}
	}
	return nil
}

// A canary's state round-trips through the deployments table, holds its
// environment and the bindings it ramps while it's active, and lists
// among the environment's canaries newest first.
func TestCanaryStateRoundTripsThroughPgStore(t *testing.T) {
	f := newCanaryFixture(t, canaryManifest)
	ctx := t.Context()
	c := rampingCanary("bind_api")
	c.Status, c.Step, c.Weight, c.Services[0].Candidate = deployments.CanaryPending, -1, 0, ""
	d := f.deployment(t, "dep_c", c)
	if d.Canary == nil || d.Canary.Step != -1 || d.Canary.Services[0].Refs["service"] != "api" || d.Canary.Services[0].Baseline != "api-00001" || d.Canary.Thresholds != releasehealth.DefaultThresholds() {
		t.Fatalf("created: %+v", d.Canary)
	}
	if id, err := f.store.RampingCanary(ctx, "org", "env"); err != nil || id != "dep_c" {
		t.Fatalf("ramping: %q %v", id, err)
	}
	if ok, err := f.store.CanaryBinding(ctx, "org", "bind_api"); err != nil || !ok {
		t.Fatalf("its binding: %v %v", ok, err)
	}
	if ok, err := f.store.CanaryBinding(ctx, "org", "bind_web"); err != nil || ok {
		t.Fatalf("another binding: %v %v", ok, err)
	}

	c.Status, c.Step, c.Weight, c.Services[0].Candidate = deployments.CanaryRamping, 0, 50, "api-00002"
	if err := f.store.SetCanary(ctx, "org", "dep_c", c); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.GetDeployment(ctx, "org", "dep_c")
	if err != nil || !reflect.DeepEqual(got.Canary.Services, c.Services) || got.Canary.Weight != 50 || got.Canary.Status != deployments.CanaryRamping {
		t.Fatalf("stored: %+v %v", got.Canary, err)
	}
	if err = f.store.SetCanary(ctx, "org", "dep_missing", c); !errors.Is(err, deployments.ErrNotFound) {
		t.Fatalf("a deployment that isn't there: %v", err)
	}

	// Finished, it holds nothing, but it's still a canary the environment
	// ran.
	if err = f.store.Finish(ctx, "org", "dep_c", deployments.StatusFailed, "The canary failed at 50%: api: too many errors", time.Now()); err != nil {
		t.Fatal(err)
	}
	if id, rampErr := f.store.RampingCanary(ctx, "org", "env"); rampErr != nil || id != "" {
		t.Fatalf("ramping after finish: %q %v", id, rampErr)
	}
	if ok, bindErr := f.store.CanaryBinding(ctx, "org", "bind_api"); bindErr != nil || ok {
		t.Fatalf("its binding after finish: %v %v", ok, bindErr)
	}
	f.deployment(t, "dep_plain", nil)
	if _, err = f.sql.Exec(ctx, `UPDATE loomplane_deployments SET started_at = started_at + interval '1 minute' WHERE id = 'dep_plain'`); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Finish(ctx, "org", "dep_plain", deployments.StatusSucceeded, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	newer := rampingCanary("bind_api")
	newer.Weight = 100
	f.deployment(t, "dep_newer", newer)
	if _, err = f.sql.Exec(ctx, `UPDATE loomplane_deployments SET started_at = started_at + interval '2 minutes' WHERE id = 'dep_newer'`); err != nil {
		t.Fatal(err)
	}
	recent, err := f.store.RecentCanaries(ctx, "org", "proj", "env", 5)
	if err != nil || len(recent) != 2 || recent[0].ID != "dep_newer" || recent[1].ID != "dep_c" || recent[1].Canary.Weight != 50 || recent[1].Status != deployments.StatusFailed {
		t.Fatalf("recent: %+v %v", recent, err)
	}
	if recent, err = f.store.RecentCanaries(ctx, "org", "proj", "env", 1); err != nil || len(recent) != 1 || recent[0].ID != "dep_newer" {
		t.Fatalf("recent, one: %+v %v", recent, err)
	}
}

// Each verdict is a canary_verdict.updated event in its deployment's
// project, written once per judgement, and judged afresh on a retry; each
// canary change is a deployment.updated event carrying the canary.
func TestCanaryVerdictEmitsRealtimeEvents(t *testing.T) {
	f := newCanaryFixture(t, canaryManifest)
	ctx := t.Context()
	f.deployment(t, "dep_e", rampingCanary("bind_api"))
	now := time.Now().UTC().Truncate(time.Microsecond)
	verdict := func(retry, step int, v releasehealth.Verdict, reason string) *deployments.CanaryVerdict {
		return &deployments.CanaryVerdict{OrgID: "org", DeploymentID: "dep_e", Retry: retry, ActionID: "service:api", Service: "api", Step: step, Weight: []int{50, 100}[step],
			Verdict: v, Reason: reason, Thresholds: releasehealth.DefaultThresholds(), WindowStart: now.Add(-time.Minute), WindowEnd: now, DecidedAt: now,
			Baseline: releasehealth.Health{Requests: 300, Errors: 1, Available: true}, Candidate: releasehealth.Health{Requests: 250, Errors: 9, Available: true}}
	}
	for _, v := range []*deployments.CanaryVerdict{
		verdict(0, 1, releasehealth.Fail, "first"),
		verdict(0, 1, releasehealth.Fail, "the same judgement again"),
		verdict(0, 0, releasehealth.Pass, "passed"),
		verdict(1, 0, releasehealth.Inconclusive, "the retry judges afresh"),
	} {
		if err := f.store.RecordVerdict(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	vs, err := f.store.Verdicts(ctx, "org", "dep_e")
	if err != nil || len(vs) != 3 {
		t.Fatalf("verdicts: %+v %v", vs, err)
	}
	want := []string{"dep_e/0/service:api/0/0 passed", "dep_e/0/service:api/1/0 first", "dep_e/1/service:api/0/0 the retry judges afresh"}
	for i, v := range vs {
		if v.ID+" "+v.Reason != want[i] || v.ProjectID != "proj" {
			t.Fatalf("verdict %d: %s %s in %q", i, v.ID, v.Reason, v.ProjectID)
		}
	}
	if v := vs[1]; v.Candidate.Errors != 9 || v.Thresholds.MinRequests != 200 || !v.WindowEnd.Equal(now) || v.Weight != 100 {
		t.Fatalf("stored verdict: %+v", v)
	}

	rows, err := f.sql.Query(ctx, `SELECT entity_id, org_id, project_id, data->>'verdict' FROM loomplane_events WHERE type = 'canary_verdict.updated' ORDER BY sequence`)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	for rows.Next() {
		var id, org, project, v string
		if err = rows.Scan(&id, &org, &project, &v); err != nil {
			t.Fatal(err)
		}
		events = append(events, strings.Join([]string{id, org, project, v}, " "))
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(events, []string{"dep_e/0/service:api/1/0 org proj fail", "dep_e/0/service:api/0/0 org proj pass", "dep_e/1/service:api/0/0 org proj inconclusive"}) {
		t.Fatalf("verdict events: %q", events)
	}

	latest := func() (status, weight string) {
		t.Helper()
		if qerr := f.sql.QueryRow(ctx, `SELECT data->'canary'->>'status', data->'canary'->>'weight' FROM loomplane_events
			WHERE type = 'deployment.updated' AND entity_id = 'dep_e' AND project_id = 'proj' ORDER BY sequence DESC LIMIT 1`).Scan(&status, &weight); qerr != nil {
			t.Fatal(qerr)
		}
		return status, weight
	}
	c := rampingCanary("bind_api")
	c.Step, c.Weight = 1, 100
	if err = f.store.SetCanary(ctx, "org", "dep_e", c); err != nil {
		t.Fatal(err)
	}
	if status, weight := latest(); status != deployments.CanaryRamping || weight != "100" {
		t.Fatalf("deployment event after the weigh: %s at %s", status, weight)
	}
	c.Status, c.Weight = deployments.CanaryRolledBack, 0
	if err = f.store.SetCanary(ctx, "org", "dep_e", c); err != nil {
		t.Fatal(err)
	}
	if status, weight := latest(); status != deployments.CanaryRolledBack || weight != "0" {
		t.Fatalf("deployment event after the rollback: %s at %s", status, weight)
	}

	// The row keeps each service's provider handles for the executors, but
	// neither the events nor the snapshot carry them, as the API doesn't.
	var stored string
	if err = f.sql.QueryRow(ctx, `SELECT canary->'services'->0->'refs'->>'service' FROM loomplane_deployments WHERE id = 'dep_e'`).Scan(&stored); err != nil || stored != "api" {
		t.Fatalf("stored refs: %q %v", stored, err)
	}
	rt := realtime.NewPgStore(f.db)
	replay, found, err := rt.Events(ctx, "org", "org:0", realtime.MaxReplayEvents)
	if err != nil || !found {
		t.Fatalf("events: %v %v", found, err)
	}
	snapshot, err := rt.Snapshot(ctx, "org", nil, []string{"dep_e"})
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range append(replay, snapshot.Records...) {
		if e.Type != "deployment.updated" || e.EntityID != "dep_e" {
			continue
		}
		seen++
		var data struct {
			Canary struct {
				Services []map[string]json.RawMessage `json:"services"`
			} `json:"canary"`
		}
		if err = json.Unmarshal(e.Data, &data); err != nil {
			t.Fatal(err)
		}
		for _, svc := range data.Canary.Services {
			if _, ok := svc["refs"]; ok || len(svc) == 0 {
				t.Fatalf("deployment event %s carries %s", e.ID, e.Data)
			}
		}
	}
	if seen < 2 {
		t.Fatalf("saw %d deployment events", seen)
	}
}

// Retrying a failed deployment starts its canary over, keeping each
// baseline and every verdict of the run that failed.
func TestReopenResetsTheCanaryInPostgres(t *testing.T) {
	f := newCanaryFixture(t, canaryManifest)
	ctx := t.Context()
	c := rampingCanary("bind_api")
	c.Status, c.Steps, c.Step, c.Weight, c.Reason = deployments.CanaryRolledBack, []int{10, 50, 100}, 2, 50, "The canary failed at 100%"
	c.Services[0].Candidate = "x"
	f.deployment(t, "dep_r", c)
	if err := f.store.RecordVerdict(ctx, &deployments.CanaryVerdict{OrgID: "org", DeploymentID: "dep_r", ActionID: "service:api", Service: "api", Step: 2, Weight: 100, Verdict: releasehealth.Fail, Reason: "errors", WindowStart: time.Now(), WindowEnd: time.Now(), DecidedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpdateStep(ctx, "org", "dep_r", deployments.Step{ActionID: "service:api", Name: "api", Subject: "service", Op: "update", Provider: "cloud-run", Status: deployments.StepFailed, Message: "The canary failed at 100%"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Finish(ctx, "org", "dep_r", deployments.StatusFailed, "The canary failed at 100%", time.Now()); err != nil {
		t.Fatal(err)
	}
	ok, err := f.store.Reopen(ctx, "org", "dep_r")
	if err != nil || !ok {
		t.Fatalf("reopen: %v %v", ok, err)
	}
	d, err := f.store.GetDeployment(ctx, "org", "dep_r")
	if err != nil {
		t.Fatal(err)
	}
	got := d.Canary
	if got.Status != deployments.CanaryPending || got.Step != -1 || got.Weight != 0 || got.Reason != "" || !slices.Equal(got.Steps, []int{10, 50, 100}) {
		t.Fatalf("reopened canary: %+v", got)
	}
	if s := got.Services[0]; s.Candidate != "" || s.Image != "" || s.Baseline != "api-00001" || s.Refs["service"] != "api" || s.BindingID != "bind_api" {
		t.Fatalf("reopened service: %+v", s)
	}
	if d.Status != deployments.StatusRunning || d.Retries != 1 || d.Steps[0].Status != deployments.StepPending {
		t.Fatalf("reopened deployment: %s, retry %d, step %+v", d.Status, d.Retries, d.Steps[0])
	}
	if vs, verr := f.store.Verdicts(ctx, "org", "dep_r"); verr != nil || len(vs) != 1 || vs[0].ID != "dep_r/0/service:api/2/0" {
		t.Fatalf("verdicts after reopen: %+v %v", vs, verr)
	}
	// The retry ramps again, so it holds the environment again.
	if id, rerr := f.store.RampingCanary(ctx, "org", "env"); rerr != nil || id != "dep_r" {
		t.Fatalf("ramping after reopen: %q %v", id, rerr)
	}
}

// canaryMeter is a cloud's monitoring of one revision, counting reads.
type canaryMeter struct {
	mu    sync.Mutex
	reads []string
}

func (m *canaryMeter) MeasureRevision(_ context.Context, conn *connections.Connection, _ []byte, b *planning.Binding, revision string, w metrics.Window) (*metrics.Measurement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads = append(m.reads, conn.ID+" "+b.ID+" "+revision)
	return &metrics.Measurement{Requests: []metrics.Point{{T: w.Start, V: 600}}, Errors: []metrics.Point{{T: w.Start, V: 3}}, ErrorsCounted: true, Latency: 90, Statistic: "p95"}, nil
}

// Every canary read is scoped to the organization: another organization
// can't read a binding to judge it, nor see the canary that ramps it, its
// verdicts or its environment held.
func TestCanaryStoresKeepToTheirOrganization(t *testing.T) {
	f := newCanaryFixture(t, canaryManifest)
	ctx := t.Context()
	now := time.Now().UTC()
	if err := f.plans.PutBinding(ctx, &planning.Binding{ID: "bind_api", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "api", Provider: "cloud-run", Region: "us-central1", ExternalID: "projects/acme-prod/services/api", Config: json.RawMessage(`{}`), ConnectionID: "gcp", Revision: "registry/api:one", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	f.deployment(t, "dep_o", rampingCanary("bind_api"))
	if err := f.store.RecordVerdict(ctx, &deployments.CanaryVerdict{OrgID: "org", DeploymentID: "dep_o", ActionID: "service:api", Service: "api", Weight: 50, Verdict: releasehealth.Pass, Reason: "ok", WindowStart: now, WindowEnd: now, DecidedAt: now}); err != nil {
		t.Fatal(err)
	}

	if b, err := f.plans.GetBinding(ctx, "org", "bind_api"); err != nil || b.Name != "api" || b.ConnectionID != "gcp" {
		t.Fatalf("own binding: %+v %v", b, err)
	}
	if b, err := f.plans.GetBinding(ctx, "other", "bind_api"); !errors.Is(err, planning.ErrNotFound) || b != nil {
		t.Fatalf("another organization's binding: %+v %v", b, err)
	}
	meter := &canaryMeter{}
	reader := &deployments.HealthReader{Bindings: f.plans, Credentials: &syncAccounts{store: connections.NewPgStore(f.db)}, Meters: map[string]deployments.RevisionMeter{"cloud-run": meter}}
	start := now.Truncate(time.Minute).Add(-10 * time.Minute)
	w := releasehealth.Window{Start: start, End: start.Add(5 * time.Minute)}
	if h, err := reader.Health(ctx, "other", "bind_api", "api-00002", w); !errors.Is(err, planning.ErrNotFound) || h.Available || len(meter.reads) != 0 {
		t.Fatalf("judging another organization's binding: %+v %v, reads %v", h, err, meter.reads)
	}
	h, err := reader.Health(ctx, "org", "bind_api", "api-00002", w)
	if err != nil || !h.Available || h.Requests != 600 || h.Errors != 3 || !slices.Equal(meter.reads, []string{"gcp bind_api api-00002"}) {
		t.Fatalf("judging its own binding: %+v %v, reads %v", h, err, meter.reads)
	}

	for name, check := range map[string]func(org string) (any, error){
		"canary binding": func(org string) (any, error) { return f.store.CanaryBinding(ctx, org, "bind_api") },
		"ramping canary": func(org string) (any, error) { return f.store.RampingCanary(ctx, org, "env") },
		"planning's ramping canary": func(org string) (any, error) {
			return f.plans.RampingCanary(ctx, org, "env")
		},
		"verdicts": func(org string) (any, error) {
			vs, verr := f.store.Verdicts(ctx, org, "dep_o")
			return len(vs), verr
		},
		"recent canaries": func(org string) (any, error) {
			ds, rerr := f.store.RecentCanaries(ctx, org, "proj", "env", 5)
			return len(ds), rerr
		},
	} {
		mine, mineErr := check("org")
		if mineErr != nil {
			t.Fatalf("%s: %v", name, mineErr)
		}
		theirs, theirErr := check("other")
		if theirErr != nil {
			t.Fatalf("%s for another organization: %v", name, theirErr)
		}
		if reflect.DeepEqual(mine, theirs) || !reflect.ValueOf(theirs).IsZero() {
			t.Fatalf("%s: the organization reads %v, another reads %v", name, mine, theirs)
		}
	}
	settled := rampingCanary("bind_api")
	settled.Status = deployments.CanaryRolledBack
	if err = f.store.SetCanary(ctx, "other", "dep_o", settled); !errors.Is(err, deployments.ErrNotFound) {
		t.Fatalf("another organization set the canary: %v", err)
	}
	if d, getErr := f.store.GetDeployment(ctx, "org", "dep_o"); getErr != nil || d.Canary.Status != deployments.CanaryRamping {
		t.Fatalf("canary after another organization's write: %+v %v", d, getErr)
	}
}

// A canary ramping in an environment blocks a new plan for it and makes an
// older ready plan go stale when it's approved, naming the deployment.
// Once the canary promoted and its deployment succeeded, planning is
// ready again.
func TestCanaryBlocksANewPlanUntilItSettles(t *testing.T) {
	f := newCanaryFixture(t, canaryManifest)
	ctx := t.Context()
	f.release(t)
	if d := f.approve(t, f.plan(t)); d.Status != deployments.StatusSucceeded {
		t.Fatalf("first deployment: %+v", d)
	}
	f.release(t)
	older := f.plan(t)
	if older.Status != planning.StatusReady || !older.Release.Ramps("service:api") {
		t.Fatalf("older plan: %s %+v", older.Status, older.Problems)
	}

	f.deployment(t, "dep_ramp", rampingCanary(f.binding(t).ID))
	message := "A canary release is ramping in production (deployment dep_ramp). Plan again once it finishes."
	if _, err := f.service.Approve(ctx, f.actor, older.ID, planning.DecideInput{Revision: older.Revision}); err != nil {
		t.Fatal(err)
	}
	if stale := f.waitPlan(t, older.ID, planning.StatusReady); stale.Status != planning.StatusStale || stale.DecisionNote != message {
		t.Fatalf("older plan approved during the ramp: %s %q", stale.Status, stale.DecisionNote)
	}
	if _, err := f.store.ForPlan(ctx, "org", older.ID); !errors.Is(err, deployments.ErrNotFound) {
		t.Fatalf("the stale plan deployed: %v", err)
	}
	blocked := f.plan(t)
	finding := canaryInProgress(blocked.Problems)
	if blocked.Status != planning.StatusBlocked || blocked.Executable || finding == nil || finding.Message != message {
		t.Fatalf("plan during the ramp: %s, executable %v, problems %+v", blocked.Status, blocked.Executable, blocked.Problems)
	}

	c := rampingCanary(f.binding(t).ID)
	c.Status, c.Step, c.Weight = deployments.CanaryPromoted, 1, 100
	if err := f.store.SetCanary(ctx, "org", "dep_ramp", c); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Finish(ctx, "org", "dep_ramp", deployments.StatusSucceeded, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	ready := f.plan(t)
	if ready.Status != planning.StatusReady || !ready.Executable || canaryInProgress(ready.Problems) != nil || !ready.Release.Ramps("service:api") {
		t.Fatalf("plan after the canary settled: %s %+v", ready.Status, ready.Problems)
	}
}

// candidateLive is Cloud Run with a candidate revision in what's live, as
// a canary paused mid-ramp may leave it.
type candidateLive struct{ *canaryWeighted }

func (candidateLive) Observe(context.Context, *connections.Connection, []byte, *planning.Binding) (*deployments.Observation, error) {
	return &deployments.Observation{Exists: true, Revision: "registry/api:candidate"}, nil
}

// A canary deployment paused for capacity still holds its environment, as
// the environment guard says: a new plan is blocked, and drift leaves the
// binding it ramps alone, since what's live may show its candidate. Once
// the canary settles, the same check finds the drift.
func TestAPausedCanaryStillHoldsItsEnvironment(t *testing.T) {
	f := newCanaryFixture(t, canaryManifest)
	ctx := t.Context()
	f.release(t)
	if d := f.approve(t, f.plan(t)); d.Status != deployments.StatusSucceeded {
		t.Fatalf("first deployment: %+v", d)
	}
	f.release(t)
	api := f.binding(t)
	f.deployment(t, "dep_paused", rampingCanary(api.ID))
	paused, err := f.store.SetCapacityWait(ctx, "org", "dep_paused", 0, []cloudinfra.CapacityBlock{{Reason: "provisioning", Resource: "cpu", Retryable: true}}, time.Now())
	if err != nil || paused.Status != deployments.StatusPaused {
		t.Fatalf("pause: %+v %v", paused, err)
	}
	if id, rampErr := f.store.RampingCanary(ctx, "org", "env"); rampErr != nil || id != "dep_paused" {
		t.Fatalf("ramping while paused: %q %v", id, rampErr)
	}
	blocked := f.plan(t)
	message := "A canary release is ramping in production (deployment dep_paused). Plan again once it finishes."
	if finding := canaryInProgress(blocked.Problems); blocked.Status != planning.StatusBlocked || blocked.Executable || finding == nil || finding.Message != message {
		t.Fatalf("plan while the canary is paused: %s, executable %v, problems %+v", blocked.Status, blocked.Executable, blocked.Problems)
	}

	drift := &deployments.DriftChecker{
		Store: f.plans, Credentials: &syncAccounts{store: connections.NewPgStore(f.db)},
		Executors: map[string]deployments.Executor{"cloud-run": candidateLive{f.exec}}, Canaries: f.store,
	}
	rep, err := drift.Check(ctx)
	if err != nil || rep.Drifted != 0 {
		t.Fatalf("drift while paused: %+v %v", rep, err)
	}
	if b := f.binding(t); b.Drift != "" {
		t.Fatalf("api drifted while its canary was paused: %q %q", b.Drift, b.DriftMessage)
	}

	c := rampingCanary(api.ID)
	c.Status, c.Weight = deployments.CanaryRolledBack, 0
	if err = f.store.SetCanary(ctx, "org", "dep_paused", c); err != nil {
		t.Fatal(err)
	}
	if id, rampErr := f.store.RampingCanary(ctx, "org", "env"); rampErr != nil || id != "" {
		t.Fatalf("ramping once settled: %q %v", id, rampErr)
	}
	if rep, err = drift.Check(ctx); err != nil || rep.Drifted != 1 {
		t.Fatalf("drift once settled: %+v %v", rep, err)
	}
	if b := f.binding(t); b.Drift != planning.DriftChanged {
		t.Fatalf("api's drift once its canary settled: %q", b.Drift)
	}
}

// Changes made to one canary at once through UpdateCanary all land, each
// on the one before it, and a change that fails stores nothing.
func TestCanaryUpdatesDontLoseEachOther(t *testing.T) {
	f := newCanaryFixture(t, canaryManifest)
	ctx := t.Context()
	f.deployment(t, "dep_u", rampingCanary("bind_api"))
	const changes = 20
	errs := make(chan error, changes)
	var wg sync.WaitGroup
	for range changes {
		wg.Go(func() {
			_, err := f.store.UpdateCanary(ctx, "org", "dep_u", func(d *deployments.Deployment) error {
				d.Canary.Step++
				return nil
			})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	refused := errors.New("refused")
	if _, err := f.store.UpdateCanary(ctx, "org", "dep_u", func(d *deployments.Deployment) error {
		d.Canary.Step = 99
		return refused
	}); !errors.Is(err, refused) {
		t.Fatalf("a refused change: %v", err)
	}
	d, err := f.store.GetDeployment(ctx, "org", "dep_u")
	if err != nil || d.Canary.Step != changes {
		t.Fatalf("step %d after %d changes: %v", d.Canary.Step, changes, err)
	}
	if _, err = f.store.UpdateCanary(ctx, "org", "dep_missing", func(*deployments.Deployment) error { return nil }); !errors.Is(err, deployments.ErrNotFound) {
		t.Fatalf("a deployment that isn't there: %v", err)
	}
}

// A canary release on Postgres, from the manifest to the binding: approved
// once, it climbs 50% then 100%, judged by the cloud's monitoring after
// each one-minute hold, and promotes. The next release fails at 50%, rolls
// back by itself, and leaves the binding on what ran.
func TestCanaryEndToEndWithFakeClouds(t *testing.T) {
	f := newCanaryFixture(t, canaryManifest)
	ctx := t.Context()
	// A new service has nothing to compare with, so it deploys at once.
	f.release(t)
	first := f.plan(t)
	if first.Status != planning.StatusReady || first.Release == nil || len(first.Release.Canary) != 0 || len(first.Warnings) != 1 || first.Warnings[0].Code != "canary-unavailable" {
		t.Fatalf("first plan: %s, release %+v, warnings %+v, problems %+v", first.Status, first.Release, first.Warnings, first.Problems)
	}
	if d := f.approve(t, first); d.Status != deployments.StatusSucceeded || d.Canary != nil {
		t.Fatalf("first deployment: %+v", d)
	}
	baseline := f.binding(t)

	commit := f.release(t)
	second := f.plan(t)
	want := &planning.Release{Strategy: "canary", Steps: []int{50, 100}, HoldSeconds: 60, Thresholds: releasehealth.DefaultThresholds(), Canary: []string{"service:api"}}
	if second.Status != planning.StatusReady || !reflect.DeepEqual(second.Release, want) {
		t.Fatalf("second plan: %s, release %+v, problems %+v", second.Status, second.Release, second.Problems)
	}
	d := f.approve(t, second)
	if d.Status != deployments.StatusSucceeded || d.Error != "" {
		t.Fatalf("canary deployment: %+v", d)
	}
	c := d.Canary
	if c == nil || c.Status != deployments.CanaryPromoted || c.Weight != 100 || c.Step != 1 || c.HoldSeconds != 60 || len(c.Services) != 1 {
		t.Fatalf("canary: %+v", c)
	}
	svc := c.Services[0]
	if svc.BindingID != baseline.ID || svc.Baseline != "api-00001" || svc.Candidate != "api-00002" || svc.Image != "registry/api:"+commit {
		t.Fatalf("canary service: %+v", svc)
	}
	if s := d.Steps[0]; s.Status != deployments.StepSucceeded || s.Image != "registry/api:"+commit {
		t.Fatalf("step: %+v", s)
	}
	// The workflow read the manifest's ramp: a minute's hold, then the
	// metrics lag before each step's first judgement.
	if ramps := f.ramp.read(); len(ramps) != 1 || !reflect.DeepEqual(ramps[0], orchestration.CanaryRamp{Steps: []int{50, 100}, Hold: time.Minute, Lag: releasehealth.MetricsLag, MaxInconclusive: releasehealth.MaxInconclusive}) {
		t.Fatalf("ramps: %+v", ramps)
	}
	promoted := f.binding(t)
	if promoted.ID != baseline.ID || promoted.Revision != "registry/api:"+commit || promoted.CommitSHA != commit || promoted.PlanID != second.ID {
		t.Fatalf("binding after the promotion: %+v", promoted)
	}
	vs, err := f.store.Verdicts(ctx, "org", d.ID)
	if err != nil || len(vs) != 2 {
		t.Fatalf("verdicts: %+v %v", vs, err)
	}
	for i, v := range vs {
		if v.ID != deployments.VerdictID(d.ID, 0, "service:api", i, 0) || v.Verdict != releasehealth.Pass || v.Weight != []int{50, 100}[i] ||
			v.ProjectID != "proj" || v.Baseline.Requests != 1000 || v.Candidate.Errors != 1 || v.WindowEnd.Sub(v.WindowStart) != time.Millisecond {
			t.Fatalf("verdict %d: %+v", i, v)
		}
	}
	if got := strings.Join(f.exec.entries()[1:], "; "); got != "candidate api-00002 registry/api:"+commit+"; weigh api-00002 50; weigh api-00002 100; promote api-00002" {
		t.Fatalf("cloud calls: %s", got)
	}
	// Each step read the baseline and the candidate over its window.
	reads := f.health.read()
	if len(reads) != 4 {
		t.Fatalf("health reads: %+v", reads)
	}
	for i, r := range reads {
		v := vs[i/2]
		if r.binding != baseline.ID || r.revision != []string{"api-00001", "api-00002"}[i%2] || !r.window.Start.Equal(v.WindowStart) || !r.window.End.Equal(v.WindowEnd) {
			t.Fatalf("health read %d: %+v, verdict window %s to %s", i, r, v.WindowStart, v.WindowEnd)
		}
	}

	// The next release's candidate fails at 50%: traffic goes back and the
	// binding still names what runs.
	next := f.release(t)
	f.health.failCandidates(true)
	third := f.plan(t)
	if third.Status != planning.StatusReady {
		t.Fatalf("third plan: %s %+v", third.Status, third.Problems)
	}
	failed := f.approve(t, third)
	if failed.Status != deployments.StatusFailed || !strings.HasPrefix(failed.Error, "The canary failed at 50%") {
		t.Fatalf("failed canary deployment: %s %q", failed.Status, failed.Error)
	}
	if fc := failed.Canary; fc == nil || fc.Status != deployments.CanaryRolledBack || fc.Weight != 0 || fc.Step != 0 || fc.Services[0].Candidate != "" {
		t.Fatalf("failed canary: %+v", fc)
	}
	if s := failed.Steps[0]; s.Status != deployments.StepFailed {
		t.Fatalf("failed step: %+v", s)
	}
	if after := f.binding(t); after.Revision != promoted.Revision || after.PlanID != second.ID || after.CommitSHA != commit {
		t.Fatalf("binding after the rollback: %+v", after)
	}
	vs, err = f.store.Verdicts(ctx, "org", failed.ID)
	if err != nil || len(vs) != 1 || vs[0].Verdict != releasehealth.Fail || vs[0].Weight != 50 || vs[0].Candidate.Errors != 80 {
		t.Fatalf("failed verdicts: %+v %v", vs, err)
	}
	log := f.exec.entries()
	if got := strings.Join(log[len(log)-4:], "; "); got != "candidate api-00003 registry/api:"+next+"; weigh api-00003 50; weigh api-00003 0; revert api-00003" {
		t.Fatalf("cloud calls of the failed canary: %s", got)
	}
}

// A canary that settled as anything but active stops holding its
// environment, even while its deployment still runs. A promote or revert
// the workflow gave up on ends in the terminal settle activity, and after
// that a new plan for the environment is ready, not blocked for good.
func TestASettledCanaryNoLongerHoldsItsEnvironment(t *testing.T) {
	t.Run("statuses", func(t *testing.T) {
		f := newCanaryFixture(t, canaryManifest)
		ctx := t.Context()
		c := rampingCanary("bind_api")
		f.deployment(t, "dep_s", c)
		for status, holds := range map[string]bool{
			deployments.CanaryPending: true, deployments.CanaryRamping: true, deployments.CanaryPromoting: true, deployments.CanaryReverting: true,
			deployments.CanaryPromoted: false, deployments.CanaryRolledBack: false, deployments.CanaryRevertFailed: false, deployments.CanaryPromoteFailed: false,
		} {
			c.Status = status
			if err := f.store.SetCanary(ctx, "org", "dep_s", c); err != nil {
				t.Fatal(err)
			}
			id, err := f.store.RampingCanary(ctx, "org", "env")
			if err != nil {
				t.Fatal(err)
			}
			ramps, err := f.store.CanaryBinding(ctx, "org", "bind_api")
			if err != nil {
				t.Fatal(err)
			}
			if (id == "dep_s") != holds || ramps != holds {
				t.Fatalf("%s on a running deployment: holds the environment as %q, its binding %v", status, id, ramps)
			}
		}
	})

	// gaveUp checks a canary the workflow is still retrying holds its
	// environment, settles it as the workflow does once it gives up, and
	// checks the environment plans again.
	gaveUp := func(t *testing.T, f *canaryFixture, d *deployments.Deployment, holding, reason, settled string) {
		t.Helper()
		ctx := t.Context()
		if d.Canary.Status != holding {
			t.Fatalf("canary while the workflow retries: %+v", d.Canary)
		}
		blocked := f.plan(t)
		if blocked.Status != planning.StatusBlocked || canaryInProgress(blocked.Problems) == nil {
			t.Fatalf("plan while the workflow retries: %s %+v", blocked.Status, blocked.Problems)
		}
		// What the workflow's terminal settle activity does after its last
		// attempt, an hour of them in.
		if err := f.runner.CanarySettle(ctx, "org", d.ID, reason); err != nil {
			t.Fatal(err)
		}
		got, err := f.store.GetDeployment(ctx, "org", d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != deployments.StatusRunning || got.Canary.Status != settled || got.Canary.Reason != reason {
			t.Fatalf("deployment after the settle: %s, canary %+v", got.Status, got.Canary)
		}
		if id, rerr := f.store.RampingCanary(ctx, "org", "env"); rerr != nil || id != "" {
			t.Fatalf("ramping after the settle: %q %v", id, rerr)
		}
		ready := f.plan(t)
		if ready.Status != planning.StatusReady || canaryInProgress(ready.Problems) != nil {
			t.Fatalf("plan after the settle: %s %+v", ready.Status, ready.Problems)
		}
	}

	t.Run("a cancel whose revert failed", func(t *testing.T) {
		f := newCanaryFixture(t, canaryManifest)
		ctx := t.Context()
		f.release(t)
		f.approve(t, f.plan(t))
		f.release(t)
		p := f.plan(t)
		entered, release := f.exec.holdMove(t, 50)
		f.exec.failReverts(errors.New("Cloud Run is unavailable"))
		if _, err := f.service.Approve(ctx, f.actor, p.ID, planning.DecideInput{Revision: p.Revision}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-entered:
		case <-time.After(2 * time.Minute):
			t.Fatal("the canary never weighed 50%")
		}
		if err := f.workflows.Cancel(ctx, p.ID, "stopped by hand"); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the cancelled run's revert", func() bool { return f.exec.logged("revert api-00002 refused") })
		// The weigh in flight when the run was cancelled finishes now, and
		// moves nothing: the revert owns the canary.
		release()
		d := f.waitDeployment(t, p.ID, func(d *deployments.Deployment) bool {
			return d.Canary != nil && d.Canary.Status == deployments.CanaryReverting
		})
		gaveUp(t, f, d, deployments.CanaryReverting,
			"The deployment was cancelled: stopped by hand Traffic may still be split: api: Cloud Run is unavailable.", deployments.CanaryRevertFailed)
		got, err := f.store.GetDeployment(ctx, "org", d.ID)
		if err != nil || got.Canary.Services[0].Candidate != "api-00002" {
			t.Fatalf("the candidate that couldn't be removed: %+v %v", got.Canary, err)
		}
	})

	t.Run("a promote that failed", func(t *testing.T) {
		f := newCanaryFixture(t, canaryManifest)
		f.release(t)
		f.approve(t, f.plan(t))
		f.release(t)
		p := f.plan(t)
		f.exec.failPromotes(errors.New("Cloud Run is unavailable"))
		if _, err := f.service.Approve(t.Context(), f.actor, p.ID, planning.DecideInput{Revision: p.Revision}); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the refused promote", func() bool { return f.exec.logged("promote api-00002 refused") })
		d := f.waitDeployment(t, p.ID, func(d *deployments.Deployment) bool {
			return d.Canary != nil && d.Canary.Status == deployments.CanaryPromoting
		})
		gaveUp(t, f, d, deployments.CanaryPromoting, "Traffic may still be split: api: Cloud Run is unavailable.", deployments.CanaryPromoteFailed)
	})
}

// A ready plan made by a Loomplane from before release blocks approves
// after the upgrade: planning again gives the same revision, so it doesn't
// go stale, and it deploys all at once. testdata/plan-before-releases.json
// is the database such a Loomplane (commit 9e6e5e5c) left once it deployed
// api and planned its next commit, as the rows planning reads.
func TestPlansWithoutReleaseBlocksKeepTheirRevisionAcrossTheUpgrade(t *testing.T) {
	f := newCanaryFixture(t, "")
	ctx := t.Context()
	raw, err := os.ReadFile("testdata/plan-before-releases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"loomplane_project_sources", "loomplane_analyses", "loomplane_manifests", "loomplane_plans", "loomplane_plan_sources", "loomplane_bindings", "loomplane_environment_sources", "loomplane_environment_connections"} {
		// Only the columns that Loomplane had: a column a later migration
		// added takes its default, as an upgraded database's rows do.
		var rows []map[string]json.RawMessage
		if err = json.Unmarshal(fixture[table], &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			continue
		}
		names := map[string]bool{}
		for _, row := range rows {
			for name := range row {
				names[name] = true
			}
		}
		columns := make([]string, 0, len(names))
		for _, name := range slices.Sorted(maps.Keys(names)) {
			columns = append(columns, pgx.Identifier{name}.Sanitize())
		}
		list := strings.Join(columns, ",")
		if _, err = f.sql.Exec(ctx, `INSERT INTO `+table+` (`+list+`) SELECT `+list+` FROM jsonb_populate_recordset(NULL::`+table+`, $1)`, []byte(fixture[table])); err != nil {
			t.Fatalf("restore %s: %v", table, err)
		}
	}
	var id string
	if err = json.Unmarshal(fixture["plan_id"], &id); err != nil {
		t.Fatal(err)
	}
	var result string
	if err = f.sql.QueryRow(ctx, `SELECT result::text FROM loomplane_plans WHERE id = $1`, id).Scan(&result); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result, `"release"`) {
		t.Fatalf("the fixture plan has a release: %s", result)
	}
	plan, err := f.plans.GetPlan(ctx, "org", id)
	if err != nil || plan.Status != planning.StatusReady || plan.Release != nil || plan.Actions[0].Op != planning.Update {
		t.Fatalf("fixture plan: %+v %v", plan, err)
	}
	before := f.binding(t)

	if err = f.workflows.Start(ctx, "org", id, time.Hour); err != nil {
		t.Fatal(err)
	}
	d := f.approve(t, plan)
	if d.Status != deployments.StatusSucceeded || d.Canary != nil {
		t.Fatalf("deployment: %+v", d)
	}
	approved, err := f.plans.GetPlan(ctx, "org", id)
	if err != nil || approved.Status != planning.StatusApproved || approved.DecisionNote != "" || approved.Revision != plan.Revision {
		t.Fatalf("plan after approval: %s %q %v", approved.Status, approved.DecisionNote, err)
	}
	after := f.binding(t)
	if after.ID != before.ID || after.CommitSHA != plan.Actions[0].Service.CommitSHA || after.CommitSHA == before.CommitSHA {
		t.Fatalf("binding before %+v, after %+v", before, after)
	}
	if got := f.exec.entries(); len(got) != 1 || !strings.HasPrefix(got[0], "apply ") {
		t.Fatalf("cloud calls: %v", got)
	}
}

// The canary migration runs on a fresh database at its version, and its
// down migration takes the schema back to two workflow builds, after which
// it runs again.
func TestCanaryReleasesMigrationOnAFreshDatabase(t *testing.T) {
	ctx := t.Context()
	dsn := freshDatabase(t)
	groups := []*migrate.Group{migrations.Group()}
	if err := ops.Migrate(ctx, dsn, groups); err != nil {
		t.Fatal(err)
	}
	const canary = "loomplane:20261104000100 canary_releases"
	status, err := ops.SchemaStatus(ctx, dsn, groups)
	if err != nil || !slices.Contains(status.Applied, canary) || len(status.Pending) != 0 {
		t.Fatalf("status after migrating: %+v %v", status, err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	schema := func() string {
		t.Helper()
		var table, column, checks string
		if qerr := conn.QueryRow(ctx, `SELECT coalesce(to_regclass('loomplane_canary_verdicts')::text, 'none'),
			coalesce((SELECT data_type FROM information_schema.columns WHERE table_name = 'loomplane_deployments' AND column_name = 'canary'), 'none'),
			(SELECT string_agg(conname::text, ',' ORDER BY conname) FROM pg_constraint WHERE conrelid = 'loomplane_deployment_workflow_runs'::regclass AND contype = 'c')`).Scan(&table, &column, &checks); qerr != nil {
			t.Fatal(qerr)
		}
		return table + " " + column + " " + checks
	}
	if got := schema(); got != "loomplane_canary_verdicts jsonb loomplane_deployment_workflow_runs_build" {
		t.Fatalf("schema after migrating: %s", got)
	}

	db := pgdriver.New()
	if err = db.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Grove rolls back the last applied migration only, so later ones go
	// first, one at a time, until the canary's own does. Until then the
	// canary's schema stays.
	const present = "loomplane_canary_verdicts jsonb loomplane_deployment_workflow_runs_build"
	o := migrate.NewOrchestrator(pgmigrate.New(db), migrations.Group())
	for n := 0; ; n++ {
		if n == len(migrations.Group().Migrations()) {
			t.Fatal("rolled back every migration without reaching canary_releases")
		}
		if got := schema(); got != present {
			t.Fatalf("schema before rolling back canary_releases: %s", got)
		}
		res, rerr := o.Rollback(ctx)
		if rerr != nil || len(res.Rollback) != 1 {
			t.Fatalf("rollback: %+v %v", res, rerr)
		}
		if res.Rollback[0].Version == "20261104000100" {
			break
		}
	}
	if got := schema(); got != "none none loomplane_deployment_workflow_runs_request_check" {
		t.Fatalf("schema after the down migration: %s", got)
	}
	if status, err = ops.SchemaStatus(ctx, dsn, groups); err != nil || !slices.Contains(status.Pending, canary) || slices.Contains(status.Applied, canary) {
		t.Fatalf("status after the down migration: %+v %v", status, err)
	}
	if err = ops.Migrate(ctx, dsn, groups); err != nil {
		t.Fatal(err)
	}
	if got := schema(); got != present {
		t.Fatalf("schema after migrating again: %s", got)
	}
}
