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

const infrastructureWorkflow = "loomplane.infrastructure.v1"
const infrastructureActivity = "loomplane.infrastructure.reconcile.v1"

type InfrastructureReconciler interface {
	ReconcileInfrastructure(context.Context, string, string, func([]byte) error) error
	RecoverInfrastructure(context.Context) error
}
type infrastructureInput struct {
	OrgID string `json:"org_id"`
	ID    string `json:"id"`
}

func (d *Deployments) BindInfrastructure(r InfrastructureReconciler) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.infrastructure = r
}
func (d *Deployments) StartInfrastructure(ctx context.Context, org, id string, attempt int) error {
	eng, err := d.engine()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(infrastructureInput{OrgID: org, ID: id})
	if err != nil {
		return err
	}
	request := fmt.Sprintf("infrastructure:%s:%d", id, attempt)
	_, err = eng.StartDurableWorkflow(ctx, durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: request, RunID: runID}, RequestID: request, WorkflowType: infrastructureWorkflow, BuildID: DeploymentBuild, Queue: DurableQueue, Input: raw, RetryPolicy: recoveryRetryPolicy()})
	return err
}
func (d *Deployments) infrastructureRun(w *drt.Workflow, input []byte) ([]byte, error) {
	var in infrastructureInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	err := callInto(w, "reconcile", infrastructureActivity, in, 6*time.Hour, actionHeartbeat, activityAttempts, nil)
	return []byte("{}"), err
}
func (d *Deployments) infrastructureReconcile(ctx context.Context, info drt.ActivityInfo, input []byte) ([]byte, error) {
	var in infrastructureInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	d.mu.RLock()
	r := d.infrastructure
	d.mu.RUnlock()
	if r == nil {
		return nil, errors.New("infrastructure reconciler unavailable")
	}
	return []byte("{}"), r.ReconcileInfrastructure(ctx, in.OrgID, in.ID, func(raw []byte) error { return info.Heartbeat(ctx, raw) })
}
