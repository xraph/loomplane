package orchestration

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
)

type managedFailedProbe struct {
	calls      int
	compatible bool
	review     string
}

func (*managedFailedProbe) RunFailedCleanup(context.Context, string, string, int, func([]byte) error) error {
	return errors.New("SQL execution is blocked in this routing test")
}
func (*managedFailedProbe) RecoverFailedCleanups(context.Context) error { return nil }
func (p *managedFailedProbe) RunManagedFailedCleanup(ctx context.Context, _, _, review string, _ int, _ func([]byte) error) error {
	p.calls++
	p.compatible = IsManagedCleanupContext(ctx)
	p.review = review
	return errors.New("managed execution is blocked in this routing test")
}

func TestManagedFailedCleanupTaskClaims(t *testing.T) {
	r := newRoutedWorkflows(t)
	runs := &memRuns{}
	r.d.BindRunStore(runs)
	probe := &managedFailedProbe{}
	r.d.BindFailedCleanups(probe)
	if err := r.d.StartManagedFailedCleanup(t.Context(), "org", "deployment", "review", 1); err != nil {
		t.Fatal(err)
	}
	route, err := runs.ForPlan(t.Context(), "managed-failed-cleanup:review")
	if err != nil {
		t.Fatal(err)
	}
	if route.Request.BuildID != ManagedCleanupBuild {
		t.Fatal("wrong managed build")
	}
	for _, worker := range []*drt.Worker{r.w3, r.w4, r.w5} {
		if worked, e := worker.RunOnce(t.Context(), durable.TaskWorkflow); e != nil || worked {
			t.Fatalf("old workflow claim: %v %v", worked, e)
		}
	}
	if worked, e := r.w6.RunOnce(t.Context(), durable.TaskWorkflow); e != nil || !worked {
		t.Fatalf("managed workflow claim: %v %v", worked, e)
	}
	for _, worker := range []*drt.Worker{r.w3, r.w4, r.w5} {
		if worked, e := worker.RunOnce(t.Context(), durable.TaskActivity); e != nil || worked {
			t.Fatalf("old activity claim: %v %v", worked, e)
		}
	}
	if worked, e := r.w6.RunOnce(t.Context(), durable.TaskActivity); e != nil || !worked {
		t.Fatalf("managed activity claim: %v %v", worked, e)
	}
	if probe.calls != 1 || !probe.compatible || probe.review != "review" {
		t.Fatalf("managed dispatch: %+v", probe)
	}
	if err = r.d.StartFailedCleanup(t.Context(), "org", "sql-deployment", 1); err != nil {
		t.Fatal(err)
	}
	sql, err := runs.ForPlan(t.Context(), "failed-cleanup:sql-deployment")
	if err != nil || sql.Request.BuildID != DeploymentBuild || sql.Request.WorkflowType != failedCleanupWorkflow {
		t.Fatalf("SQL history changed: %+v %v", sql, err)
	}
}
