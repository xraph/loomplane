package deployments

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"

	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/planning"
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
