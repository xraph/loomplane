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

const failedCleanupWorkflow = "loomplane.failed-cleanup.v1"
const failedCleanupActivity = "loomplane.failed-cleanup.execute.v1"

// FailedCleanupRunner executes and recovers explicitly approved abandonments.
type FailedCleanupRunner interface {
	RunFailedCleanup(context.Context, string, string, int, func([]byte) error) error
	RecoverFailedCleanups(context.Context) error
}

type failedCleanupInput struct {
	OrgID        string `json:"org_id"`
	DeploymentID string `json:"deployment_id"`
	Attempt      int    `json:"attempt"`
}

func (d *Deployments) BindFailedCleanups(r FailedCleanupRunner) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failedCleanups = r
}

func (d *Deployments) StartFailedCleanup(ctx context.Context, org, id string, attempt int) error {
	request := fmt.Sprintf("failed-cleanup:%s:%d", id, attempt)
	raw, err := json.Marshal(failedCleanupInput{org, id, attempt})
	if err != nil {
		return err
	}
	return d.startRun(ctx, DeploymentRun{OrgID: org, PlanID: "failed-cleanup:" + id, Request: durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: request, RunID: runID}, RequestID: request, WorkflowType: failedCleanupWorkflow, BuildID: DeploymentBuild, Queue: DurableQueue, Input: raw, RetryPolicy: recoveryRetryPolicy()}})
}

func (d *Deployments) cleanupWorkflow(w *drt.Workflow, input []byte) ([]byte, error) {
	var in failedCleanupInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	return []byte("{}"), callInto(w, "cleanup", failedCleanupActivity, in, 6*time.Hour, actionHeartbeat, activityAttempts, nil)
}

func (d *Deployments) cleanupActivity(ctx context.Context, info drt.ActivityInfo, input []byte) ([]byte, error) {
	var in failedCleanupInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	d.mu.RLock()
	runner := d.failedCleanups
	d.mu.RUnlock()
	if runner == nil {
		return nil, errors.New("failed cleanup runner is unavailable")
	}
	return []byte("{}"), runner.RunFailedCleanup(ctx, in.OrgID, in.DeploymentID, in.Attempt, func(raw []byte) error { return info.Heartbeat(ctx, raw) })
}
