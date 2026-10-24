package orchestration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/store/memory"
)

// timeoutClaims times out the first workflow claim the way Postgres did
// when its host was overloaded. With commit set, the claim takes the task
// first, so only the reply is lost.
type timeoutClaims struct {
	durable.Store
	commit bool
	fired  atomic.Bool
}

func (s *timeoutClaims) ClaimTask(ctx context.Context, r durable.ClaimRequest) (*durable.Task, error) {
	if r.Kind != durable.TaskWorkflow || s.fired.Load() {
		return s.Store.ClaimTask(ctx, r)
	}
	s.fired.Store(true)
	if s.commit {
		if _, err := s.Store.ClaimTask(ctx, r); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("dispatch/postgres: claim execution task: timeout: %w", context.DeadlineExceeded)
}

func TestDurableSupervisorRecoversFromClaimTimeout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		commit bool
		build  string
	}{{"v2 claim rolled back", false, DeploymentBuild}, {"v2 claim committed, reply lost", true, DeploymentBuild}, {"v3 claim rolled back", false, CapacityBuild}, {"v3 claim committed, reply lost", true, CapacityBuild}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			base := memory.New()
			store := &timeoutClaims{Store: base, commit: tc.commit}
			var mu sync.Mutex
			ran := map[string]int{}
			options := drt.Options{Namespace: DurableNamespace, Queue: DurableQueue, BuildID: tc.build, Owner: "supervisor-test",
				Concurrency: 1, LeaseDuration: 300 * time.Millisecond, PollInterval: 5 * time.Millisecond,
				Workflows: map[string]drt.WorkflowFunc{"plan": func(w *drt.Workflow, in []byte) ([]byte, error) {
					return w.Activity("step", "step", "", in).Get()
				}},
				Activities: map[string]drt.ActivityFunc{"step": func(_ context.Context, info drt.ActivityInfo, in []byte) ([]byte, error) {
					mu.Lock()
					defer mu.Unlock()
					ran[info.Key.WorkflowID]++
					return in, nil
				}},
			}
			initial, err := drt.NewWorker(store, options)
			if err != nil {
				t.Fatal(err)
			}
			start := func(id string) durable.Key {
				t.Helper()
				key := durable.Key{Namespace: DurableNamespace, WorkflowID: id, RunID: runID}
				// Starts go through the engine's worker even after it fails.
				if _, startErr := initial.StartExecution(ctx, durable.StartRequest{Key: key, RequestID: id, WorkflowType: "plan",
					BuildID: tc.build, Queue: DurableQueue, Input: []byte(id)}); startErr != nil {
					t.Fatal(startErr)
				}
				return key
			}
			before := start("before")

			var warnings atomic.Int32
			sup := NewDurableSupervisor(options, func(string, error) { warnings.Add(1) })
			sup.check, sup.minBackoff = 5*time.Millisecond, 200*time.Millisecond
			if err = sup.Start(initial, store, nil); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if stopErr := sup.Stop(context.Background()); stopErr != nil {
					t.Error(stopErr)
				}
			})
			if err = sup.Health(); err != nil {
				t.Fatalf("healthy before any failure: %v", err)
			}

			// The engine runs its own worker; the timeout ends it.
			if runErr := initial.Run(ctx); !errors.Is(runErr, context.DeadlineExceeded) {
				t.Fatalf("engine worker: %v", runErr)
			}
			if err = sup.Health(); err == nil {
				t.Fatal("health ignored the stopped worker")
			}

			after := start("after")
			for _, key := range []durable.Key{before, after} {
				waitCompleted(t, base, key)
			}
			mu.Lock()
			if ran["before"] != 1 || ran["after"] != 1 {
				t.Errorf("each run's step should run once: %v", ran)
			}
			mu.Unlock()
			if err = sup.Health(); err != nil {
				t.Errorf("still unhealthy after the replacement ran: %v", err)
			}
			if warnings.Load() != 1 {
				t.Errorf("expected one restart, got %d", warnings.Load())
			}
		})
	}
}

func waitCompleted(t *testing.T, store durable.Store, key durable.Key) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		execution, err := store.GetExecution(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		if execution.State == durable.StateCompleted {
			if string(execution.Output) != key.WorkflowID {
				t.Fatalf("%s output: %q", key.WorkflowID, execution.Output)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never completed: %s", key.WorkflowID, execution.State)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDurableSupervisorStaysUnhealthyDuringBackoff(t *testing.T) {
	store := &timeoutClaims{Store: memory.New()}
	options := drt.Options{Namespace: DurableNamespace, Queue: DurableQueue, BuildID: DeploymentBuild, Owner: "supervisor-test",
		PollInterval: 5 * time.Millisecond, Workflows: map[string]drt.WorkflowFunc{"plan": func(*drt.Workflow, []byte) ([]byte, error) { return nil, nil }}}
	initial, err := drt.NewWorker(store, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = initial.StartExecution(t.Context(), durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: "plan", RunID: runID},
		RequestID: "plan", WorkflowType: "plan", BuildID: DeploymentBuild, Queue: DurableQueue}); err != nil {
		t.Fatal(err)
	}
	sup := NewDurableSupervisor(options, nil)
	sup.check, sup.minBackoff = 5*time.Millisecond, time.Hour
	if err = sup.Start(initial, store, nil); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if stopErr := sup.Stop(context.Background()); stopErr != nil {
			t.Error(stopErr)
		}
	}()
	_ = initial.Run(t.Context()) // fails on the timeout
	// A replacement waits out its backoff, so the probe stays unhealthy.
	time.Sleep(50 * time.Millisecond)
	if err = sup.Health(); err == nil {
		t.Fatal("health ignored the stopped worker during backoff")
	}
	// Stop doesn't wait for the backoff.
	stopped, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = sup.Stop(stopped); err != nil {
		t.Fatalf("stop during backoff: %v", err)
	}
}
