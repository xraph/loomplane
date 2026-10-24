package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	drt "github.com/xraph/dispatch/durable/runtime"
)

const cloudSyncPrepareActivity = "loomplane.cloud.prepare.v3"
const cloudSyncFinishActivity = "loomplane.cloud.finish.v3"
const deploymentStateActivity = "loomplane.deploy.state.v3"

type CloudSyncDeployment struct {
	PlanID       string     `json:"plan_id"`
	DeploymentID string     `json:"deployment_id"`
	Steps        [][]string `json:"steps"`
}
type capacityCloudReconciler interface {
	CloudSyncNeedsCapacity(context.Context, string, string, string) (bool, error)
	PrepareCapacityCloudSync(context.Context, string, string, string) ([]CloudSyncDeployment, error)
	FinishCapacityCloudSync(context.Context, string, string, string, func([]byte) error) error
}

func (d *Deployments) syncV3(w *drt.Workflow, raw []byte) ([]byte, error) {
	var in cloudSyncInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	var plans []CloudSyncDeployment
	if err := callInto(w, "prepare", cloudSyncPrepareActivity, in, activityTimeout, 0, activityAttempts, &plans); err != nil {
		return nil, err
	}
	for _, plan := range plans {
		ready, err := awaitCapacity(w, deployInput{OrgID: in.OrgID, DeploymentID: plan.DeploymentID}, "preflight-"+plan.PlanID, in.ReconcileInterval)
		if err != nil {
			return nil, err
		}
		if !ready {
			err := callInto(w, "cancelled", cloudSyncFinishActivity, in, activityTimeout, 0, activityAttempts, nil)
			return []byte("{}"), err
		}
	}
	for _, plan := range plans {
		if err := runStepsV3(w, in.OrgID, deployStarted{DeploymentID: plan.DeploymentID, Steps: plan.Steps}, in.ReconcileInterval); err != nil {
			return nil, err
		}

		var state CapacityAssessment
		if err := callInto(w, "settled-"+plan.DeploymentID, deploymentStateActivity, deployInput{OrgID: in.OrgID, DeploymentID: plan.DeploymentID}, activityTimeout, 0, activityAttempts, &state); err != nil {
			return nil, err
		}
		if state.Status != "succeeded" {
			break
		}
	}
	err := callInto(w, "finish", cloudSyncFinishActivity, in, 6*time.Hour, actionHeartbeat, activityAttempts, nil)
	return []byte("{}"), err
}
func (d *Deployments) capacitySyncReconciler() (capacityCloudReconciler, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	r, ok := d.cloudSync.(capacityCloudReconciler)
	if !ok {
		return nil, errors.New("capacity Cloud sync reconciler is unavailable")
	}
	return r, nil
}
func (d *Deployments) syncPrepare(ctx context.Context, info drt.ActivityInfo, raw []byte) ([]byte, error) {
	var in cloudSyncInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	r, err := d.capacitySyncReconciler()
	if err != nil {
		return nil, err
	}
	plans, err := r.PrepareCapacityCloudSync(CapacityContext(ctx), in.OrgID, in.ProjectID, in.SyncID)
	if err != nil {
		return nil, err
	}
	if d.runs != nil {
		for _, plan := range plans {
			if err = d.runs.AttachPlan(ctx, in.OrgID, plan.PlanID, info.Key.WorkflowID); err != nil {
				return nil, fmt.Errorf("attach sync deployment run: %w", err)
			}
		}
	}
	return json.Marshal(plans)
}
func (d *Deployments) syncFinish(ctx context.Context, info drt.ActivityInfo, raw []byte) ([]byte, error) {
	var in cloudSyncInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	r, err := d.capacitySyncReconciler()
	if err != nil {
		return nil, err
	}
	return []byte("{}"), r.FinishCapacityCloudSync(CapacityContext(ctx), in.OrgID, in.ProjectID, in.SyncID, func(data []byte) error { return info.Heartbeat(ctx, data) })
}

func (d *Deployments) deploymentState(ctx context.Context, _ drt.ActivityInfo, raw []byte) ([]byte, error) {
	var in deployInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	dep, err := d.boundDeployer()
	if err != nil {
		return nil, err
	}
	reader, ok := dep.(interface {
		WorkflowCapacityState(context.Context, string, string) (CapacityAssessment, error)
	})
	if !ok {
		return nil, errors.New("deployment state reader is unavailable")
	}
	state, err := reader.WorkflowCapacityState(ctx, in.OrgID, in.DeploymentID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(state)
}
