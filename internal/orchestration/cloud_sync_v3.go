package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
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
	// Timeouts are the actions that get longer than actionTimeout, as
	// deployStarted's. The prepare activity fills them in, so they're
	// recorded with its output.
	Timeouts map[string]time.Duration `json:"timeouts,omitempty"`
}

// CloudSyncPreparation keeps a business rejection out of activity retry policy.
// Older v3 histories stored the participant array directly.
type CloudSyncPreparation struct {
	Deployments []CloudSyncDeployment `json:"deployments"`
	Stopped     bool                  `json:"stopped,omitempty"`
}

func (p *CloudSyncPreparation) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '[' {
		return json.Unmarshal(raw, &p.Deployments)
	}
	type result CloudSyncPreparation
	return json.Unmarshal(raw, (*result)(p))
}

type capacityCloudReconciler interface {
	CloudSyncNeedsCapacity(context.Context, string, string, string) (bool, error)
	PrepareCapacityCloudSync(context.Context, string, string, string) (CloudSyncPreparation, error)
	FinishCapacityCloudSync(context.Context, string, string, string, func([]byte) error) error
}

func (d *Deployments) syncV3(w *drt.Workflow, raw []byte) ([]byte, error) {
	var in cloudSyncInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	var prepared CloudSyncPreparation
	if err := callInto(w, "prepare", cloudSyncPrepareActivity, in, activityTimeout, 0, activityAttempts, &prepared); err != nil {
		return nil, err
	}
	if prepared.Stopped {
		return []byte("{}"), nil
	}
	for _, plan := range prepared.Deployments {
		ready, err := awaitCapacity(w, deployInput{OrgID: in.OrgID, DeploymentID: plan.DeploymentID}, "preflight-"+plan.PlanID, in.ReconcileInterval)
		if err != nil {
			return nil, err
		}
		if !ready {
			err := callInto(w, "cancelled", cloudSyncFinishActivity, in, activityTimeout, 0, activityAttempts, nil)
			return []byte("{}"), err
		}
	}
	for _, plan := range prepared.Deployments {
		if err := runStepsV3(w, in.OrgID, deployStarted{DeploymentID: plan.DeploymentID, Steps: plan.Steps, Timeouts: plan.Timeouts}, in.ReconcileInterval); err != nil {
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
	activityCtx, err := capacitySyncActivityContext(ctx, info, in.SyncID)
	if err != nil {
		return nil, err
	}
	r, err := d.capacitySyncReconciler()
	if err != nil {
		return nil, err
	}
	prepared, err := r.PrepareCapacityCloudSync(activityCtx, in.OrgID, in.ProjectID, in.SyncID)
	if err != nil {
		return nil, err
	}
	if len(prepared.Deployments) > 0 {
		dep, depErr := d.boundDeployer()
		if depErr != nil {
			return nil, depErr
		}
		for i := range prepared.Deployments {
			plan := &prepared.Deployments[i]
			if plan.Timeouts, err = actionTimeouts(ctx, dep, in.OrgID, plan.DeploymentID); err != nil {
				return nil, err
			}
		}
	}
	if d.runs != nil {
		for _, plan := range prepared.Deployments {
			if err = d.runs.AttachPlan(ctx, in.OrgID, plan.PlanID, info.Key.WorkflowID); err != nil {
				return nil, fmt.Errorf("attach sync deployment run: %w", err)
			}
		}
	}
	return json.Marshal(prepared)
}
func (d *Deployments) syncFinish(ctx context.Context, info drt.ActivityInfo, raw []byte) ([]byte, error) {
	var in cloudSyncInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	activityCtx, err := capacitySyncActivityContext(ctx, info, in.SyncID)
	if err != nil {
		return nil, err
	}
	r, err := d.capacitySyncReconciler()
	if err != nil {
		return nil, err
	}
	return []byte("{}"), r.FinishCapacityCloudSync(activityCtx, in.OrgID, in.ProjectID, in.SyncID, func(data []byte) error { return info.Heartbeat(ctx, data) })
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

type cloudSyncAttemptKey struct{}

// CloudSyncAttemptContext fences phase delivery to its accepted operation attempt.
func CloudSyncAttemptContext(ctx context.Context, attempt int) context.Context {
	return context.WithValue(ctx, cloudSyncAttemptKey{}, attempt)
}
func CloudSyncAttempt(ctx context.Context) (int, bool) {
	attempt, ok := ctx.Value(cloudSyncAttemptKey{}).(int)
	return attempt, ok
}
func capacitySyncActivityContext(ctx context.Context, info drt.ActivityInfo, syncID string) (context.Context, error) {
	suffix, ok := strings.CutPrefix(info.Key.WorkflowID, syncID+".attempt.")
	attempt, err := strconv.Atoi(suffix)
	if !ok || err != nil || attempt < 1 {
		return nil, errors.New("invalid Cloud sync workflow attempt")
	}
	return CloudSyncAttemptContext(CapacityContext(ctx), attempt), nil
}
