package orchestration

import (
	"encoding/json"
	"testing"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
)

func TestManagedProtocolBuildIsolation(t *testing.T) {
	r := newRoutedWorkflows(t)
	req := planRequest("managed", ManagedWorkloadBuild)
	for _, worker := range []*drt.Worker{r.w3, r.w4} {
		if _, err := worker.StartExecution(t.Context(), req); err == nil {
			t.Fatal("old worker started managed build")
		}
	}
	if _, err := r.w5.StartExecution(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []durable.TaskKind{durable.TaskWorkflow, durable.TaskActivity} {
		for _, worker := range []*drt.Worker{r.w3, r.w4} {
			if worked, err := worker.RunOnce(t.Context(), kind); err != nil || worked {
				t.Fatalf("old worker claimed managed task: %v %v", worked, err)
			}
		}
	}
	// Claim only the workflow task so its first activity remains pending.
	if worked, err := r.w5.RunOnce(t.Context(), durable.TaskWorkflow); err != nil || !worked {
		t.Fatalf("managed workflow claim: %v %v", worked, err)
	}
	for _, worker := range []*drt.Worker{r.w3, r.w4} {
		if worked, err := worker.RunOnce(t.Context(), durable.TaskActivity); err != nil || worked {
			t.Fatalf("old worker claimed managed activity: %v %v", worked, err)
		}
	}
	pumpCanary(t, r.w5, nil)
	dec, _ := json.Marshal(Decision{Approve: true, Revision: "r1", By: "usr"})
	if _, err := r.w5.SignalExecution(t.Context(), durable.SignalRequest{Key: req.Key, RequestID: "approve-managed", BuildID: ManagedWorkloadBuild, Name: SignalDecision, Input: dec}); err != nil {
		t.Fatal(err)
	}
	pumpCanary(t, r.w5, nil)
	dep, err := r.d.boundDeployer()
	if err != nil {
		t.Fatal(err)
	}
	probe := dep.(*canaryProbe)
	if len(probe.started) != 1 || !IsManagedWorkloadContext(probe.started[0]) {
		t.Fatal("managed activity lost protocol context")
	}
	e, err := r.store.GetExecution(t.Context(), req.Key)
	if err != nil || e.State != durable.StateCompleted {
		t.Fatalf("managed workflow: %+v %v", e, err)
	}
}
