package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/releasehealth"
)

// weighted is a Weighted executor that records each canary call as
// "<op> <service>[ <weight>]", and applies everything else as recorder
// does.
type weighted struct {
	recorder
	cmu       sync.Mutex
	calls     []string
	jobs      []*Job
	revertErr error
}

func (w *weighted) record(job *Job, call string) {
	w.cmu.Lock()
	defer w.cmu.Unlock()
	w.calls = append(w.calls, call)
	w.jobs = append(w.jobs, job)
}

func (w *weighted) log() []string {
	w.cmu.Lock()
	defer w.cmu.Unlock()
	return slices.Clone(w.calls)
}

func (w *weighted) ApplyCandidate(_ context.Context, job *Job) (*CanaryTarget, error) {
	name := job.Action.Name
	w.record(job, "candidate "+name)
	return &CanaryTarget{Baseline: name + "-00001", Candidate: name + "-00002", Image: "img:" + name + "-new",
		Refs: map[string]string{"service": "loomplane-" + name}}, nil
}

func (w *weighted) SetWeights(_ context.Context, job *Job, t CanaryTarget, percent int) error {
	w.record(job, fmt.Sprintf("weigh %s %s %d", job.Action.Name, t.Candidate, percent))
	return nil
}

func (w *weighted) Promote(_ context.Context, job *Job, t CanaryTarget) (*Outcome, error) {
	w.record(job, "promote "+job.Action.Name)
	return &Outcome{ExternalID: "ext-" + job.Action.Name, URL: "https://" + job.Action.Name + ".example", Image: t.Image, Revision: t.Image}, nil
}

func (w *weighted) Revert(_ context.Context, job *Job, t CanaryTarget) error {
	w.record(job, "revert "+job.Action.Name+" "+t.Baseline)
	return w.revertErr
}

var _ Weighted = (*weighted)(nil)

// canaryPlan updates api, which ramps, and the resource db, which doesn't.
func canaryPlan() *planning.Plan {
	return &planning.Plan{
		ID: "plan_1", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Environment: "production",
		Status: planning.StatusApproved, Executable: true,
		Sources: []planning.PlanSource{{SourceID: "src_1", Alias: "mono", Repository: "acme/mono", AnalysisID: "anl_1",
			CommitSHA: strings.Repeat("a", 40), InScope: true}},
		Connections: map[string]string{"gcp": "conn_g"},
		Release: &planning.Release{Strategy: "canary", Steps: []int{5, 25, 50, 100}, HoldSeconds: 600,
			Thresholds: releasehealth.DefaultThresholds(), Canary: []string{"service:api"}},
		Actions: []planning.Action{
			{ID: "resource:db", Op: planning.Update, Subject: planning.ResourceSubject, Name: "db", Provider: "cloud-run", Step: 1,
				Resource: &planning.ResourceConfig{}},
			{ID: "service:api", Op: planning.Update, Subject: planning.ServiceSubject, Name: "api", Provider: "cloud-run", Step: 2,
				Service: &planning.ServiceConfig{Provider: "cloud-run", Service: "svc:go:services/api", Public: true}},
		},
	}
}

// withWeb adds web, which ramps too, after api.
func withWeb(plan *planning.Plan) *planning.Plan {
	plan.Actions = append(plan.Actions, planning.Action{ID: "service:web", Op: planning.Update, Subject: planning.ServiceSubject,
		Name: "web", Provider: "cloud-run", Step: 3,
		Service: &planning.ServiceConfig{Provider: "cloud-run", Service: "svc:go:services/web", Public: true}})
	plan.Release.Canary = append(plan.Release.Canary, "service:web")
	return plan
}

// deployed binds each of the plan's actions as an earlier deployment left
// them.
func deployed(plans *fakePlans, plan *planning.Plan) {
	for _, a := range plan.Actions {
		plans.bindings[a.ID] = &planning.Binding{ID: "bind_" + a.Name, OrgID: plan.OrgID, ProjectID: plan.ProjectID,
			EnvironmentID: plan.EnvironmentID, Subject: a.Subject, Name: a.Name, Provider: a.Provider, ConnectionID: "conn_g",
			ExternalID: "ext-" + a.Name, URL: "https://" + a.Name + ".example", Revision: "img:" + a.Name + "-old", Config: json.RawMessage(`{}`)}
	}
}

func newCanaryRunner(t *testing.T, plan *planning.Plan) (*Runner, *memStore, *fakePlans, *weighted) {
	t.Helper()
	exec := &weighted{recorder: recorder{url: answering(t, http.StatusOK)}}
	r, store, plans := newRunner(t, plan, exec, nil)
	deployed(plans, plan)
	return r, store, plans, exec
}

// startCanary starts a deployment as a canary workflow does, and runs its
// actions until one fails.
func startCanary(t *testing.T, r *Runner, plan *planning.Plan) (string, error) {
	t.Helper()
	ctx := orchestration.CanaryContext(t.Context())
	id, steps, err := r.Start(ctx, plan.OrgID, plan.ID)
	if err != nil || id == "" {
		t.Fatalf("start: %q %v", id, err)
	}
	if err = r.FetchSource(ctx, plan.OrgID, id); err != nil {
		t.Fatal(err)
	}
	for _, group := range steps {
		for _, action := range group {
			if err = r.Run(ctx, plan.OrgID, id, action, nil, func([]byte) error { return nil }); err != nil {
				return id, err
			}
		}
	}
	return id, nil
}

func getDeployment(t *testing.T, store *memStore, id string) *Deployment {
	t.Helper()
	d, err := store.GetDeployment(t.Context(), "org_1", id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestStartRecordsTheCanary(t *testing.T) {
	plan := canaryPlan()
	r, store, _, _ := newCanaryRunner(t, plan)
	id, _, err := r.Start(orchestration.CanaryContext(t.Context()), plan.OrgID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := getDeployment(t, store, id).Canary
	if c == nil {
		t.Fatal("no canary recorded")
	}
	if !c.UpdatedAt.Equal(store.d[id].StartedAt) {
		t.Fatalf("updated at %v, want the start %v", c.UpdatedAt, store.d[id].StartedAt)
	}
	c.UpdatedAt = time.Time{}
	want := &Canary{Status: CanaryPending, Steps: []int{5, 25, 50, 100}, HoldSeconds: 600, Thresholds: releasehealth.DefaultThresholds(),
		Step: -1, Weight: 0, Services: []CanaryService{{ActionID: "service:api", Name: "api", Provider: "cloud-run", BindingID: "bind_api"}}}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("canary\n%+v\nwant\n%+v", c, want)
	}

	t.Run("without a release", func(t *testing.T) {
		plan := canaryPlan()
		plan.Release = nil
		r, store, _, _ := newCanaryRunner(t, plan)
		id, _, err := r.Start(orchestration.CanaryContext(t.Context()), plan.OrgID, plan.ID)
		if err != nil || getDeployment(t, store, id).Canary != nil {
			t.Fatalf("canary %+v, %v", store.d[id].Canary, err)
		}
	})
	t.Run("with nothing that ramps", func(t *testing.T) {
		plan := canaryPlan()
		plan.Release.Canary = nil
		r, store, _, _ := newCanaryRunner(t, plan)
		id, _, err := r.Start(orchestration.CanaryContext(t.Context()), plan.OrgID, plan.ID)
		if err != nil || getDeployment(t, store, id).Canary != nil {
			t.Fatalf("canary %+v, %v", store.d[id].Canary, err)
		}
	})
	t.Run("on a build that doesn't ramp", func(t *testing.T) {
		plan := canaryPlan()
		r, store, _, _ := newCanaryRunner(t, plan)
		id, _, err := r.Start(t.Context(), plan.OrgID, plan.ID)
		if err != nil || getDeployment(t, store, id).Canary != nil {
			t.Fatalf("canary %+v, %v", store.d[id].Canary, err)
		}
	})
}

func TestRunDeploysTheCandidateDark(t *testing.T) {
	plan := canaryPlan()
	r, store, plans, exec := newCanaryRunner(t, plan)
	before := *plans.bindings["service:api"]
	id, err := startCanary(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if got := exec.log(); !slices.Equal(got, []string{"candidate api"}) {
		t.Fatalf("canary calls %v", got)
	}
	for _, job := range exec.jobs {
		if job.Action.Name == "api" && (job.Binding == nil || job.Binding.ID != "bind_api") {
			t.Fatalf("the candidate's job names binding %+v", job.Binding)
		}
	}
	for _, job := range exec.recorder.jobs {
		if job.Action.Name == "api" {
			t.Fatal("Apply deployed api all at once")
		}
	}
	if got := *plans.bindings["service:api"]; !reflect.DeepEqual(got, before) {
		t.Fatalf("binding changed:\n%+v\nwas\n%+v", got, before)
	}
	d := getDeployment(t, store, id)
	step := d.Step("service:api")
	if step.Status != StepRamping || step.Message != "The candidate is up and takes no traffic yet" {
		t.Fatalf("step %s %q", step.Status, step.Message)
	}
	svc := d.Canary.Services[0]
	if svc.Baseline != "api-00001" || svc.Candidate != "api-00002" || svc.Image != "img:api-new" || svc.Refs["service"] != "loomplane-api" {
		t.Fatalf("canary service %+v", svc)
	}
	if d.Canary.Status != CanaryPending {
		t.Fatalf("canary status %s before the ramp", d.Canary.Status)
	}
}

func TestRunAgainOnARampingStepDoesNothing(t *testing.T) {
	plan := canaryPlan()
	r, store, _, exec := newCanaryRunner(t, plan)
	id, err := startCanary(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	calls := len(exec.log())
	if err = r.Run(orchestration.CanaryContext(t.Context()), plan.OrgID, id, "service:api", nil, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(exec.log()) != calls {
		t.Fatalf("a second run called the executor: %v", exec.log())
	}
	if s := getDeployment(t, store, id).Step("service:api"); s.Status != StepRamping {
		t.Fatalf("step %s", s.Status)
	}
}

func TestNonCanaryActionsDeployAsToday(t *testing.T) {
	plan := canaryPlan()
	r, store, plans, exec := newCanaryRunner(t, plan)
	id, err := startCanary(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	applied := make([]string, 0, len(exec.recorder.jobs))
	for _, job := range exec.recorder.jobs {
		applied = append(applied, job.Action.Name)
	}
	if !slices.Equal(applied, []string{"db"}) {
		t.Fatalf("applied %v, want db", applied)
	}
	if s := getDeployment(t, store, id).Step("resource:db"); s.Status != StepSucceeded {
		t.Fatalf("db step %s", s.Status)
	}
	if b := plans.bindings["resource:db"]; b.PlanID != plan.ID {
		t.Fatalf("db wasn't bound by the plan: %+v", b)
	}
}

func TestWeighMovesEveryCanaryService(t *testing.T) {
	plan := withWeb(canaryPlan())
	r, store, _, exec := newCanaryRunner(t, plan)
	at := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return at }
	id, err := startCanary(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := r.CanaryWeigh(t.Context(), plan.OrgID, id, 1, 25)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Equal(at) {
		t.Fatalf("applied at %v, want %v", applied, at)
	}
	want := []string{"candidate api", "candidate web", "weigh api api-00002 25", "weigh web web-00002 25"}
	if got := exec.log(); !slices.Equal(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
	for _, job := range exec.jobs[2:] {
		if job.Connection == nil || job.Connection.ID != "conn_g" || string(job.Credential) != "secret-conn_g" || job.Binding == nil {
			t.Fatalf("weigh job without its connection or binding: %+v", job)
		}
	}
	c := getDeployment(t, store, id).Canary
	if c.Status != CanaryRamping || c.Step != 1 || c.Weight != 25 || !c.UpdatedAt.Equal(at) {
		t.Fatalf("canary %+v", c)
	}
}

func TestPromoteBindsTheCandidate(t *testing.T) {
	plan := canaryPlan()
	r, store, plans, exec := newCanaryRunner(t, plan)
	observed := &releaseLog{}
	r.observers = []ReleaseObserver{observed}
	id, err := startCanary(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.CanaryWeigh(t.Context(), plan.OrgID, id, 0, 5); err != nil {
		t.Fatal(err)
	}
	if err = r.CanaryPromote(t.Context(), plan.OrgID, id); err != nil {
		t.Fatal(err)
	}
	b := plans.bindings["service:api"]
	if b.Revision != "img:api-new" || b.ID != "bind_api" || b.PlanID != plan.ID {
		t.Fatalf("binding %+v", b)
	}
	if rel := observed.all(); len(rel) != 1 || rel[0].Binding.Name != "api" || rel[0].Removed {
		t.Fatalf("observers heard %+v", rel)
	}
	d := getDeployment(t, store, id)
	if s := d.Step("service:api"); s.Status != StepSucceeded || s.Image != "img:api-new" || s.FinishedAt == nil {
		t.Fatalf("step %+v", s)
	}
	if d.Canary.Status != CanaryPromoted {
		t.Fatalf("canary %s", d.Canary.Status)
	}
	calls := exec.log()
	if err = r.CanaryPromote(t.Context(), plan.OrgID, id); err != nil {
		t.Fatal(err)
	}
	if got := exec.log(); !slices.Equal(got, calls) || len(observed.all()) != 1 {
		t.Fatalf("a second promote did something: %v", got)
	}
	if err = r.Finish(t.Context(), plan.OrgID, id, ""); err != nil {
		t.Fatal(err)
	}
	if d = getDeployment(t, store, id); d.Status != StatusSucceeded || d.Canary.Status != CanaryPromoted {
		t.Fatalf("finished %s, canary %s", d.Status, d.Canary.Status)
	}
}

func TestRevertRestoresTheBaseline(t *testing.T) {
	plan := withWeb(canaryPlan())
	r, store, plans, exec := newCanaryRunner(t, plan)
	before := map[string]planning.Binding{"api": *plans.bindings["service:api"], "web": *plans.bindings["service:web"]}
	id, err := startCanary(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.CanaryWeigh(t.Context(), plan.OrgID, id, 1, 25); err != nil {
		t.Fatal(err)
	}
	reason := "The canary failed at 25%: web: 3.2% of requests failed"
	if err = r.CanaryRevert(t.Context(), plan.OrgID, id, reason); err != nil {
		t.Fatal(err)
	}
	want := []string{"weigh api api-00002 0", "revert api api-00001", "weigh web web-00002 0", "revert web web-00001"}
	if got := exec.log()[4:]; !slices.Equal(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
	for name, b := range before {
		if got := *plans.bindings["service:"+name]; !reflect.DeepEqual(got, b) {
			t.Fatalf("%s's binding changed: %+v", name, got)
		}
	}
	d := getDeployment(t, store, id)
	for _, action := range []string{"service:api", "service:web"} {
		if s := d.Step(action); s.Status != StepFailed || s.Message != reason || s.FinishedAt == nil {
			t.Fatalf("%s step %+v", action, s)
		}
	}
	if d.Canary.Status != CanaryRolledBack || d.Canary.Reason != reason || d.Canary.Weight != 0 {
		t.Fatalf("canary %+v", d.Canary)
	}
	calls := exec.log()
	if err = r.CanaryRevert(t.Context(), plan.OrgID, id, reason); err != nil {
		t.Fatal(err)
	}
	if got := exec.log(); !slices.Equal(got, calls) {
		t.Fatalf("a second revert did something: %v", got[len(calls):])
	}
	if err = r.Finish(t.Context(), plan.OrgID, id, reason); err != nil {
		t.Fatal(err)
	}
	if d = getDeployment(t, store, id); d.Status != StatusFailed || d.Canary.Status != CanaryRolledBack {
		t.Fatalf("finished %s, canary %s", d.Status, d.Canary.Status)
	}
}

func TestRevertBeforeEveryCandidateIsUp(t *testing.T) {
	plan := withWeb(canaryPlan())
	plan.Actions = slices.Insert(plan.Actions, 2, planning.Action{ID: "service:worker", Op: planning.Update, Subject: planning.ServiceSubject,
		Name: "worker", Provider: "cloud-run", Step: 3, Service: &planning.ServiceConfig{Provider: "cloud-run", Service: "svc:go:services/worker"}})
	plan.Actions[3].Step = 4
	r, store, _, exec := newCanaryRunner(t, plan)
	exec.fail = map[string]error{"worker": errors.New("the worker's image didn't build")}
	id, err := startCanary(t, r, plan)
	if err == nil {
		t.Fatal("the worker deployed")
	}
	if err = r.CanaryRevert(t.Context(), plan.OrgID, id, err.Error()); err != nil {
		t.Fatal(err)
	}
	want := []string{"candidate api", "weigh api api-00002 0", "revert api api-00001"}
	if got := exec.log(); !slices.Equal(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
	d := getDeployment(t, store, id)
	if s := d.Step("service:api"); s.Status != StepFailed {
		t.Fatalf("api step %s", s.Status)
	}
	if s := d.Step("service:web"); s.Status != StepSkipped {
		t.Fatalf("web step %s", s.Status)
	}
	if d.Canary.Status != CanaryRolledBack {
		t.Fatalf("canary %s", d.Canary.Status)
	}
}

func TestRevertErrorLeavesStatusReverting(t *testing.T) {
	plan := withWeb(canaryPlan())
	r, store, _, exec := newCanaryRunner(t, plan)
	id, err := startCanary(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.CanaryWeigh(t.Context(), plan.OrgID, id, 0, 5); err != nil {
		t.Fatal(err)
	}
	exec.revertErr = errors.New("permission denied")
	err = r.CanaryRevert(t.Context(), plan.OrgID, id, "The canary failed at 5%: api: too slow")
	if err == nil || !strings.Contains(err.Error(), "api: permission denied") || !strings.Contains(err.Error(), "web: permission denied") {
		t.Fatalf("revert error %v, want it to name api and web", err)
	}
	if c := getDeployment(t, store, id).Canary; c.Status != CanaryReverting {
		t.Fatalf("canary %s", c.Status)
	}
	if err = r.Finish(t.Context(), plan.OrgID, id, "The canary failed at 5%: api: too slow. Traffic may still be split: "+err.Error()); err != nil {
		t.Fatal(err)
	}
	d := getDeployment(t, store, id)
	if d.Canary.Status != CanaryRevertFailed {
		t.Fatalf("canary %s after finishing", d.Canary.Status)
	}
	for _, action := range []string{"service:api", "service:web"} {
		if s := d.Step(action); s.Status != StepFailed {
			t.Fatalf("%s step %s after finishing", action, s.Status)
		}
	}
}

func TestFinishFailsAPromotingCanary(t *testing.T) {
	plan := canaryPlan()
	r, store, _, _ := newCanaryRunner(t, plan)
	id, err := startCanary(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	c := getDeployment(t, store, id).Canary
	c.Status = CanaryPromoting
	if err = store.SetCanary(t.Context(), plan.OrgID, id, c); err != nil {
		t.Fatal(err)
	}
	if err = r.Finish(t.Context(), plan.OrgID, id, "Traffic may still be split: the cloud is down."); err != nil {
		t.Fatal(err)
	}
	d := getDeployment(t, store, id)
	if d.Canary.Status != CanaryPromoteFailed || d.Step("service:api").Status != StepFailed {
		t.Fatalf("canary %s, step %s", d.Canary.Status, d.Step("service:api").Status)
	}
}

func TestRampOfADeploymentWithoutACanaryIsEmpty(t *testing.T) {
	plan := canaryPlan()
	plan.Release = nil
	r, _, _, _ := newCanaryRunner(t, plan)
	id, err := startCanary(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	ramp, err := r.CanaryRamp(t.Context(), plan.OrgID, id)
	if err != nil || !reflect.DeepEqual(ramp, orchestration.CanaryRamp{}) {
		t.Fatalf("ramp %+v, %v", ramp, err)
	}
}

func TestRampCarriesTheControllerBounds(t *testing.T) {
	plan := canaryPlan()
	r, _, _, _ := newCanaryRunner(t, plan)
	id, err := startCanary(t, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	ramp, err := r.CanaryRamp(t.Context(), plan.OrgID, id)
	if err != nil {
		t.Fatal(err)
	}
	want := orchestration.CanaryRamp{Steps: []int{5, 25, 50, 100}, Hold: 10 * time.Minute, Lag: releasehealth.MetricsLag, MaxInconclusive: releasehealth.MaxInconclusive}
	if !reflect.DeepEqual(ramp, want) {
		t.Fatalf("ramp %+v, want %+v", ramp, want)
	}
}

// driftStore is a DriftStore of fixed bindings that records each drift.
type driftStore struct {
	bindings []*planning.Binding
	recorded []string
}

func (s *driftStore) AllBindings(_ context.Context, after string, limit int) ([]*planning.Binding, error) {
	var out []*planning.Binding
	for _, b := range s.bindings {
		if b.ID > after && len(out) < limit {
			out = append(out, b)
		}
	}
	return out, nil
}

func (s *driftStore) RecordDrift(_ context.Context, _, id, _, _ string, _ time.Time) error {
	s.recorded = append(s.recorded, id)
	return nil
}

// observed is an executor whose Observe records each binding it reads and
// finds image "img:live".
type observed struct {
	recorder
	seen []string
}

func (o *observed) Observe(_ context.Context, _ *connections.Connection, _ []byte, b *planning.Binding) (*Observation, error) {
	o.seen = append(o.seen, b.ID)
	return &Observation{Exists: true, Revision: "img:live"}, nil
}

type canaryBindings map[string]bool

func (c canaryBindings) CanaryBinding(_ context.Context, _, id string) (bool, error) {
	return c[id], nil
}

func TestDriftSkipsBindingsInARunningCanary(t *testing.T) {
	store := &driftStore{bindings: []*planning.Binding{
		{ID: "bind_api", OrgID: "org_1", Name: "api", Provider: "cloud-run", ConnectionID: "conn_g", Revision: "img:api"},
		{ID: "bind_web", OrgID: "org_1", Name: "web", Provider: "cloud-run", ConnectionID: "conn_g", Revision: "img:web"},
	}}
	exec := &observed{}
	c := &DriftChecker{Store: store, Credentials: fakeCredentials{}, Executors: map[string]Executor{"cloud-run": exec},
		Canaries: canaryBindings{"bind_api": true}}
	rep, err := c.Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Skipped != 1 || rep.Checked != 1 || rep.Drifted != 1 {
		t.Fatalf("report %+v", rep)
	}
	if !slices.Equal(exec.seen, []string{"bind_web"}) || !slices.Equal(store.recorded, []string{"bind_web"}) {
		t.Fatalf("observed %v, recorded %v", exec.seen, store.recorded)
	}
}
