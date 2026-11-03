package deployments

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

func (m *memStore) CancelDeployment(ctx context.Context, org, id string, at time.Time) (*Deployment, error) {
	m.mu.Lock()
	d := m.d[id]
	if d == nil || d.OrgID != org {
		m.mu.Unlock()
		return nil, ErrNotFound
	}
	switch d.Status {
	case StatusPaused:
		m.mu.Unlock()
		return m.StopDeployment(ctx, org, id, at)
	case StatusCancelled:
		defer m.mu.Unlock()
		return copyDeployment(d), nil
	}
	defer m.mu.Unlock()
	if err := requestCancel(d, at); err != nil {
		return nil, err
	}
	return copyDeployment(d), nil
}

func (m *memStore) ClaimStep(_ context.Context, org, id, actionID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.d[id]
	if d == nil || d.OrgID != org {
		return false, ErrNotFound
	}
	stop, _, err := claimStep(d, actionID)
	return stop, err
}

func (m *memStore) StopDeployment(_ context.Context, org, id string, at time.Time) (*Deployment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.d[id]
	if d == nil || d.OrgID != org {
		return nil, ErrNotFound
	}
	event, err := stopDeployment(d, at)
	if err != nil {
		return nil, err
	}
	if event != nil {
		m.capacityEvents = append(m.capacityEvents, *event)
	}
	return copyDeployment(d), nil
}

type cancelAudit struct {
	mu     sync.Mutex
	events []projects.AuditEvent
}

func (a *cancelAudit) Record(_ context.Context, e projects.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

func runningDeployment() *memStore {
	return &memStore{d: map[string]*Deployment{"dep": {
		ID: "dep", OrgID: "org", ProjectID: "project", PlanID: "plan", Environment: "production", Status: StatusRunning,
		Steps: []Step{{ActionID: "done", Status: StepSucceeded}, {ActionID: "web", Status: StepDeploying}, {ActionID: "api", Status: StepPending}},
	}}}
}

func TestCancelWaitsForTheActionThatsRunning(t *testing.T) {
	ctx := t.Context()
	m := runningDeployment()
	auth := &waitAuth{deny: true}
	audit := &cancelAudit{}
	svc := NewService(m, auth, nil, audit)
	p := security.Principal{OrgID: "org", UserID: "user"}

	if _, err := svc.Cancel(ctx, p, "dep"); !errors.Is(err, security.ErrForbidden) || auth.project != "project" {
		t.Fatalf("permission: %s %v", auth.project, err)
	}
	auth.deny = false
	d, err := svc.Cancel(ctx, p, "dep")
	if err != nil || d.Status != StatusRunning || d.CancelRequestedAt == nil || d.FinishedAt != nil {
		t.Fatalf("request: %+v %v", d, err)
	}
	// Nothing is fenced yet: the running action carries on as it was.
	if len(m.capacityEvents) != 0 || d.Steps[1].Status != StepDeploying {
		t.Fatalf("cancelled at once: %+v %+v", d, m.capacityEvents)
	}
	if len(audit.events) != 1 || audit.events[0].Action != "deployment.cancel" {
		t.Fatalf("audit: %+v", audit.events)
	}
	again, err := svc.Cancel(ctx, p, "dep")
	if err != nil || !again.CancelRequestedAt.Equal(*d.CancelRequestedAt) {
		t.Fatalf("repeat moved the request: %+v %v", again, err)
	}

	// A retry of the action that was running when the request landed still
	// runs: it resumes where its last attempt was.
	r := NewRunner(RunnerDeps{Store: m, Plans: &fakePlans{plan: &planning.Plan{ID: "plan", OrgID: "org", ProjectID: "project"}}})
	if err = r.Run(ctx, "org", "dep", "web", []byte("checkpoint"), func([]byte) error { return nil }); errors.Is(err, ErrCapacityCancelled) {
		t.Fatal("the running action was refused")
	}
	if cur, _ := m.GetDeployment(ctx, "org", "dep"); cur.Status != StatusRunning {
		t.Fatalf("the running action stopped it: %+v", cur)
	}

	// The next action doesn't start, and the deployment is cancelled then.
	if err = r.Run(ctx, "org", "dep", "api", nil, func([]byte) error { return nil }); !errors.Is(err, ErrCapacityCancelled) {
		t.Fatalf("next action: %v", err)
	}
	d, _ = m.GetDeployment(ctx, "org", "dep")
	if d.Status != StatusCancelled || d.FinishedAt == nil || d.Steps[2].Status != StepPending || d.Steps[2].StartedAt != nil {
		t.Fatalf("stopped: %+v", d)
	}
	if len(m.capacityEvents) != 1 || m.capacityEvents[0].Status != StatusCancelled {
		t.Fatalf("event: %+v", m.capacityEvents)
	}
	if err = r.Finish(ctx, "org", "dep", "deployment capacity state: cancelled"); err != nil {
		t.Fatal(err)
	}
	if d, _ = m.GetDeployment(ctx, "org", "dep"); d.Status != StatusCancelled || d.Error != "" {
		t.Fatalf("the workflow's Finish settled it again: %+v", d)
	}
	if _, err = svc.Retry(ctx, p, "dep"); !errors.Is(err, ErrNotFailed) {
		t.Fatalf("retried a cancelled deployment: %v", err)
	}
}

func TestCancelThatComesTooLateLetsItFinish(t *testing.T) {
	ctx := t.Context()
	m := runningDeployment()
	m.d["dep"].Steps[2].Status = StepBuilding
	if _, err := NewService(m, &waitAuth{}, nil, nil).Cancel(ctx, security.Principal{OrgID: "org"}, "dep"); !errors.Is(err, ErrNothingToCancel) {
		t.Fatalf("every step started: %v", err)
	}
	if m.d["dep"].CancelRequestedAt != nil {
		t.Fatal("recorded a request nothing would carry out")
	}
}

func TestARetryForgetsAnEarlierCancelRequest(t *testing.T) {
	ctx := t.Context()
	m := runningDeployment()
	if _, err := m.CancelDeployment(ctx, "org", "dep", time.Now()); err != nil {
		t.Fatal(err)
	}
	// The running action failed before the next one started.
	m.d["dep"].Status, m.d["dep"].Steps[1].Status = StatusFailed, StepFailed
	if ok, err := m.Reopen(ctx, "org", "dep"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if m.d["dep"].CancelRequestedAt != nil {
		t.Fatal("the retry would stop before its first action")
	}
}

func TestCancelRefusesWhatItCannotStop(t *testing.T) {
	ctx := t.Context()
	p := security.Principal{OrgID: "org", UserID: "user"}
	for _, status := range []string{StatusSucceeded, StatusFailed} {
		t.Run(status, func(t *testing.T) {
			m := &memStore{d: map[string]*Deployment{"dep": {ID: "dep", OrgID: "org", ProjectID: "project", PlanID: "plan", Status: status}}}
			if _, err := NewService(m, &waitAuth{}, nil, nil).Cancel(ctx, p, "dep"); !errors.Is(err, ErrNotRunning) {
				t.Fatalf("got %v", err)
			}
			if m.d["dep"].Status != status || len(m.capacityEvents) != 0 {
				t.Fatalf("changed: %+v", m.d["dep"])
			}
		})
	}
}

func TestCancelStopsAPausedDeploymentAtOnce(t *testing.T) {
	ctx := t.Context()
	at := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	m := &memStore{d: map[string]*Deployment{"dep": {ID: "dep", OrgID: "org", ProjectID: "project", Status: StatusPaused, CapacityRevision: 4, CapacityWait: &CapacityWait{Revision: 4}}}}
	svc := NewService(m, &waitAuth{}, nil, nil)
	svc.now = func() time.Time { return at }
	d, err := svc.Cancel(ctx, security.Principal{OrgID: "org"}, "dep")
	if err != nil || d.Status != StatusCancelled || d.CapacityWait != nil || d.FinishedAt == nil || !d.FinishedAt.Equal(at) {
		t.Fatalf("deployment: %+v %v", d, err)
	}
	if len(m.capacityEvents) != 1 || m.capacityEvents[0].Revision != 5 || m.capacityEvents[0].Wait == nil {
		t.Fatalf("event: %+v", m.capacityEvents)
	}
	// Stopping one nobody asked to cancel is refused.
	running := &Deployment{Status: StatusRunning}
	if _, err = stopDeployment(running, at); !errors.Is(err, ErrCapacityWaitConflict) || running.Status != StatusRunning {
		t.Fatalf("stopped without a request: %+v %v", running, err)
	}
}

func TestAClaimedStepRunsOnAfterACancelRequest(t *testing.T) {
	ctx := t.Context()
	m := runningDeployment()
	r := NewRunner(RunnerDeps{Store: m, Plans: &fakePlans{plan: &planning.Plan{ID: "plan", OrgID: "org", ProjectID: "project"}}})
	// The next action starts and claims its step before any effect.
	_ = r.Run(ctx, "org", "dep", "api", nil, func([]byte) error { return nil })
	if !m.d["dep"].Steps[2].Claimed {
		t.Fatal("the action didn't claim its step")
	}
	// A cancel after that has nothing left to stop before.
	if _, err := m.CancelDeployment(ctx, "org", "dep", time.Now()); !errors.Is(err, ErrNothingToCancel) {
		t.Fatalf("accepted a cancel nothing would carry out: %v", err)
	}
	// A request that's on record anyway, from before a worker died mid
	// action, doesn't stop the retry of an action that claimed its step.
	requested := time.Now()
	m.d["dep"].CancelRequestedAt = &requested
	if err := r.Run(ctx, "org", "dep", "api", nil, func([]byte) error { return nil }); errors.Is(err, ErrCapacityCancelled) {
		t.Fatal("the retry of a claimed action was stopped")
	}
	if m.d["dep"].Status != StatusRunning {
		t.Fatalf("stopped: %+v", m.d["dep"])
	}
}

func TestACapacityBlockStopsARequestedCancelInsteadOfPausing(t *testing.T) {
	ctx := t.Context()
	m := runningDeployment()
	requested := time.Now()
	m.d["dep"].CancelRequestedAt = &requested
	resolver := &waitResolver{err: &cloudinfra.CapacityAdmissionError{Block: cloudinfra.CapacityBlock{Reason: "hard_limit", Resource: "cpu"}}}
	r := NewRunner(RunnerDeps{Store: m, Plans: &fakePlans{plan: &planning.Plan{ID: "plan", OrgID: "org", ProjectID: "project"}}, Capacity: resolver})
	if _, err := r.AssessCapacity(ctx, "org", "dep"); !errors.Is(err, ErrCapacityCancelled) {
		t.Fatalf("assessment: %v", err)
	}
	if d := m.d["dep"]; d.Status != StatusCancelled || d.CapacityWait != nil {
		t.Fatalf("paused a cancelled deployment: %+v", d)
	}
	state, err := r.AssessWorkflowCapacity(ctx, "org", "dep")
	if err != nil || state.Status != StatusCancelled {
		t.Fatalf("workflow state: %+v %v", state, err)
	}
}
