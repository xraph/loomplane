package orchestration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/store/memory"
)

type clusterProbe struct {
	calls   int
	org, id string
}

func (p *clusterProbe) ReconcileClusterOperation(_ context.Context, org, id string, heartbeat func([]byte) error) error {
	p.calls++
	p.org = org
	p.id = id
	return heartbeat([]byte(`{"attempted":true}`))
}
func (*clusterProbe) RecoverClusterOperations(context.Context) error { return nil }
func TestClusterUsesDurableActivityAcrossWorkerRestart(t *testing.T) {
	store := memory.New()
	dispatch := NewDeployments()
	probe := &clusterProbe{}
	dispatch.BindClusters(probe)
	options := dispatch.Options("infra-test", 1)
	worker, err := drt.NewWorker(store, options)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(clusterInput{OrgID: "org", ID: "operation"})
	if err != nil {
		t.Fatal(err)
	}
	request := durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: "cluster:operation:1", RunID: runID}, RequestID: "cluster:operation:1", WorkflowType: clusterWorkflow, BuildID: DeploymentBuild, Queue: DurableQueue, Input: raw, RetryPolicy: recoveryRetryPolicy()}
	if _, err = worker.StartExecution(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if worked, runErr := worker.RunOnce(t.Context(), durable.TaskWorkflow); runErr != nil || !worked {
		t.Fatalf("schedule: %v %v", worked, runErr)
	}
	// Replace the worker after Dispatch has committed the activity task.
	worker, err = drt.NewWorker(store, options)
	if err != nil {
		t.Fatal(err)
	}
	if worked, runErr := worker.RunOnce(t.Context(), durable.TaskActivity); runErr != nil || !worked {
		t.Fatalf("activity: %v %v", worked, runErr)
	}
	if worked, runErr := worker.RunOnce(t.Context(), durable.TaskWorkflow); runErr != nil || !worked {
		t.Fatalf("completion: %v %v", worked, runErr)
	}
	execution, err := store.GetExecution(t.Context(), request.Key)
	if err != nil || execution.State != durable.StateCompleted || probe.calls != 1 || probe.org != "org" || probe.id != "operation" {
		t.Fatalf("durable cluster: %+v %+v %v", execution, probe, err)
	}
	if _, err = worker.StartExecution(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if worked, runErr := worker.RunOnce(t.Context(), durable.TaskWorkflow); runErr != nil || worked {
		t.Fatalf("duplicate durable start: %v %v", worked, runErr)
	}
}
