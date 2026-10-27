package orchestration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/store/memory"
)

type registrationProbe struct{ infrastructureProbe }

func (p *registrationProbe) ReconcileRegistration(ctx context.Context, org, id string, heartbeat func([]byte) error) error {
	return p.ReconcileInfrastructure(ctx, org, id, heartbeat)
}

func TestRegistrationRunsOnceAsADurableActivity(t *testing.T) {
	store := memory.New()
	dispatch := NewDeployments()
	probe := &registrationProbe{}
	dispatch.BindInfrastructure(probe)
	worker, err := drt.NewWorker(store, dispatch.Options("registration-test", 1))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(infrastructureInput{OrgID: "org", ID: "reg_1"})
	if err != nil {
		t.Fatal(err)
	}
	request := durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: "registration:reg_1:1", RunID: runID}, RequestID: "registration:reg_1:1", WorkflowType: registrationWorkflow, BuildID: DeploymentBuild, Queue: DurableQueue, Input: raw, RetryPolicy: recoveryRetryPolicy()}
	if _, err = worker.StartExecution(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	for _, task := range []durable.TaskKind{durable.TaskWorkflow, durable.TaskActivity, durable.TaskWorkflow} {
		if worked, runErr := worker.RunOnce(t.Context(), task); runErr != nil || !worked {
			t.Fatalf("%s: %v %v", task, worked, runErr)
		}
	}
	execution, err := store.GetExecution(t.Context(), request.Key)
	if err != nil || execution.State != durable.StateCompleted || probe.calls != 1 || probe.org != "org" || probe.id != "reg_1" {
		t.Fatalf("durable registration: %+v %+v %v", execution, probe, err)
	}
	// Starting the same attempt again runs nothing.
	if _, err = worker.StartExecution(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if worked, runErr := worker.RunOnce(t.Context(), durable.TaskWorkflow); runErr != nil || worked {
		t.Fatalf("duplicate durable start: %v %v", worked, runErr)
	}
}
