package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
)

const FleetInstanceBuild = "fleet-instance-v1"
const fleetInstanceWorkflow = "loomplane.fleet.instance.v1"
const fleetInstanceActivity = "loomplane.fleet.instance.reconcile.v1"

// FleetInstanceProgress reports persisted state after one bounded reconciliation.
type FleetInstanceProgress struct {
	Status string `json:"status"`
}

// FleetInstanceRunner reloads the recorded principal, pins and receipts on every
// call. Its provider operations must reconcile unknown outcomes before retrying.
type FleetInstanceRunner interface {
	ReconcileFleetInstance(context.Context, string) (FleetInstanceProgress, error)
}

type fleetInstanceInput struct {
	OperationID string `json:"operation_id"`
}

// FleetInstances schedules Fleet work on the existing Dispatch durable runtime.
type FleetInstances struct {
	mu     sync.RWMutex
	worker *drt.Worker
	runner FleetInstanceRunner
}

func NewFleetInstances() *FleetInstances             { return &FleetInstances{} }
func (f *FleetInstances) Bind(r FleetInstanceRunner) { f.mu.Lock(); defer f.mu.Unlock(); f.runner = r }
func (f *FleetInstances) BindWorker(w *drt.Worker)   { f.mu.Lock(); defer f.mu.Unlock(); f.worker = w }
func (f *FleetInstances) Options(owner string, concurrency int) drt.Options {
	return drt.Options{Namespace: DurableNamespace, Queue: DurableQueue, BuildID: FleetInstanceBuild, Owner: owner, Concurrency: concurrency,
		Workflows: map[string]drt.WorkflowFunc{fleetInstanceWorkflow: f.workflow}, Activities: map[string]drt.ActivityFunc{fleetInstanceActivity: f.reconcile}}
}

// RunFleetInstance deduplicates starts even when the previous response was lost.
func (f *FleetInstances) RunFleetInstance(ctx context.Context, operationID string) error {
	if _, err := uuid.Parse(operationID); err != nil {
		return fmt.Errorf("invalid fleet operation ID: %w", err)
	}
	f.mu.RLock()
	worker, runner := f.worker, f.runner
	f.mu.RUnlock()
	if worker == nil || runner == nil {
		return errors.New("fleet workflow worker or runner is unavailable")
	}
	raw, err := json.Marshal(fleetInstanceInput{OperationID: operationID})
	if err != nil {
		return err
	}
	id := "fleet-instance:" + operationID
	_, err = worker.StartExecution(ctx, durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: id, RunID: runID}, RequestID: id, WorkflowType: fleetInstanceWorkflow, BuildID: FleetInstanceBuild, Queue: DurableQueue, Input: raw, RetryPolicy: recoveryRetryPolicy()})
	return err
}

func (f *FleetInstances) workflow(w *drt.Workflow, raw []byte) ([]byte, error) {
	var in fleetInstanceInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	for n := 0; n < 2880; n++ {
		step := fmt.Sprintf("reconcile-%d", n)
		var progress FleetInstanceProgress
		if err := callInto(w, step, fleetInstanceActivity, in, activityTimeout, 0, activityAttempts, &progress); err != nil {
			return nil, err
		}
		switch progress.Status {
		case "succeeded", "failed", "cancelled":
			return json.Marshal(progress)
		case "waiting_approval", "waiting_capacity", "provisioning", "verifying", "cancelling", "needs_attention":
		default:
			return nil, fmt.Errorf("invalid fleet operation status %q", progress.Status)
		}
		if _, err := w.Timer(step+"-wait", 30*time.Second).Get(); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("fleet operation reconciliation deadline exceeded")
}

func (f *FleetInstances) reconcile(ctx context.Context, _ drt.ActivityInfo, raw []byte) ([]byte, error) {
	var in fleetInstanceInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	f.mu.RLock()
	runner := f.runner
	f.mu.RUnlock()
	if runner == nil {
		return nil, errors.New("fleet instance runner is unavailable")
	}
	progress, err := runner.ReconcileFleetInstance(ctx, in.OperationID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(progress)
}
