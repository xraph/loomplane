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

const routeWorkflow = "loomplane.domain-route.v1"
const routeActivity = "loomplane.domain-route.reconcile.v1"

type RouteReconciler interface {
	ReconcileRoute(context.Context, string, string, func([]byte) error) error
}

func (d *Deployments) StartRoute(ctx context.Context, org, id string, attempt int) error {
	engine, err := d.engine()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(infrastructureInput{OrgID: org, ID: id})
	if err != nil {
		return err
	}
	request := fmt.Sprintf("route:%s:%d", id, attempt)
	_, err = engine.StartDurableWorkflow(ctx, durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: request, RunID: runID}, RequestID: request, WorkflowType: routeWorkflow, BuildID: DeploymentBuild, Queue: DurableQueue, Input: raw, RetryPolicy: recoveryRetryPolicy()})
	return err
}

func (d *Deployments) routeRun(w *drt.Workflow, input []byte) ([]byte, error) {
	var in infrastructureInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	err := callInto(w, "reconcile", routeActivity, in, 6*time.Hour, actionHeartbeat, activityAttempts, nil)
	return []byte("{}"), err
}

func (d *Deployments) routeReconcile(ctx context.Context, info drt.ActivityInfo, input []byte) ([]byte, error) {
	var in infrastructureInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	d.mu.RLock()
	reconciler, ok := d.infrastructure.(RouteReconciler)
	d.mu.RUnlock()
	if !ok {
		return nil, errors.New("domain route reconciler unavailable")
	}
	return []byte("{}"), reconciler.ReconcileRoute(ctx, in.OrgID, in.ID, func(raw []byte) error { return info.Heartbeat(ctx, raw) })
}
