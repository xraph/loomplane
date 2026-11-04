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

const clusterWorkflow = "loomplane.cluster.v1"
const clusterActivity = "loomplane.cluster.reconcile.v1"

type ClusterReconciler interface {
	ReconcileClusterOperation(context.Context, string, string, func([]byte) error) error
	RecoverClusterOperations(context.Context) error
}
type clusterInput struct {
	OrgID string `json:"org_id"`
	ID    string `json:"id"`
}

func (d *Deployments) BindClusters(r ClusterReconciler) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cluster = r
}
func (d *Deployments) StartClusterOperation(ctx context.Context, org, id string, attempt int) error {
	eng, err := d.engine()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(clusterInput{OrgID: org, ID: id})
	if err != nil {
		return err
	}
	request := fmt.Sprintf("cluster:%s:%d", id, attempt)
	_, err = eng.StartDurableWorkflow(ctx, durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: request, RunID: runID}, RequestID: request, WorkflowType: clusterWorkflow, BuildID: DeploymentBuild, Queue: DurableQueue, Input: raw, RetryPolicy: recoveryRetryPolicy()})
	return err
}
func (d *Deployments) clusterRun(w *drt.Workflow, input []byte) ([]byte, error) {
	var in clusterInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	err := callInto(w, "reconcile", clusterActivity, in, 6*time.Hour, actionHeartbeat, activityAttempts, nil)
	return []byte("{}"), err
}
func (d *Deployments) clusterReconcile(ctx context.Context, info drt.ActivityInfo, input []byte) ([]byte, error) {
	var in clusterInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	d.mu.RLock()
	r := d.cluster
	d.mu.RUnlock()
	if r == nil {
		return nil, errors.New("cluster reconciler unavailable")
	}
	return []byte("{}"), r.ReconcileClusterOperation(ctx, in.OrgID, in.ID, func(raw []byte) error { return info.Heartbeat(ctx, raw) })
}
