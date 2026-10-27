package orchestration

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	drt "github.com/xraph/dispatch/durable/runtime"
)

func (d *Deployments) workflowV3(w *drt.Workflow, input []byte) ([]byte, error) {
	w.SetQueryHandler("execution-state", func([]byte) ([]byte, error) { return []byte("{}"), nil })
	var in DeploymentInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("decode deployment input: %w", err)
	}
	status, err := call(w, "plan", ActivityPlan, in)
	if err != nil {
		// Say so on the plan, or it would sit in "planning" for good.
		if _, ferr := call(w, "fail", ActivityFail, failInput{DeploymentInput: in, Reason: err.Error()}); ferr != nil {
			return nil, errors.Join(err, ferr)
		}
		return nil, err
	}
	if status != PlanReady {
		return finished(status)
	}

	deadline := w.Timer("deadline", in.Window)
	for i := 1; i <= maxDecisions; i++ {
		decision := w.ReceiveSignal(fmt.Sprintf("decision-%d", i), SignalDecision)
		if w.Select(fmt.Sprintf("await-%d", i), decision, deadline) == deadline {
			break
		}
		data, gerr := decision.Get()
		if gerr != nil {
			return nil, gerr
		}
		var dec Decision
		if json.Unmarshal(data, &dec) != nil {
			// A malformed signal is skipped, not fatal: the plan keeps
			// waiting for a decision it can read.
			continue
		}
		status, err = call(w, fmt.Sprintf("decide-%d", i), ActivityDecide, decideInput{DeploymentInput: in, Decision: dec})
		if err != nil {
			return nil, err
		}
		if status == PlanApproved {
			return deployV3(w, in)
		}
		if status != PlanReady {
			return finished(status)
		}
	}
	status, err = call(w, "expire", ActivityExpire, in)
	if err != nil {
		return nil, err
	}
	return finished(status)
}

func deployV3(w *drt.Workflow, in DeploymentInput) ([]byte, error) {
	var started deployStarted
	if err := callInto(w, "deploy-start", ActivityDeployStart, in, activityTimeout, 0, activityAttempts, &started); err != nil {
		return nil, err
	}
	if started.DeploymentID == "" {
		return finished(PlanApproved)
	}
	if err := runStepsV3(w, in.OrgID, started, in.ReconcileInterval); err != nil {
		return nil, err
	}
	return json.Marshal(Outcome{Status: PlanApproved, Deployment: started.DeploymentID})
}
func (d *Deployments) retryV3(w *drt.Workflow, raw []byte) ([]byte, error) {
	var in retryInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	if err := runStepsV3(w, in.OrgID, deployStarted{DeploymentID: in.DeploymentID, Steps: in.Steps, Timeouts: in.Timeouts}, in.ReconcileInterval); err != nil {
		return nil, err
	}
	return json.Marshal(Outcome{Status: PlanApproved, Deployment: in.DeploymentID})
}
func (d *Deployments) resumeReleaseV3(w *drt.Workflow, input []byte) ([]byte, error) {
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
			return deployV3(w, in)
		}
		if _, err := w.Timer(fmt.Sprintf("wait-%d", attempt), time.Minute).Get(); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("release waited a year for environment recovery")
}
