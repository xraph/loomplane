package platform

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	dispatchext "github.com/xraph/dispatch/extension"
	"github.com/xraph/dispatch/store/memory"

	"github.com/xraph/loomplane/internal/orchestration"
)

type timedOutClaims struct{ durable.Store }

func (timedOutClaims) ClaimTask(context.Context, durable.ClaimRequest) (*durable.Task, error) {
	return nil, fmt.Errorf("dispatch/postgres: claim execution task: timeout: %w", context.DeadlineExceeded)
}

// The probe must fail while the durable worker is down, not just log it.
func TestJobsHealthReportsStoppedDurableWorker(t *testing.T) {
	store := timedOutClaims{memory.New()}
	options := drt.Options{Namespace: orchestration.DurableNamespace, Queue: orchestration.DurableQueue,
		BuildID: orchestration.DeploymentBuild, Owner: "health-test",
		Workflows: map[string]drt.WorkflowFunc{"plan": func(*drt.Workflow, []byte) ([]byte, error) { return nil, nil }}}
	worker, err := drt.NewWorker(store, options)
	if err != nil {
		t.Fatal(err)
	}
	jobs := &jobsExtension{Extension: dispatchext.New(), durable: orchestration.NewDurableSupervisor(options, nil)}
	if err = jobs.durable.Start(worker, store, nil); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if stopErr := jobs.durable.Stop(context.Background()); stopErr != nil {
			t.Error(stopErr)
		}
	}()
	if err = worker.Run(t.Context()); err == nil {
		t.Fatal("the worker survived a claim timeout on its own")
	}
	err = jobs.Health(t.Context())
	if err == nil || !strings.Contains(err.Error(), "durable workflow worker stopped") {
		t.Fatalf("health: %v", err)
	}
}

func TestJobsHealthReportsStoppedCapacityWorker(t *testing.T) {
	store := timedOutClaims{memory.New()}
	d := orchestration.NewDeployments()
	options := d.CapacityOptions("capacity-health", 1)
	worker, err := drt.NewWorker(store, options)
	if err != nil {
		t.Fatal(err)
	}
	jobs := &jobsExtension{Extension: dispatchext.New(), durable: orchestration.NewDurableSupervisor(d.Options("legacy-health", 1), nil), capacity: orchestration.NewDurableSupervisor(options, nil)}
	if err = jobs.capacity.Start(worker, store, nil); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e := jobs.capacity.Stop(context.Background()); e != nil {
			t.Error(e)
		}
	}()
	if err = worker.Run(t.Context()); err == nil {
		t.Fatal("capacity worker survived failing store")
	}
	if err = jobs.Health(t.Context()); err == nil || !strings.Contains(err.Error(), "durable workflow worker stopped") {
		t.Fatalf("capacity health: %v", err)
	}
}
