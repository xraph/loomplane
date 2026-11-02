package orchestration

import (
	"context"
	"testing"
	"time"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
)

func TestManagedCleanupCutoverRealWorkerClaims(t *testing.T) {
	for _, race := range []bool{false, true} {
		name := "unclaimed"
		if race {
			name = "claim after cancellation request"
		}
		t.Run(name, func(t *testing.T) {
			r := newRoutedWorkflows(t)
			entered, release := make(chan struct{}, 1), make(chan struct{})
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			opts := r.d.LegacyCleanupOptions("historical", 1)
			const wf = WorkflowDeployment
			opts.Workflows[wf] = func(w *drt.Workflow, _ []byte) ([]byte, error) {
				input := deployInput{ActionID: "service:api"}
				return []byte("{}"), callInto(w, "remove", ActivityDeployAction, input, time.Minute, 0, 1, nil)
			}
			opts.Activities[ActivityDeployAction] = func(c context.Context, info drt.ActivityInfo, _ []byte) ([]byte, error) {
				if info.Key.WorkflowID == "legacy-target" {
					entered <- struct{}{}
					select {
					case <-release:
					case <-c.Done():
						return nil, c.Err()
					}
				}
				return []byte("{}"), nil
			}
			old, err := drt.NewWorker(r.store, opts)
			if err != nil {
				t.Fatal(err)
			}
			recoveryOpts := opts
			recoveryOpts.Owner = "compatible-recovery"
			recovery, err := drt.NewWorker(r.store, recoveryOpts)
			if err != nil {
				t.Fatal(err)
			}
			req := planRequest("legacy-target", LegacyCleanupBuild)
			req.WorkflowType = wf
			if _, err = old.StartExecution(ctx, req); err != nil {
				t.Fatal(err)
			}
			if did, e := old.RunOnce(ctx, durable.TaskWorkflow); !did || e != nil {
				t.Fatal(did, e)
			}
			q := legacyCleanupRun{org: "org", plan: "p", workflow: req.WorkflowID, route: req, cutoff: time.Now().UTC(), actions: map[string]bool{"service:api": true}}
			mismatched := q
			mismatched.route.Input = []byte("another input")
			if state, _, mismatchErr := mismatched.reconcile(ctx, r.store); state != "uncertain" || mismatchErr != nil {
				t.Fatal("mismatched saved route", state, mismatchErr)
			}
			state, _, err := q.reconcile(ctx, r.store)
			if err != nil || state != "pending" {
				t.Fatal("request is not terminal proof", state, err)
			}
			done := make(chan error, 1)
			if race {
				go func() { _, e := old.RunOnce(ctx, durable.TaskActivity); done <- e }()
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			for n := 0; n < 8; n++ {
				if _, err = recovery.RunOnce(ctx, durable.TaskWorkflow); err != nil {
					t.Fatal(err)
				}
				e, readErr := r.store.GetExecution(ctx, req.Key)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if e.State != durable.StateRunning {
					break
				}
			}
			close(release)
			if race {
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			state, detail, err := q.reconcile(ctx, r.store)
			want := "drained"
			if race {
				want = "uncertain"
			}
			if err != nil || state != want {
				t.Fatalf("%s %s: %v", state, detail, err)
			}
			other := planRequest("unrelated", LegacyCleanupBuild)
			other.WorkflowType = wf
			if _, err = old.StartExecution(ctx, other); err != nil {
				t.Fatal(err)
			}
			if did, e := old.RunOnce(ctx, durable.TaskWorkflow); !did || e != nil {
				t.Fatal("unrelated workflow", did, e)
			}
			if did, e := old.RunOnce(ctx, durable.TaskActivity); !did || e != nil {
				t.Fatal("unrelated activity", did, e)
			}
			v7 := planRequest("new-cleanup", ManagedCleanupBuild)
			if _, err = r.w7.StartExecution(ctx, v7); err != nil {
				t.Fatal(err)
			}
			// Finish the unrelated run, then only the v7 build can claim its new cleanup.
			_, _ = old.RunOnce(ctx, durable.TaskWorkflow)
			if did, e := old.RunOnce(ctx, durable.TaskWorkflow); did || e != nil {
				t.Fatal("v6 claimed v7", did, e)
			}
			if did, e := r.w7.RunOnce(ctx, durable.TaskWorkflow); !did || e != nil {
				t.Fatal("v7 claim", did, e)
			}
		})
	}
}
