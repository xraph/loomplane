package orchestration

import (
	"context"
	"testing"
	"time"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/store/memory"
)

type recoveryEngine struct {
	worker *drt.Worker
	starts []durable.StartRequest
}

func (e *recoveryEngine) QueryDurableWorkflow(ctx context.Context, q drt.QueryRequest) (drt.QueryResult, error) {
	return e.worker.QueryExecution(ctx, q)
}
func (e *recoveryEngine) StartDurableWorkflow(ctx context.Context, r durable.StartRequest) (durable.Receipt, error) {
	e.starts = append(e.starts, r)
	return e.worker.StartExecution(ctx, r)
}

func TestReleaseRecoverySurvivesSettledStartAndWorkerRestart(t *testing.T) {
	ctx := t.Context()
	store := memory.New()
	options := drt.Options{Namespace: DurableNamespace, Queue: DurableQueue, BuildID: DeploymentBuild, Owner: "recovery-test", Concurrency: 1, LeaseDuration: time.Minute, Workflows: map[string]drt.WorkflowFunc{
		resumeReleaseWorkflow: func(w *drt.Workflow, _ []byte) ([]byte, error) {
			w.SetQueryHandler("execution-state", func([]byte) ([]byte, error) { return []byte("{}"), nil })
			if w.RunInfo().RetryAttempt == 1 {
				return nil, &drt.ApplicationError{Type: "temporary", Message: "database unavailable"}
			}
			return []byte("resumed"), nil
		},
	}}
	worker, err := drt.NewWorker(store, options)
	if err != nil {
		t.Fatal(err)
	}
	run := func(w *drt.Worker) {
		t.Helper()
		if worked, e := w.RunOnce(ctx, durable.TaskWorkflow); e != nil || !worked {
			t.Fatalf("workflow step: %v %v", worked, e)
		}
	}
	legacy := durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: "resume-release:plan", RunID: runID}, RequestID: "resume-release:plan", WorkflowType: resumeReleaseWorkflow, BuildID: DeploymentBuild, Queue: DurableQueue}
	if _, err = worker.StartExecution(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	run(worker)
	// Dispatch returns the original receipt even when the accepted run has failed.
	if _, err = worker.StartExecution(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	closed, err := store.GetExecution(ctx, legacy.Key)
	if err != nil || closed.State != durable.StateFailed || closed.NextRunID != "" {
		t.Fatalf("settled legacy run unexpectedly restarted: %+v %v", closed, err)
	}
	eng := &recoveryEngine{worker: worker}
	if err = resumeReleaseOn(ctx, eng, "org", "plan"); err != nil {
		t.Fatal(err)
	}
	if len(eng.starts) != 1 || eng.starts[0].WorkflowID != "resume-release-v2:plan" || eng.starts[0].RetryPolicy == nil {
		t.Fatalf("recovery request: %+v", eng.starts)
	}
	run(worker)
	root, err := store.GetExecution(ctx, eng.starts[0].Key)
	if err != nil || root.State != durable.StateFailed || root.NextRunID == "" {
		t.Fatalf("failure did not persist a successor: %+v %v", root, err)
	}
	if err = resumeReleaseOn(ctx, eng, "org", "plan"); err != nil {
		t.Fatal(err)
	}
	unchanged, err := store.GetExecution(ctx, eng.starts[0].Key)
	if err != nil || unchanged.NextRunID != root.NextRunID {
		t.Fatal("outbox replay changed the persisted attempt identity")
	}
	nextKey := root.Key
	nextKey.RunID = root.NextRunID
	successor, err := store.GetExecution(ctx, nextKey)
	if err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(time.Until(successor.AvailableAt()) + time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	replacement, err := drt.NewWorker(store, options)
	if err != nil {
		t.Fatal(err)
	}
	run(replacement)
	completed, err := store.GetExecution(ctx, nextKey)
	if err != nil || completed.State != durable.StateCompleted || string(completed.Output) != "resumed" {
		t.Fatalf("replacement worker did not resume: %+v %v", completed, err)
	}
}

func TestReleaseRecoveryDoesNotRaceRunningLegacyWorkflow(t *testing.T) {
	store := memory.New()
	options := drt.Options{Namespace: DurableNamespace, Queue: DurableQueue, BuildID: DeploymentBuild, Owner: "running-test", Concurrency: 1, Workflows: map[string]drt.WorkflowFunc{
		resumeReleaseWorkflow: func(w *drt.Workflow, _ []byte) ([]byte, error) {
			w.SetQueryHandler("execution-state", func([]byte) ([]byte, error) { return []byte("{}"), nil })
			return []byte("{}"), nil
		},
	}}
	worker, err := drt.NewWorker(store, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = worker.StartExecution(t.Context(), durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: "resume-release:plan", RunID: runID}, RequestID: "legacy", WorkflowType: resumeReleaseWorkflow, BuildID: DeploymentBuild, Queue: DurableQueue}); err != nil {
		t.Fatal(err)
	}
	eng := &recoveryEngine{worker: worker}
	if err = resumeReleaseOn(t.Context(), eng, "org", "plan"); err != nil {
		t.Fatal(err)
	}
	if len(eng.starts) != 0 {
		t.Fatal("running legacy recovery was duplicated")
	}
}
