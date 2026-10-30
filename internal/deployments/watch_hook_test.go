package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// recordingWatches is a WatchHook that records what it was asked, with
// the deployment's stored status at each call.
type recordingWatches struct {
	mu     sync.Mutex
	store  Store
	calls  []string
	armed  []Deployment
	planID string
}

func (w *recordingWatches) stored(ctx context.Context, d *Deployment) string {
	got, err := w.store.GetDeployment(ctx, d.OrgID, d.ID)
	if err != nil {
		return "missing"
	}
	return got.Status
}

func (w *recordingWatches) Prepare(ctx context.Context, d *Deployment, plan *planning.Plan) (WatchDecision, error) {
	status := w.stored(ctx, d)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, "prepare:"+status)
	w.planID = plan.ID
	return WatchDecision{Armed: true, Duration: 15 * time.Minute}, nil
}

func (w *recordingWatches) Arm(ctx context.Context, d *Deployment, decision WatchDecision) error {
	status := w.stored(ctx, d)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, "arm:"+status)
	if !decision.Armed || decision.Duration != 15*time.Minute {
		w.calls = append(w.calls, "arm without the decision")
	}
	w.armed = append(w.armed, *d)
	return nil
}

func (w *recordingWatches) log() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.calls...)
}

func TestRunner_armsAWatchWhenADeploymentSucceeds(t *testing.T) {
	exec := &recorder{url: answering(t, http.StatusOK)}
	r, store, _ := newRunner(t, webAndAPI(), exec, fakeVariables{})
	watches := &recordingWatches{store: store}
	r.watches = watches
	id, err := runAll(t, r, webAndAPI())
	if err != nil {
		t.Fatal(err)
	}
	// Prepare runs before route cleanup, while the deployment still runs;
	// Arm once it's stored succeeded.
	if got := watches.log(); len(got) != 2 || got[0] != "prepare:running" || got[1] != "arm:succeeded" {
		t.Fatalf("watch calls %v", got)
	}
	if watches.planID != "plan_1" {
		t.Fatalf("prepared plan %q", watches.planID)
	}
	// Arm gets the settled deployment, so the watch starts when it finished.
	if a := watches.armed[0]; a.ID != id || a.Status != StatusSucceeded || a.FinishedAt == nil {
		t.Fatalf("armed %+v", a)
	}
}

func TestRunner_armsNoWatchWhenADeploymentFails(t *testing.T) {
	exec := &recorder{url: answering(t, http.StatusOK)}
	r, store, _ := newRunner(t, webAndAPI(), exec, fakeVariables{})
	watches := &recordingWatches{store: store}
	r.watches = watches
	id, _, err := r.Start(t.Context(), "org_1", "plan_1")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Finish(t.Context(), "org_1", id, "api failed"); err != nil {
		t.Fatal(err)
	}
	if got := watches.log(); len(got) != 0 {
		t.Fatalf("a failed deployment asked the watch %v", got)
	}
}

func TestRunner_noWatchesChangesNothing(t *testing.T) {
	exec := &recorder{url: answering(t, http.StatusOK)}
	r, store, _ := newRunner(t, webAndAPI(), exec, fakeVariables{})
	id, err := runAll(t, r, webAndAPI())
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := store.GetDeployment(t.Context(), "org_1", id); d.Status != StatusSucceeded {
		t.Fatalf("status %s", d.Status)
	}
}

// receiptStore is a memStore that has a finish receipt for every
// deployment, as PgStore does once a finish activity committed.
type receiptStore struct{ *memStore }

func (s receiptStore) StaleFinishActivity(context.Context, string, string) (bool, error) {
	return false, nil
}

func (s receiptStore) FinishedActivity(_ context.Context, _, id, _ string) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d[id].Retries, true, nil
}

// A retried finish activity whose first try committed the deployment
// replays its receipt and returns early; it arms the watch there too, so a
// first try that failed after its commit still gets one.
func TestRunner_armsAWatchOnARetriedFinish(t *testing.T) {
	plan := webAndAPI()
	mem := &memStore{d: map[string]*Deployment{}}
	finished := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	mem.d["dep_1"] = &Deployment{
		ID: "dep_1", OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Environment: "production", PlanID: plan.ID,
		Status: StatusSucceeded, StartedAt: finished.Add(-5 * time.Minute), FinishedAt: &finished,
	}
	plans := &fakePlans{plan: plan, bindings: map[string]*planning.Binding{}}
	watches := &recordingWatches{store: receiptStore{mem}}
	r := NewRunner(RunnerDeps{Store: receiptStore{mem}, Plans: plans, Watches: watches})
	workflows := orchestration.NewDeployments()
	workflows.Bind(nil, r, nil)
	finish := workflows.Options("test", 1).Activities[orchestration.ActivityDeployFinish]
	info := drt.ActivityInfo{Key: durable.Key{Namespace: "loomplane", WorkflowID: plan.ID, RunID: "run"}, CommandID: "finish", Attempt: 2}
	raw, _ := json.Marshal(map[string]string{"org_id": "org_1", "deployment_id": "dep_1", "reason": ""})
	if _, err := finish(t.Context(), info, raw); err != nil {
		t.Fatal(err)
	}
	if got := watches.log(); len(got) != 2 || got[0] != "prepare:succeeded" || got[1] != "arm:succeeded" {
		t.Fatalf("watch calls %v", got)
	}
	if a := watches.armed[0]; a.FinishedAt == nil || !a.FinishedAt.Equal(finished) {
		t.Fatalf("armed %+v", a)
	}
	if len(plans.releases) != 1 {
		t.Fatalf("recorded %d releases", len(plans.releases))
	}

	// A replayed failure arms nothing.
	mem.d["dep_1"].Status = StatusFailed
	watches.calls = nil
	raw, _ = json.Marshal(map[string]string{"org_id": "org_1", "deployment_id": "dep_1", "reason": "it broke"})
	if _, err := finish(t.Context(), info, raw); err != nil {
		t.Fatal(err)
	}
	if got := watches.log(); len(got) != 0 {
		t.Fatalf("a replayed failure asked the watch %v", got)
	}
}

// failingWatches can't prepare: its reads fail.
type failingWatches struct{ armed int }

func (w *failingWatches) Prepare(context.Context, *Deployment, *planning.Plan) (WatchDecision, error) {
	return WatchDecision{}, errors.New("read the manifest: connection reset by 10.0.0.7 with password=hunter2")
}

func (w *failingWatches) Arm(context.Context, *Deployment, WatchDecision) error {
	w.armed++
	return nil
}

// A watch that can't be prepared never fails the finish: it logs the
// error's type and the IDs, never its text, and arms nothing.
func TestRunner_aPrepareErrorDoesntFailTheFinish(t *testing.T) {
	exec := &recorder{url: answering(t, http.StatusOK)}
	r, store, _ := newRunner(t, webAndAPI(), exec, fakeVariables{})
	watches := &failingWatches{}
	var warnings []string
	r.watches = watches
	r.warn = func(msg string) { warnings = append(warnings, msg) }
	id, err := runAll(t, r, webAndAPI())
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := store.GetDeployment(t.Context(), "org_1", id); d.Status != StatusSucceeded {
		t.Fatalf("status %s", d.Status)
	}
	if watches.armed != 0 || len(warnings) != 1 {
		t.Fatalf("armed %d, warnings %v", watches.armed, warnings)
	}
	if w := warnings[0]; !strings.Contains(w, id) || !strings.Contains(w, "org_1") || !strings.Contains(w, "*errors.errorString") || strings.Contains(w, "hunter2") {
		t.Fatalf("warning %q", w)
	}
}

// latestRelease is a release history that answers Latest only.
type latestRelease struct {
	mu     sync.Mutex
	latest *planning.ReleaseRecord
}

func (h *latestRelease) Latest(context.Context, string, string) (*planning.ReleaseRecord, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.latest == nil {
		return nil, planning.ErrNotFound
	}
	r := *h.latest
	return &r, nil
}

func (h *latestRelease) set(r planning.ReleaseRecord) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.latest = &r
}

// guardedStore is a memStore whose environment a Cloud sync or catalog
// deployment holds while held is true.
type guardedStore struct {
	*memStore
	held *bool
}

func (s guardedStore) EnvironmentAvailable(context.Context, string, string, string) (bool, error) {
	return !*s.held, nil
}

// A rollback approved while something held the environment starts only
// once it's free. If a newer release deployed in between, the rollback
// refuses: its deployment fails saying so, nothing deploys, and what runs
// stays the newer release.
func TestRunner_aRollbackRefusesOnceANewerReleaseDeployed(t *testing.T) {
	plan := webAndAPI()
	plan.Kind, plan.DecidedBy = planning.KindRollback, security.SystemSubject(plan.OrgID)
	plan.Rollback = &planning.Rollback{Reason: "errors", Trigger: planning.TriggerWatch, TargetDeploymentID: "dep_bad", TargetPlanID: "plan_bad"}
	exec := &recorder{url: answering(t, http.StatusOK)}
	r, store, plans := newRunner(t, plan, exec, fakeVariables{})
	history := &latestRelease{}
	history.set(planning.ReleaseRecord{DeploymentID: "dep_bad", PlanID: "plan_bad", Kind: planning.KindRelease, Status: StatusSucceeded})
	r.history = history
	held := true
	r.store = guardedStore{store, &held}
	plans.bindings["service:api"] = &planning.Binding{ID: "bind_api", Subject: planning.ServiceSubject, Name: "api", Provider: "cloud-run", Revision: "img:promoted", PlanID: "plan_promo"}

	if _, _, err := r.Start(t.Context(), plan.OrgID, plan.ID); !errors.Is(err, cloudprofiles.ErrBusy) {
		t.Fatalf("held start: %v", err)
	}
	// A person's promotion deploys first.
	history.set(planning.ReleaseRecord{DeploymentID: "dep_promo", PlanID: "plan_promo", Kind: planning.KindPromotion, Status: StatusSucceeded})
	held = false
	id, steps, err := r.Start(t.Context(), plan.OrgID, plan.ID)
	if err != nil || id != "" || steps != nil {
		t.Fatalf("start: %q %v %v", id, steps, err)
	}
	d, err := store.ForPlan(t.Context(), plan.OrgID, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusFailed || d.Error != StaleRollback || d.FinishedAt == nil {
		t.Fatalf("deployment %+v", d)
	}
	for _, s := range d.Steps {
		if s.Status != StepSkipped {
			t.Fatalf("step %+v", s)
		}
	}
	if len(exec.jobs) != 0 || plans.bindings["service:api"].Revision != "img:promoted" {
		t.Fatalf("ran %d jobs, api runs %s", len(exec.jobs), plans.bindings["service:api"].Revision)
	}
	// Starting again, when the refused deployment is the newest, deploys
	// nothing either.
	history.set(planning.ReleaseRecord{DeploymentID: d.ID, PlanID: plan.ID, Kind: planning.KindRollback, Status: StatusFailed})
	if id, _, err = r.Start(t.Context(), plan.OrgID, plan.ID); err != nil || id != "" {
		t.Fatalf("second start: %q %v", id, err)
	}
}

// A rollback whose target is still the newest release starts, and a
// retried start finds its own deployment.
func TestRunner_aFreshRollbackStarts(t *testing.T) {
	plan := webAndAPI()
	plan.Kind = planning.KindRollback
	plan.Rollback = &planning.Rollback{Reason: "errors", Trigger: planning.TriggerManual, TargetDeploymentID: "dep_bad", TargetPlanID: "plan_bad"}
	r, _, _ := newRunner(t, plan, &recorder{url: answering(t, http.StatusOK)}, fakeVariables{})
	history := &latestRelease{}
	history.set(planning.ReleaseRecord{DeploymentID: "dep_bad", PlanID: "plan_bad", Status: StatusSucceeded})
	r.history = history
	id, steps, err := r.Start(t.Context(), plan.OrgID, plan.ID)
	if err != nil || id == "" || len(steps) == 0 {
		t.Fatalf("start: %q %v %v", id, steps, err)
	}
	history.set(planning.ReleaseRecord{DeploymentID: id, PlanID: plan.ID, Kind: planning.KindRollback, Status: StatusRunning})
	again, _, err := r.Start(t.Context(), plan.OrgID, plan.ID)
	if err != nil || again != id {
		t.Fatalf("retried start: %q %v", again, err)
	}
}

type permitAll struct{}

func (permitAll) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

type countingRetrier struct{ retries int }

func (c *countingRetrier) Retry(context.Context, string, string, string, [][]string) error {
	c.retries++
	return nil
}

// A rollback refused as stale can't be retried: it would undo the newer
// release. Plan the rollback again instead.
func TestService_aStaleRollbackCantBeRetried(t *testing.T) {
	store := &memStore{d: map[string]*Deployment{}}
	finished := time.Now()
	store.d["dep_rb"] = &Deployment{ID: "dep_rb", OrgID: "org_1", PlanID: "plan_rb", Status: StatusFailed, Error: StaleRollback, FinishedAt: &finished}
	retrier := &countingRetrier{}
	svc := NewService(store, permitAll{}, retrier, nil)
	_, err := svc.Retry(t.Context(), security.Principal{OrgID: "org_1", UserID: "usr_ada"}, "dep_rb")
	if !errors.Is(err, ErrStaleRollback) || retrier.retries != 0 {
		t.Fatalf("retry %v after %d retries", err, retrier.retries)
	}
	if d, _ := store.GetDeployment(t.Context(), "org_1", "dep_rb"); d.Status != StatusFailed {
		t.Fatalf("status %s", d.Status)
	}
}
