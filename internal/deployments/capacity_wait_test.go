package deployments

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

func TestCapacityWaitTransitions(t *testing.T) {
	ctx := context.Background()
	m := &memStore{d: map[string]*Deployment{"dep": {ID: "dep", OrgID: "org", ProjectID: "project", PlanID: "plan", Status: StatusRunning, Retries: 2, Steps: []Step{{ActionID: "done", Status: StepSucceeded, Checkpoint: []byte("receipt")}}}}}
	blocks := []cloudinfra.CapacityBlock{{Reason: "hard_limit", Resource: "cpu", Requested: 1000}, {Reason: "provisioning", Resource: "memory", Retryable: true}}
	d, err := m.SetCapacityWait(ctx, "org", "dep", 0, blocks, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusPaused || d.FinishedAt != nil || d.CapacityWait.Revision != 1 {
		t.Fatalf("bad pause: %+v", d)
	}
	d, err = m.SetCapacityWait(ctx, "org", "dep", 1, []cloudinfra.CapacityBlock{blocks[1], blocks[0]}, time.Now())
	if err != nil || d.CapacityWait.Revision != 1 {
		t.Fatalf("repeat: %+v %v", d, err)
	}
	blocks[0].Limit = 4
	d, err = m.SetCapacityWait(ctx, "org", "dep", 1, blocks, time.Now())
	if err != nil || d.CapacityWait.Revision != 2 {
		t.Fatalf("changed: %+v %v", d, err)
	}
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for _, status := range []string{StatusRunning, StatusCancelled} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := m.TransitionCapacityWait(ctx, "org", "dep", 2, status, time.Now())
			outcomes <- e
		}()
	}
	wg.Wait()
	close(outcomes)
	wins := 0
	for e := range outcomes {
		if e == nil {
			wins++
		} else if !errors.Is(e, ErrCapacityWaitConflict) {
			t.Fatal(e)
		}
	}
	if wins != 1 {
		t.Fatalf("winners %d", wins)
	}
	d, _ = m.GetDeployment(ctx, "org", "dep")
	if d.Retries != 2 || d.Steps[0].Status != StepSucceeded || string(d.Steps[0].Checkpoint) != "receipt" {
		t.Fatalf("lost receipt: %+v", d)
	}
	if (d.Status == StatusCancelled) != (d.FinishedAt != nil) {
		t.Fatalf("terminal time: %+v", d)
	}
	events, err := m.ListCapacityTransitions(ctx, "org", CapacityConsumerWorkflow, 100)
	if err != nil || len(events) != 3 {
		t.Fatalf("events: %+v %v", events, err)
	}
}

func (m *memStore) SetCapacityWait(_ context.Context, org, id string, expected int64, blocks []cloudinfra.CapacityBlock, at time.Time) (*Deployment, error) {
	return m.capacityTransition(org, id, expected, StatusPaused, blocks, at)
}
func (m *memStore) TransitionCapacityWait(_ context.Context, org, id string, expected int64, status string, at time.Time) (*Deployment, error) {
	return m.capacityTransition(org, id, expected, status, nil, at)
}
func (m *memStore) capacityTransition(org, id string, expected int64, status string, blocks []cloudinfra.CapacityBlock, at time.Time) (*Deployment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.d[id]
	if d == nil || d.OrgID != org {
		return nil, ErrNotFound
	}
	event, err := applyCapacityTransition(d, expected, status, blocks, at)
	if err != nil {
		return nil, err
	}
	if event != nil {
		m.capacityEvents = append(m.capacityEvents, *event)
	}
	out := *d
	return &out, nil
}
func (m *memStore) ListCapacityTransitions(_ context.Context, org, consumer string, _ int) ([]CapacityTransition, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []CapacityTransition
	for _, e := range m.capacityEvents {
		if e.OrgID == org && !m.capacityAcks[fmt.Sprintf("%s/%s/%d/%s", org, e.DeploymentID, e.Revision, consumer)] {
			out = append(out, e)
		}
	}
	return out, nil
}
func (m *memStore) AcknowledgeCapacityTransition(_ context.Context, org, id string, revision int64, consumer string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.capacityAcks == nil {
		m.capacityAcks = map[string]bool{}
	}
	m.capacityAcks[fmt.Sprintf("%s/%s/%d/%s", org, id, revision, consumer)] = true
	return nil
}

type waitResolver struct {
	err   error
	calls int
}

func (r *waitResolver) ResolveCapacityIntents(context.Context, string, string) error {
	r.calls++
	return r.err
}

type waitAuth struct {
	project string
	deny    bool
}

func (a *waitAuth) Require(_ context.Context, _ security.Principal, _ security.Permission, project string) error {
	a.project = project
	if a.deny {
		return security.ErrForbidden
	}
	return nil
}

func TestCapacityWaitAssessmentAndPermissions(t *testing.T) {
	ctx := t.Context()
	plan := &planning.Plan{ID: "plan", OrgID: "org", ProjectID: "project"}
	m := &memStore{d: map[string]*Deployment{"dep": {ID: "dep", OrgID: "org", ProjectID: "project", PlanID: "plan", Status: StatusRunning, Steps: []Step{{ActionID: "done", Status: StepSucceeded}, {ActionID: "pending", Status: StepPending}}}}}
	first := cloudinfra.CapacityBlock{Reason: "hard_limit", Resource: "cpu"}
	second := cloudinfra.CapacityBlock{Reason: "expansion_approval", Resource: "memory"}
	resolver := &waitResolver{err: errors.Join(&cloudinfra.CapacityAdmissionError{Block: first}, fmt.Errorf("wrapped: %w", errors.Join(&cloudinfra.CapacityAdmissionError{Block: second}, &cloudinfra.CapacityAdmissionError{Block: first})))}
	r := NewRunner(RunnerDeps{Store: m, Plans: &fakePlans{plan: plan}, Capacity: resolver})
	wait, err := r.AssessCapacity(ctx, "org", "dep")
	if err != nil || len(wait.Blocks) != 2 {
		t.Fatalf("assessment: %+v %v", wait, err)
	}
	repeat, err := r.AssessCapacity(ctx, "org", "dep")
	if err != nil || repeat.Revision != wait.Revision {
		t.Fatalf("repeat: %+v %v", repeat, err)
	}
	if err = r.Finish(ctx, "org", "dep", "capacity wait"); err != nil {
		t.Fatal(err)
	}
	d, _ := m.GetDeployment(ctx, "org", "dep")
	if d.Status != StatusPaused || d.Steps[1].Status != StepPending {
		t.Fatalf("finish changed paused work: %+v", d)
	}
	auth := &waitAuth{deny: true}
	svc := NewService(m, auth, nil, nil)
	p := security.Principal{OrgID: "org", UserID: "user"}
	if _, err = svc.ResumeCapacity(ctx, p, "dep", wait.Revision); !errors.Is(err, security.ErrForbidden) || auth.project != "project" {
		t.Fatalf("permission: %s %v", auth.project, err)
	}
	auth.deny = false
	d, err = svc.ResumeCapacity(ctx, p, "dep", wait.Revision)
	if err != nil || d.Status != StatusRunning || d.Retries != 0 {
		t.Fatalf("resume: %+v %v", d, err)
	}
	wait, err = r.AssessCapacity(ctx, "org", "dep")
	if err != nil || wait.Revision != 3 {
		t.Fatalf("second wait: %+v %v", wait, err)
	}
	if _, err = svc.CancelCapacityWait(ctx, p, "dep", 1); !errors.Is(err, ErrCapacityWaitConflict) {
		t.Fatal(err)
	}
	resolver.err = nil
	if wait, err = r.AssessCapacity(ctx, "org", "dep"); err != nil || wait != nil {
		t.Fatalf("ready: %+v %v", wait, err)
	}
	resolver.err = errors.Join(&cloudinfra.CapacityAdmissionError{Block: first}, errors.New("database failed"))
	if _, err = r.AssessCapacity(ctx, "org", "dep"); err == nil || err.Error() != "database failed" {
		t.Fatalf("lost generic failure: %v", err)
	}
	d, _ = m.GetDeployment(ctx, "org", "dep")
	if d.Status != StatusRunning {
		t.Fatalf("generic failure paused: %+v", d)
	}
}

func TestCapacityWaitLateGuardPreservesSteps(t *testing.T) {
	exec := &recorder{url: answering(t, 200), fail: map[string]error{"web": &cloudinfra.CapacityAdmissionError{Block: cloudinfra.CapacityBlock{Reason: "stale_inventory", Retryable: true}}}}
	r, st, _ := newRunner(t, webAndAPI(), exec, fakeVariables{})
	id, err := runAll(t, r, webAndAPI())
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.GetDeployment(t.Context(), "org_1", id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusPaused || d.FinishedAt != nil || d.CapacityWait == nil || d.Step("service:api").Status != StepSucceeded || d.Step("service:web").Status == StepFailed || d.Step("service:web").Status == StepSkipped {
		t.Fatalf("late guard lost work: %+v", d)
	}
	count := len(exec.jobs)
	if err = r.Run(t.Context(), "org_1", id, "service:web", nil, func([]byte) error { return nil }); !errors.Is(err, ErrCapacityPaused) {
		t.Fatalf("paused run: %v", err)
	}
	if len(exec.jobs) != count {
		t.Fatal("paused run executed")
	}
}
