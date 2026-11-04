package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
)

const RuntimeAccessBuild = "runtime-access-v1"
const RuntimeAccessWorkflow = "loomplane.runtime-access.v1"
const runtimeAccessActivity = "loomplane.runtime-access.reconcile.v1"

type runtimeAccessReconciler interface {
	ReconcileRuntimeAccessOperation(context.Context, string, string, func([]byte) error) error
}

func (d *Deployments) RuntimeAccessOptions(owner string, concurrency int) drt.Options {
	options := d.Options(owner, concurrency)
	options.BuildID = RuntimeAccessBuild
	options.Workflows = map[string]drt.WorkflowFunc{RuntimeAccessWorkflow: d.runtimeAccessWorkflow}
	options.Activities = map[string]drt.ActivityFunc{runtimeAccessActivity: d.runtimeAccessReconcile}
	return options
}

func (d *Deployments) StartRuntimeAccessOperation(ctx context.Context, org, id string, attempt int) error {
	if d.runs == nil {
		return errors.New("runtime access routing store is unavailable")
	}
	raw, err := json.Marshal(clusterInput{OrgID: org, ID: id})
	if err != nil {
		return err
	}
	request := fmt.Sprintf("runtime-access:%s:%d", id, attempt)
	return d.startRun(ctx, DeploymentRun{OrgID: org, PlanID: "runtime-access:" + id, Request: durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: request, RunID: runID}, RequestID: request, WorkflowType: RuntimeAccessWorkflow, BuildID: RuntimeAccessBuild, Queue: DurableQueue, Input: raw, RetryPolicy: recoveryRetryPolicy()}})
}
func (d *Deployments) runtimeAccessWorkflow(w *drt.Workflow, input []byte) ([]byte, error) {
	var in clusterInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	return []byte("{}"), callInto(w, "runtime-access", runtimeAccessActivity, in, 6*time.Hour, actionHeartbeat, activityAttempts, nil)
}
func (d *Deployments) runtimeAccessReconcile(ctx context.Context, info drt.ActivityInfo, input []byte) ([]byte, error) {
	var in clusterInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	d.mu.RLock()
	r, ok := d.cluster.(runtimeAccessReconciler)
	d.mu.RUnlock()
	if !ok {
		return nil, errors.New("runtime access reconciler unavailable")
	}
	return []byte("{}"), r.ReconcileRuntimeAccessOperation(ctx, in.OrgID, in.ID, func(raw []byte) error { return info.Heartbeat(ctx, raw) })
}
