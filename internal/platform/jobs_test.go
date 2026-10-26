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
	// The capacity build is one of the jobs' build workers.
	capacity := orchestration.NewDurableSupervisor(options, nil)
	jobs := &jobsExtension{Extension: dispatchext.New(), durable: orchestration.NewDurableSupervisor(d.Options("legacy-health", 1), nil),
		builds: []*buildWorker{{options: options, supervisor: capacity}}}
	if err = capacity.Start(worker, store, nil); err != nil {
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
		if e := capacity.Stop(context.Background()); e != nil {
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

// failingBuild fails every claim for one build, as a lost connection
// would, and serves the rest.
type failingBuild struct {
	durable.Store
	build string
}

func (s failingBuild) ClaimTask(ctx context.Context, r durable.ClaimRequest) (*durable.Task, error) {
	if r.BuildID == s.build {
		return nil, errStoreTimeout
	}
	return s.Store.ClaimTask(ctx, r)
}

// Every build runs a worker of its own, and health names the build whose
// claims fail without the others' failing too.
func TestJobsRunsEveryBuildWorker(t *testing.T) {
	for _, failing := range []string{orchestration.CapacityBuild, orchestration.CanaryBuild} {
		t.Run(failing+" fails", func(t *testing.T) {
			d := orchestration.NewDeployments()
			jobs := &jobsExtension{Extension: dispatchext.New(), durable: orchestration.NewDurableSupervisor(d.Options("legacy", 1), nil)}
			workers := map[string]*drt.Worker{}
			capacity, canary := d.CapacityOptions("v3", 1), d.CanaryOptions("v4", 1)
			capacity.PollInterval, canary.PollInterval = 5*time.Millisecond, 5*time.Millisecond
			jobs.WithCapacity(capacity, func(w *drt.Worker) {
				workers[orchestration.CapacityBuild] = w
				d.BindCapacityWorker(w)
			})
			jobs.WithBuild(canary, func(w *drt.Worker) {
				workers[orchestration.CanaryBuild] = w
				d.BindWorker(orchestration.CanaryBuild, w)
			})
			if err := jobs.startBuilds(failingBuild{Store: memory.New(), build: failing}, nil); err != nil {
				t.Fatal(err)
			}
			if len(workers) != 2 {
				t.Fatalf("bound %v", workers)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				err := jobs.Health(t.Context())
				if errors.Is(err, drt.ErrClaimUnknown) && strings.HasPrefix(err.Error(), failing+": ") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("health: %v", err)
				}
				time.Sleep(time.Millisecond)
			}
			for build, w := range workers {
				if status := w.Status(); status.State == drt.WorkerFailed || (build != failing && status.ClaimFailures > 0) {
					t.Fatalf("%s: %+v", build, status)
				}
			}
			if err := jobs.stopBuilds(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, b := range jobs.builds {
				select {
				case <-b.done:
				default:
					t.Fatalf("%s still runs", b.options.BuildID)
				}
			}
		})
	}
}
