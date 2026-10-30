package orchestration

import (
	"testing"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
)

func TestManagedCleanupBuildIsolation(t *testing.T) {
	r := newRoutedWorkflows(t)
	req := planRequest("cleanup", ManagedCleanupBuild)
	for _, worker := range []*drt.Worker{r.w3, r.w4, r.w5} {
		if _, err := worker.StartExecution(t.Context(), req); err == nil {
			t.Fatal("old worker started cleanup")
		}
	}
	if _, err := r.w6.StartExecution(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	for _, worker := range []*drt.Worker{r.w3, r.w4, r.w5} {
		if worked, err := worker.RunOnce(t.Context(), durable.TaskWorkflow); err != nil || worked {
			t.Fatalf("old workflow claim: %v %v", worked, err)
		}
	}
	if worked, err := r.w6.RunOnce(t.Context(), durable.TaskWorkflow); err != nil || !worked {
		t.Fatalf("cleanup workflow claim: %v %v", worked, err)
	}
	for _, worker := range []*drt.Worker{r.w3, r.w4, r.w5} {
		if worked, err := worker.RunOnce(t.Context(), durable.TaskActivity); err != nil || worked {
			t.Fatalf("old activity claim: %v %v", worked, err)
		}
	}
	if !IsManagedWorkloadContext(ManagedCleanupContext(t.Context())) || !IsCapacityContext(ManagedCleanupContext(t.Context())) {
		t.Fatal("cleanup context lost application capabilities")
	}
}
