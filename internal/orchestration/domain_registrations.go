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

const registrationWorkflow = "loomplane.domain-registration.v1"
const registrationActivity = "loomplane.domain-registration.reconcile.v1"

type RegistrationReconciler interface {
	ReconcileRegistration(context.Context, string, string, func([]byte) error) error
}

// StartRegistration runs one approved registration. The request key keeps a
// repeated start of the same attempt from running it twice.
func (d *Deployments) StartRegistration(ctx context.Context, org, id string, attempt int) error {
	engine, err := d.engine()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(infrastructureInput{OrgID: org, ID: id})
	if err != nil {
		return err
	}
	request := fmt.Sprintf("registration:%s:%d", id, attempt)
	_, err = engine.StartDurableWorkflow(ctx, durable.StartRequest{Key: durable.Key{Namespace: DurableNamespace, WorkflowID: request, RunID: runID}, RequestID: request, WorkflowType: registrationWorkflow, BuildID: DeploymentBuild, Queue: DurableQueue, Input: raw, RetryPolicy: recoveryRetryPolicy()})
	return err
}

func (d *Deployments) registrationRun(w *drt.Workflow, input []byte) ([]byte, error) {
	var in infrastructureInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	err := callInto(w, "reconcile", registrationActivity, in, 6*time.Hour, actionHeartbeat, activityAttempts, nil)
	return []byte("{}"), err
}

func (d *Deployments) registrationReconcile(ctx context.Context, info drt.ActivityInfo, input []byte) ([]byte, error) {
	var in infrastructureInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	d.mu.RLock()
	reconciler, ok := d.infrastructure.(RegistrationReconciler)
	d.mu.RUnlock()
	if !ok {
		return nil, errors.New("domain registration reconciler unavailable")
	}
	return []byte("{}"), reconciler.ReconcileRegistration(ctx, in.OrgID, in.ID, func(raw []byte) error { return info.Heartbeat(ctx, raw) })
}
