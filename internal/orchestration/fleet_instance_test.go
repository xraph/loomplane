package orchestration

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/store/memory"
)

type fleetInstanceProbe struct{ calls int }

func (p *fleetInstanceProbe) ReconcileFleetInstance(_ context.Context, _ string) (FleetInstanceProgress, error) {
	p.calls++
	return FleetInstanceProgress{Status: "succeeded"}, nil
}

func TestFleetCreateDurableRestart(t *testing.T) {
	store := memory.New()
	flows := NewFleetInstances()
	probe := &fleetInstanceProbe{}
	flows.Bind(probe)
	worker, err := drt.NewWorker(store, flows.Options("fleet-test", 1))
	if err != nil {
		t.Fatal(err)
	}
	flows.BindWorker(worker)
	id := "00000000-0000-4000-8000-000000000001"
	if err = flows.RunFleetInstance(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []durable.TaskKind{durable.TaskWorkflow, durable.TaskActivity} {
		if done, runErr := worker.RunOnce(t.Context(), kind); runErr != nil || !done {
			t.Fatalf("%s: %t %v", kind, done, runErr)
		}
	}
	// Restart after the activity result is durable, before the workflow replays it.
	replacement, err := drt.NewWorker(store, flows.Options("fleet-restarted", 1))
	if err != nil {
		t.Fatal(err)
	}
	flows.BindWorker(replacement)
	if done, runErr := replacement.RunOnce(t.Context(), durable.TaskWorkflow); runErr != nil || !done {
		t.Fatalf("resume: %t %v", done, runErr)
	}
	if err = flows.RunFleetInstance(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	execution, err := store.GetExecution(t.Context(), durable.Key{Namespace: DurableNamespace, WorkflowID: "fleet-instance:" + id, RunID: runID})
	if err != nil || execution.State != durable.StateCompleted || probe.calls != 1 {
		t.Fatalf("restart duplicated work: %+v calls=%d err=%v", execution, probe.calls, err)
	}
}

func TestFleetCreateRequiresRunnerBeforeDispatch(t *testing.T) {
	store := memory.New()
	flows := NewFleetInstances()
	worker, err := drt.NewWorker(store, flows.Options("fleet-unbound", 1))
	if err != nil {
		t.Fatal(err)
	}
	flows.BindWorker(worker)
	id := "00000000-0000-4000-8000-000000000002"
	if err = flows.RunFleetInstance(t.Context(), id); err == nil {
		t.Fatal("unbound runner accepted durable dispatch")
	}
	if _, err = store.GetExecution(t.Context(), durable.Key{Namespace: DurableNamespace, WorkflowID: "fleet-instance:" + id, RunID: runID}); !errors.Is(err, durable.ErrNotFound) {
		t.Fatalf("unavailable runner persisted execution: %v", err)
	}
	flows.Bind(&fleetInstanceProbe{})
	if err = flows.RunFleetInstance(t.Context(), id); err != nil {
		t.Fatalf("configured retry: %v", err)
	}
}
