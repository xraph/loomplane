package platform

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	dispatchext "github.com/xraph/dispatch/extension"
	"github.com/xraph/dispatch/store/memory"

	"github.com/xraph/loomplane/internal/orchestration"
)

var errStoreTimeout = fmt.Errorf("dispatch/postgres: timeout: %w", context.DeadlineExceeded)

// timedOutCommits never lands a task commit, so the worker stops once
// Dispatch's own retries run out.
type timedOutCommits struct{ durable.Store }

func (timedOutCommits) CommitTransition(context.Context, durable.CommitRequest) (durable.Receipt, error) {
	return durable.Receipt{}, errStoreTimeout
}

// timedOutClaims never answers a claim. Dispatch keeps retrying, so the
// worker runs on with nothing to do.
type timedOutClaims struct{ durable.Store }

func (timedOutClaims) ClaimTask(context.Context, durable.ClaimRequest) (*durable.Task, error) {
	return nil, errStoreTimeout
}

// The probe must fail while the durable worker is down, not just log it.
func TestJobsHealthReportsStoppedDurableWorker(t *testing.T) {
	store := timedOutCommits{memory.New()}
	options := drt.Options{Namespace: orchestration.DurableNamespace, Queue: orchestration.DurableQueue,
		BuildID: orchestration.DeploymentBuild, Owner: "health-test", PollInterval: 5 * time.Millisecond,
		Workflows: map[string]drt.WorkflowFunc{"plan": func(*drt.Workflow, []byte) ([]byte, error) { return nil, nil }}}
	worker, err := drt.NewWorker(store, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = worker.StartExecution(t.Context(), durable.StartRequest{Key: durable.Key{Namespace: orchestration.DurableNamespace, WorkflowID: "plan", RunID: "run"},
		RequestID: "plan", WorkflowType: "plan", BuildID: orchestration.DeploymentBuild, Queue: orchestration.DurableQueue}); err != nil {
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
		t.Fatal("the worker survived a commit that never landed")
	}
	err = jobs.Health(t.Context())
	if err == nil || !strings.Contains(err.Error(), "durable workflow worker stopped") {
		t.Fatalf("health: %v", err)
	}
}

// Claims the store doesn't answer leave the worker running, so the probe
// reports them instead.
func TestJobsHealthReportsCapacityClaimFailures(t *testing.T) {
	store := timedOutClaims{memory.New()}
	d := orchestration.NewDeployments()
	options := d.CapacityOptions("capacity-health", 1)
	options.PollInterval = 5 * time.Millisecond
	worker, err := drt.NewWorker(store, options)
	if err != nil {
		t.Fatal(err)
	}
	jobs := &jobsExtension{Extension: dispatchext.New(), durable: orchestration.NewDurableSupervisor(d.Options("legacy-health", 1), nil), capacity: orchestration.NewDurableSupervisor(options, nil)}
	if err = jobs.capacity.Start(worker, store, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	defer func() {
		cancel()
		if runErr := <-done; runErr != nil {
			t.Errorf("capacity worker stopped on failing claims: %v", runErr)
		}
		if e := jobs.capacity.Stop(context.Background()); e != nil {
			t.Error(e)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err = jobs.Health(t.Context())
		if errors.Is(err, drt.ErrClaimUnknown) && strings.Contains(err.Error(), "can't claim tasks") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("capacity health: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}
