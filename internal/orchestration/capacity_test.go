package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/xraph/dispatch/engine"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/store/memory"
)

type capacityProbe struct {
	paused bool
	calls  map[string]int
}

func (p *capacityProbe) Start(context.Context, string, string) (string, [][]string, error) {
	return "dep", [][]string{{"first", "second"}}, nil
}
func (p *capacityProbe) FetchSource(context.Context, string, string) error {
	p.calls["source"]++
	return nil
}
func (p *capacityProbe) Run(_ context.Context, _, _, a string, _ []byte, _ func([]byte) error) error {
	p.calls[a]++
	if a == "first" {
		p.paused = true
	}
	return nil
}
func (p *capacityProbe) Finish(context.Context, string, string, string) error {
	p.calls["finish"]++
	return nil
}
func (p *capacityProbe) AssessWorkflowCapacity(context.Context, string, string) (CapacityAssessment, error) {
	if p.paused {
		return CapacityAssessment{Status: "paused", Revision: 1}, nil
	}
	return CapacityAssessment{Status: "running"}, nil
}
func TestCapacityWorkflowReplay(t *testing.T) {
	s := memory.New()
	d := NewDeployments()
	p := &capacityProbe{calls: map[string]int{}}
	d.deployer = p
	opts := d.CapacityOptions("capacity-test", 1)
	w, err := drt.NewWorker(s, opts)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(retryInput{OrgID: "org", DeploymentID: "dep", Steps: [][]string{{"first", "second"}}, ReconcileInterval: 30 * time.Second})
	req := durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: "retry", RunID: runID}, RequestID: "start:retry", WorkflowType: WorkflowRetry, BuildID: CapacityBuild, Queue: DurableQueue, Input: raw}
	if _, err = w.StartExecution(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	pump := func() {
		t.Helper()
		for i := 0; i < 40; i++ {
			a, e := w.RunOnce(t.Context(), durable.TaskWorkflow)
			if e != nil {
				t.Fatal(e)
			}
			b, e := w.RunOnce(t.Context(), durable.TaskActivity)
			if e != nil {
				t.Fatal(e)
			}
			if !a && !b {
				return
			}
		}
		t.Fatal("did not quiesce")
	}
	pump()
	if p.calls["first"] != 1 || p.calls["second"] != 0 {
		t.Fatalf("effects before wait: %v", p.calls)
	}
	w, err = drt.NewWorker(s, opts)
	if err != nil {
		t.Fatal(err)
	}
	p.paused = false
	if _, err = w.SignalExecution(t.Context(), durable.SignalRequest{Key: req.Key, RequestID: "capacity:1", BuildID: CapacityBuild, Name: SignalCapacity, Input: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	pump()
	e, err := s.GetExecution(t.Context(), req.Key)
	if err != nil || e.State != durable.StateCompleted || p.calls["first"] != 1 || p.calls["source"] != 1 || p.calls["second"] != 1 || p.calls["finish"] != 1 {
		t.Fatalf("replay: %+v %v %v", e, p.calls, err)
	}
}

type capacityPlanner struct{}

func (capacityPlanner) Plan(context.Context, string, string) (string, error) { return PlanReady, nil }
func (capacityPlanner) ApplyDecision(context.Context, string, string, Decision) (string, error) {
	return PlanApproved, nil
}
func (capacityPlanner) Expire(context.Context, string, string) (string, error) { return "expired", nil }
func (capacityPlanner) Fail(context.Context, string, string, string) error     { return nil }

func pumpCapacity(t *testing.T, w *drt.Worker) {
	t.Helper()
	for range 100 {
		a, e := w.RunOnce(t.Context(), durable.TaskWorkflow)
		if e != nil {
			t.Fatal(e)
		}
		b, e := w.RunOnce(t.Context(), durable.TaskActivity)
		if e != nil {
			t.Fatal(e)
		}
		if !a && !b {
			return
		}
	}
	t.Fatal("worker did not quiesce")
}
func TestCapacityWorkflowV2Compatibility(t *testing.T) {
	s := memory.New()
	d := NewDeployments()
	p := &capacityProbe{calls: map[string]int{}}
	d.deployer = p
	d.planner = capacityPlanner{}
	d.engineOf = func() *engine.Engine { return nil }
	old, err := drt.NewWorker(s, d.Options("old", 1))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(DeploymentInput{OrgID: "org", PlanID: "legacy", Window: time.Hour})
	req := durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: "legacy", RunID: runID}, RequestID: "start:legacy", WorkflowType: WorkflowDeployment, BuildID: DeploymentBuild, Queue: DurableQueue, Input: raw}
	if _, err = old.StartExecution(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	pumpCapacity(t, old)
	newer, err := drt.NewWorker(s, d.CapacityOptions("new", 1))
	if err != nil {
		t.Fatal(err)
	}
	pumpCapacity(t, newer)
	old, err = drt.NewWorker(s, d.Options("old-restarted", 1))
	if err != nil {
		t.Fatal(err)
	}
	dec, _ := json.Marshal(Decision{Approve: true, Revision: "review", By: "user"})
	if _, err = old.SignalExecution(t.Context(), durable.SignalRequest{Key: req.Key, RequestID: "approve", BuildID: DeploymentBuild, Name: SignalDecision, Input: dec}); err != nil {
		t.Fatal(err)
	}
	pumpCapacity(t, old)
	e, err := s.GetExecution(t.Context(), req.Key)
	if err != nil || e.State != durable.StateCompleted || p.calls["first"] != 1 || p.calls["second"] != 1 {
		t.Fatalf("v2 replay: %+v %v %v", e, p.calls, err)
	}
	history, err := s.ReadHistory(t.Context(), req.Key, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range history {
		if bytes.Contains(event.Payload, []byte(ActivityCapacity)) {
			t.Fatal("v2 recorded capacity command")
		}
	}
}
func TestCapacityWorkflowTimerFallback(t *testing.T) {
	s := memory.New()
	d := NewDeployments()
	p := &capacityProbe{paused: true, calls: map[string]int{}}
	d.deployer = p
	w, err := drt.NewWorker(s, d.CapacityOptions("timer", 1))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(retryInput{OrgID: "org", DeploymentID: "dep", Steps: [][]string{{"second"}}, ReconcileInterval: time.Millisecond})
	req := durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: "timer", RunID: runID}, RequestID: "start:timer", WorkflowType: WorkflowRetry, BuildID: CapacityBuild, Queue: DurableQueue, Input: raw}
	if _, err = w.StartExecution(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	pumpCapacity(t, w)
	if len(p.calls) > 0 {
		t.Fatal(p.calls)
	}
	p.paused = false
	w, err = drt.NewWorker(s, d.CapacityOptions("timer-restart", 1))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		worked, e := w.RunOnce(t.Context(), durable.TaskTimer)
		if e != nil {
			t.Fatal(e)
		}
		if worked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timer did not fire")
		}
		time.Sleep(time.Millisecond)
	}
	pumpCapacity(t, w)
	e, err := s.GetExecution(t.Context(), req.Key)
	if err != nil || e.State != durable.StateCompleted || p.calls["second"] != 1 {
		t.Fatalf("lost wake: %+v %v %v", e, p.calls, err)
	}
}

type staleCapacityError struct{}

func (staleCapacityError) Error() string          { return "observed paused fence" }
func (staleCapacityError) CapacityStatus() string { return "paused" }

type resumedCapacityProbe struct {
	capacityProbe
	attempts int
}

func (p *resumedCapacityProbe) Run(ctx context.Context, org, dep, action string, resume []byte, heartbeat func([]byte) error) error {
	p.attempts++
	if p.attempts == 1 {
		return errors.Join(errors.New("first effect was fenced"), staleCapacityError{})
	}
	return p.capacityProbe.Run(ctx, org, dep, action, resume, heartbeat)
}
func TestCapacityWorkflowStalePauseAfterResume(t *testing.T) {
	s := memory.New()
	d := NewDeployments()
	p := &resumedCapacityProbe{capacityProbe: capacityProbe{calls: map[string]int{}}}
	d.deployer = p
	w, err := drt.NewWorker(s, d.CapacityOptions("stale", 1))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(retryInput{OrgID: "org", DeploymentID: "dep", Steps: [][]string{{"second"}}, ReconcileInterval: capacityInterval})
	req := durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: "stale", RunID: runID}, RequestID: "start:stale", WorkflowType: WorkflowRetry, BuildID: CapacityBuild, Queue: DurableQueue, Input: raw}
	if _, err = w.StartExecution(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	pumpCapacity(t, w)
	e, err := s.GetExecution(t.Context(), req.Key)
	if err != nil || e.State != durable.StateCompleted || p.attempts != 2 || p.calls["second"] != 1 {
		t.Fatalf("stale fence failed or slept after newer resume: %+v %v attempts=%d calls=%v", e, err, p.attempts, p.calls)
	}
}

type legacySyncProbe struct{ calls int }

func (p *legacySyncProbe) ReconcileCloudSync(_ context.Context, _, _, _ string, heartbeat func([]byte) error) error {
	p.calls++
	return heartbeat([]byte(`{"accepted":true}`))
}
func (*legacySyncProbe) RecoverCloudSyncs(context.Context) error { return nil }

func TestCapacityWorkflowV2CloudSyncCompatibility(t *testing.T) {
	store := memory.New()
	d := NewDeployments()
	probe := &legacySyncProbe{}
	d.BindCloudSync(probe)
	old, err := drt.NewWorker(store, d.Options("legacy-sync", 1))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cloudSyncInput{OrgID: "org", ProjectID: "project", SyncID: "sync"})
	req := durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: "sync.attempt.1", RunID: runID}, RequestID: "start:sync.attempt.1", WorkflowType: cloudSyncWorkflow, BuildID: DeploymentBuild, Queue: DurableQueue, Input: raw, RetryPolicy: recoveryRetryPolicy()}
	if _, err = old.StartExecution(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if worked, runErr := old.RunOnce(t.Context(), durable.TaskWorkflow); runErr != nil || !worked {
		t.Fatalf("schedule legacy activity: %v %v", worked, runErr)
	}
	newer, err := drt.NewWorker(store, d.CapacityOptions("new-sync", 1))
	if err != nil {
		t.Fatal(err)
	}
	pumpCapacity(t, newer)
	if probe.calls != 0 {
		t.Fatal("v3 worker consumed legacy sync activity")
	}
	old, err = drt.NewWorker(store, d.Options("legacy-sync-restarted", 1))
	if err != nil {
		t.Fatal(err)
	}
	pumpCapacity(t, old)
	if _, err = old.StartExecution(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	pumpCapacity(t, old)
	e, err := store.GetExecution(t.Context(), req.Key)
	if err != nil || e.State != durable.StateCompleted || e.BuildID != DeploymentBuild || probe.calls != 1 {
		t.Fatalf("legacy sync replay: %+v calls=%d %v", e, probe.calls, err)
	}
	history, err := store.ReadHistory(t.Context(), req.Key, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range history {
		if bytes.Contains(event.Payload, []byte(ActivityCapacity)) || bytes.Contains(event.Payload, []byte(cloudSyncPrepareActivity)) {
			t.Fatal("legacy sync recorded a v3 command")
		}
	}
}

func TestCapacityWorkflowLegacyPreparationResult(t *testing.T) {
	for _, raw := range []string{`[{"plan_id":"plan","deployment_id":"dep","steps":[["step"]]}]`, `{"deployments":[{"plan_id":"plan","deployment_id":"dep","steps":[["step"]]}]}`} {
		var result CloudSyncPreparation
		if err := json.Unmarshal([]byte(raw), &result); err != nil || result.Stopped || len(result.Deployments) != 1 || result.Deployments[0].DeploymentID != "dep" {
			t.Fatalf("stored preparation: %+v %v", result, err)
		}
	}
}

func TestCapacityWorkflowV2InfrastructureCompatibility(t *testing.T) {
	store := memory.New()
	d := NewDeployments()
	probe := &infrastructureProbe{}
	d.BindInfrastructure(probe)
	old, err := drt.NewWorker(store, d.Options("old-infra", 1))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(infrastructureInput{OrgID: "org", ID: "operation"})
	req := durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: "infrastructure:operation:1", RunID: runID}, RequestID: "infrastructure:operation:1", WorkflowType: infrastructureWorkflow, BuildID: DeploymentBuild, Queue: DurableQueue, Input: raw}
	if _, err = old.StartExecution(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if worked, e := old.RunOnce(t.Context(), durable.TaskWorkflow); e != nil || !worked {
		t.Fatalf("schedule infrastructure: %v %v", worked, e)
	}
	for _, owner := range []string{"v3-infra", "v3-infra-restarted"} {
		newer, e := drt.NewWorker(store, d.CapacityOptions(owner, 1))
		if e != nil {
			t.Fatal(e)
		}
		pumpCapacity(t, newer)
	}
	if probe.calls != 0 {
		t.Fatal("v3 consumed queued v2 infrastructure")
	}
	old, err = drt.NewWorker(store, d.Options("old-infra-restarted", 1))
	if err != nil {
		t.Fatal(err)
	}
	pumpCapacity(t, old)
	if _, err = old.StartExecution(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	pumpCapacity(t, old)
	execution, err := store.GetExecution(t.Context(), req.Key)
	if err != nil || execution.State != durable.StateCompleted || execution.BuildID != DeploymentBuild || probe.calls != 1 {
		t.Fatalf("infrastructure restart: %+v calls=%d %v", execution, probe.calls, err)
	}
}

func TestCapacityWorkflowSyncAttemptIdentity(t *testing.T) {
	for _, workflow := range []string{"sync.attempt.2", "other.attempt.2", "sync.attempt.0", "sync.attempt.-1", "sync.attempt.invalid", "sync"} {
		info := drt.ActivityInfo{Key: durable.Key{WorkflowID: workflow}}
		ctx, err := capacitySyncActivityContext(t.Context(), info, "sync")
		if workflow != "sync.attempt.2" {
			if err == nil {
				t.Fatalf("invalid phase identity accepted: %s", workflow)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		attempt, bound := CloudSyncAttempt(ctx)
		if !bound || attempt != 2 || !IsCapacityContext(ctx) {
			t.Fatalf("phase authority lost: %d %v", attempt, bound)
		}
	}
}
