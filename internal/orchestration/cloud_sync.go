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

// Cloud reconciliation has its own workflow type on the existing worker build.
const cloudSyncWorkflow = "loomplane.cloud.sync.v1"
const cloudSyncActivity = "loomplane.cloud.reconcile.v1"

// CloudReconciler executes the persisted migration and recovers pending outbox rows.
type CloudReconciler interface {
	ReconcileCloudSync(context.Context, string, string, string, func([]byte) error) error
	RecoverCloudSyncs(context.Context) error
}

type cloudSyncInput struct {
	OrgID     string `json:"org_id"`
	ProjectID string `json:"project_id"`
	SyncID    string `json:"sync_id"`
}

// BindCloudSync binds the migration service after the runner is available.
func (d *Deployments) BindCloudSync(reconciler CloudReconciler) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cloudSync = reconciler
}

func (d *Deployments) syncWorkflow(w *drt.Workflow, input []byte) ([]byte, error) {
	var in cloudSyncInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	err := callInto(w, "reconcile", cloudSyncActivity, in, 6*time.Hour, actionHeartbeat, activityAttempts, nil)
	return []byte("{}"), err
}
func (d *Deployments) syncActivity(ctx context.Context, info drt.ActivityInfo, input []byte) ([]byte, error) {
	var in cloudSyncInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	d.mu.RLock()
	reconciler := d.cloudSync
	d.mu.RUnlock()
	if reconciler == nil {
		return nil, errors.New("the Cloud sync reconciler is unavailable")
	}
	return []byte("{}"), reconciler.ReconcileCloudSync(ctx, in.OrgID, in.ProjectID, in.SyncID, func(raw []byte) error { return info.Heartbeat(ctx, raw) })
}

// StartCloudSync deduplicates delivery by operation and explicitly authorized attempt.
func (d *Deployments) StartCloudSync(ctx context.Context, org, project, id string, attempt int) error {
	eng, err := d.engine()
	if err != nil {
		return err
	}
	input, err := json.Marshal(cloudSyncInput{OrgID: org, ProjectID: project, SyncID: id})
	if err != nil {
		return err
	}
	request := fmt.Sprintf("%s.attempt.%d", id, attempt)
	_, err = eng.StartDurableWorkflow(ctx, durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: request, RunID: runID}, RequestID: "start:" + request, WorkflowType: cloudSyncWorkflow, BuildID: DeploymentBuild, Queue: DurableQueue, Input: input, RetryPolicy: recoveryRetryPolicy()})
	return err
}

// RecoverCloudSyncs runs once at startup and periodically through the owner extension.
func (d *Deployments) RecoverCloudSyncs(ctx context.Context) error {
	d.mu.RLock()
	reconciler := d.cloudSync
	d.mu.RUnlock()
	if reconciler == nil {
		return errors.New("the Cloud sync reconciler is unavailable")
	}
	return reconciler.RecoverCloudSyncs(ctx)
}

const resumeReleaseWorkflow = "loomplane.release.resume.v1"
const releaseAvailableActivity = "loomplane.release.available"

// ResumeRelease recovers a settled original workflow without racing one that
// still runs. The replacement workflow waits durably behind an accepted sync.
func (d *Deployments) ResumeRelease(ctx context.Context, org, planID string) error {
	eng, err := d.engine()
	if err != nil {
		return err
	}
	return resumeReleaseOn(ctx, eng, org, planID)
}

type releaseRecoveryEngine interface {
	QueryDurableWorkflow(context.Context, drt.QueryRequest) (drt.QueryResult, error)
	StartDurableWorkflow(context.Context, durable.StartRequest) (durable.Receipt, error)
}

// Dispatch persists successor run IDs and backoff after infrastructure failures.
// Business failures stay in the deployment/sync record and require its retry API.
func recoveryRetryPolicy() *durable.WorkflowRetryPolicy {
	return &durable.WorkflowRetryPolicy{InitialInterval: time.Second, MaximumInterval: time.Minute}
}

func resumeReleaseOn(ctx context.Context, eng releaseRecoveryEngine, org, planID string) error {
	for _, id := range []string{planID, "resume-release:" + planID} {
		result, err := eng.QueryDurableWorkflow(ctx, drt.QueryRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: id, RunID: runID}, BuildID: DeploymentBuild, Name: "execution-state"})
		if err == nil && result.State == durable.StateRunning {
			return nil
		}
		if err != nil && !errors.Is(err, durable.ErrNotFound) {
			return err
		}
	}
	input, err := json.Marshal(DeploymentInput{OrgID: org, PlanID: planID})
	if err != nil {
		return err
	}
	// Older recovery runs had no retry policy. A stable new identity recovers
	// their closed histories without changing an accepted request's fingerprint.
	request := "resume-release-v2:" + planID
	_, err = eng.StartDurableWorkflow(ctx, durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: request, RunID: runID}, RequestID: request, WorkflowType: resumeReleaseWorkflow, BuildID: DeploymentBuild, Queue: DurableQueue, Input: input, RetryPolicy: recoveryRetryPolicy()})
	return err
}
func (d *Deployments) resumeRelease(w *drt.Workflow, input []byte) ([]byte, error) {
	w.SetQueryHandler("execution-state", func([]byte) ([]byte, error) { return []byte("{}"), nil })
	var in DeploymentInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 525600; attempt++ {
		var available bool
		if err := callInto(w, fmt.Sprintf("available-%d", attempt), releaseAvailableActivity, in, activityTimeout, 0, activityAttempts, &available); err != nil {
			return nil, err
		}
		if available {
			return deploy(w, in)
		}
		if _, err := w.Timer(fmt.Sprintf("wait-%d", attempt), time.Minute).Get(); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("release waited a year for environment recovery")
}
func (d *Deployments) releaseAvailable(ctx context.Context, _ drt.ActivityInfo, input []byte) ([]byte, error) {
	var in DeploymentInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	dep, err := d.boundDeployer()
	if err != nil {
		return nil, err
	}
	guard, ok := dep.(interface {
		ReleaseAvailable(context.Context, string, string) (bool, error)
	})
	if !ok {
		return nil, errors.New("release serialization is unavailable")
	}
	available, err := guard.ReleaseAvailable(ctx, in.OrgID, in.PlanID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(available)
}
