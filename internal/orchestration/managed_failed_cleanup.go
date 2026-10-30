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

const managedFailedCleanupWorkflow = "loomplane.managed-failed-cleanup.v1"
const managedFailedCleanupActivity = "loomplane.managed-failed-cleanup.execute.v1"

type managedFailedCleanupRunner interface {
	RunManagedFailedCleanup(context.Context, string, string, string, int, func([]byte) error) error
}

type managedFailedCleanupInput struct {
	OrgID        string `json:"org_id"`
	DeploymentID string `json:"deployment_id"`
	ReviewID     string `json:"review_id"`
	Attempt      int    `json:"attempt"`
}

func (d *Deployments) StartManagedFailedCleanup(ctx context.Context, org, deployment, review string, attempt int) error {
	if d.runs == nil {
		return errors.New("managed cleanup routing store is unavailable")
	}
	request := fmt.Sprintf("managed-failed-cleanup:%s:%d", review, attempt)
	raw, err := json.Marshal(managedFailedCleanupInput{org, deployment, review, attempt})
	if err != nil {
		return err
	}
	return d.startRun(ctx, DeploymentRun{OrgID: org, PlanID: "managed-failed-cleanup:" + review, Request: durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: request, RunID: runID}, RequestID: request, WorkflowType: managedFailedCleanupWorkflow, BuildID: ManagedCleanupBuild, Queue: DurableQueue, Input: raw, RetryPolicy: recoveryRetryPolicy()}})
}

func (d *Deployments) managedCleanupWorkflow(w *drt.Workflow, input []byte) ([]byte, error) {
	var in managedFailedCleanupInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	return []byte("{}"), callInto(w, "managed-cleanup", managedFailedCleanupActivity, in, 6*time.Hour, actionHeartbeat, activityAttempts, nil)
}

func (d *Deployments) managedCleanupActivity(ctx context.Context, info drt.ActivityInfo, input []byte) ([]byte, error) {
	var in managedFailedCleanupInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	d.mu.RLock()
	runner, ok := d.failedCleanups.(managedFailedCleanupRunner)
	d.mu.RUnlock()
	if !ok {
		return nil, errors.New("managed failed cleanup runner is unavailable")
	}
	return []byte("{}"), runner.RunManagedFailedCleanup(ctx, in.OrgID, in.DeploymentID, in.ReviewID, in.Attempt, func(raw []byte) error { return info.Heartbeat(ctx, raw) })
}
