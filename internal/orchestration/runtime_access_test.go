package orchestration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/store/memory"
)

type accessProbe struct{ clusterProbe }

func (p *accessProbe) ReconcileRuntimeAccessOperation(ctx context.Context, org, id string, h func([]byte) error) error {
	return p.ReconcileClusterOperation(ctx, org, id, h)
}

func TestRuntimeAccessWorkerIsolation(t *testing.T) {
	store := memory.New()
	d := NewDeployments()
	probe := &accessProbe{}
	d.BindClusters(probe)
	worker, err := drt.NewWorker(store, d.RuntimeAccessOptions("access", 1))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(clusterInput{OrgID: "org", ID: "operation"})
	if err != nil {
		t.Fatal(err)
	}
	req := durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: "runtime-access:operation:1", RunID: runID}, RequestID: "runtime-access:operation:1", WorkflowType: RuntimeAccessWorkflow, BuildID: RuntimeAccessBuild, Queue: DurableQueue, Input: raw, RetryPolicy: recoveryRetryPolicy()}
	if _, err = worker.StartExecution(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	old := make([]*drt.Worker, 0, 5)
	for _, options := range []drt.Options{d.Options("old", 1), d.CapacityOptions("capacity", 1), d.CanaryOptions("canary", 1), d.ManagedWorkloadOptions("workload", 1), d.ManagedCleanupOptions("cleanup", 1)} {
		w, e := drt.NewWorker(store, options)
		if e != nil {
			t.Fatal(e)
		}
		old = append(old, w)
		if _, e = w.StartExecution(t.Context(), req); e == nil {
			t.Fatal("old worker accepted new build")
		}
	}
	for _, kind := range []durable.TaskKind{durable.TaskWorkflow, durable.TaskActivity, durable.TaskWorkflow} {
		for _, w := range old {
			if worked, e := w.RunOnce(t.Context(), kind); e != nil || worked {
				t.Fatalf("old worker claimed access: %v %v", worked, e)
			}
		}
		if worked, e := worker.RunOnce(t.Context(), kind); e != nil || !worked {
			t.Fatalf("access worker: %v %v", worked, e)
		}
	}
	if probe.calls != 1 {
		t.Fatal("access activity not executed")
	}
}
