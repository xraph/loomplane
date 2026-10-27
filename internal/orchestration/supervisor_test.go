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

// timeoutCommits times out the first three task commits, enough to use up
// Dispatch's own retries, the way Postgres does when its host is
// overloaded. With commit set, the first one lands, so only its reply is
// lost; Dispatch then reads back the newer revision and carries on, so only
// a commit that never landed stops the worker.
type timeoutCommits struct {
	durable.Store
	commit   bool
	failures atomic.Int64
}

func newTimeoutCommits(store durable.Store, commit bool) *timeoutCommits {
	s := &timeoutCommits{Store: store, commit: commit}
	s.failures.Store(3)
	return s
}

func (s *timeoutCommits) CommitTransition(ctx context.Context, r durable.CommitRequest) (durable.Receipt, error) {
	n := s.failures.Add(-1)
	if n < 0 {
		return s.Store.CommitTransition(ctx, r)
	}
	if s.commit && n == 2 {
		if _, err := s.Store.CommitTransition(ctx, r); err != nil {
			return durable.Receipt{}, err
		}
	}
	return durable.Receipt{}, fmt.Errorf("dispatch/postgres: commit transition: timeout: %w", context.DeadlineExceeded)
}

// unansweredClaims times out every claim while down is set.
type unansweredClaims struct {
	durable.Store
	down atomic.Bool
}

func (s *unansweredClaims) ClaimTask(ctx context.Context, r durable.ClaimRequest) (*durable.Task, error) {
	if s.down.Load() {
		return nil, fmt.Errorf("dispatch/postgres: claim execution task: timeout: %w", context.DeadlineExceeded)
	}
	return s.Store.ClaimTask(ctx, r)
}

func TestDurableSupervisorRecoversFromCommitTimeout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		commit bool
		build  string
	}{{"v2 commit rolled back", false, DeploymentBuild}, {"v2 commit landed, reply lost", true, DeploymentBuild}, {"v3 commit rolled back", false, CapacityBuild}, {"v3 commit landed, reply lost", true, CapacityBuild}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			base := memory.New()
			store := newTimeoutCommits(base, tc.commit)
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

			// The engine runs its own worker; a commit that never landed
			// ends it once Dispatch's retries run out.
			runCtx, cancel := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { done <- initial.Run(runCtx) }()
			t.Cleanup(func() { cancel(); <-done })
			if !tc.commit {
				select {
				case runErr := <-done:
					done <- runErr
					if !errors.Is(runErr, context.DeadlineExceeded) {
						t.Fatalf("engine worker: %v", runErr)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("the commit timeout didn't stop the engine worker")
				}
				if err = sup.Health(); err == nil {
					t.Fatal("health ignored the stopped worker")
				}
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
			if restarts := map[bool]int32{false: 1, true: 0}[tc.commit]; warnings.Load() != restarts {
				t.Errorf("expected %d restarts, got %d", restarts, warnings.Load())
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
	store := newTimeoutCommits(memory.New(), false)
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

// Dispatch retries claims the store doesn't answer, so the worker keeps
// running: the probe reports the failing claims, nothing restarts, and the
// work runs once the store answers again.
func TestDurableSupervisorReportsFailingClaims(t *testing.T) {
	base := memory.New()
	store := &unansweredClaims{Store: base}
	store.down.Store(true)
	options := drt.Options{Namespace: DurableNamespace, Queue: DurableQueue, BuildID: DeploymentBuild, Owner: "supervisor-test",
		PollInterval: 5 * time.Millisecond, Workflows: map[string]drt.WorkflowFunc{"plan": func(_ *drt.Workflow, in []byte) ([]byte, error) { return in, nil }}}
	initial, err := drt.NewWorker(store, options)
	if err != nil {
		t.Fatal(err)
	}
	key := durable.Key{Namespace: DurableNamespace, WorkflowID: "plan", RunID: runID}
	if _, err = initial.StartExecution(t.Context(), durable.StartRequest{Key: key, RequestID: "plan", WorkflowType: "plan",
		BuildID: DeploymentBuild, Queue: DurableQueue, Input: []byte("plan")}); err != nil {
		t.Fatal(err)
	}
	var warnings atomic.Int32
	sup := NewDurableSupervisor(options, func(string, error) { warnings.Add(1) })
	sup.check, sup.minBackoff = 5*time.Millisecond, 10*time.Millisecond
	if err = sup.Start(initial, store, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- initial.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		if stopErr := sup.Stop(context.Background()); stopErr != nil {
			t.Error(stopErr)
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for !errors.Is(sup.Health(), drt.ErrClaimUnknown) {
		if time.Now().After(deadline) {
			t.Fatalf("health ignored failing claims: %v", sup.Health())
		}
		time.Sleep(time.Millisecond)
	}
	store.down.Store(false)
	waitCompleted(t, base, key)
	deadline = time.Now().Add(5 * time.Second)
	for sup.Health() != nil {
		if time.Now().After(deadline) {
			t.Fatalf("still unhealthy after claims recovered: %v", sup.Health())
		}
		time.Sleep(time.Millisecond)
	}
	if warnings.Load() != 0 {
		t.Errorf("a failing claim restarted the worker %d times", warnings.Load())
	}
}
