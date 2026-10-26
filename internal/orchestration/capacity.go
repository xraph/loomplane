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

const CapacityBuild = "deployment-v3"
const SignalCapacity = "capacity"
const ActivityCapacity = "loomplane.deploy.capacity.v3"
const capacityInterval = 30 * time.Second

type capacityContextKey struct{}

// CapacityContext identifies activities that enforce the v3 admission protocol.
func CapacityContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, capacityContextKey{}, true)
}
func IsCapacityContext(ctx context.Context) bool {
	v, ok := ctx.Value(capacityContextKey{}).(bool)
	return ok && v
}

// CapacityAssessment is persisted as an activity result, including its next timer.
type CapacityAssessment struct {
	Status   string `json:"status"`
	Revision int64  `json:"revision,omitempty"`
}
type capacityDeployer interface {
	AssessWorkflowCapacity(context.Context, string, string) (CapacityAssessment, error)
}
type capacityEffectResult struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// CapacityEffectError lets the workflow preserve an observed fence after a concurrent resume.
type CapacityEffectError interface{ CapacityStatus() string }

func (d *Deployments) CapacityOptions(owner string, concurrency int) drt.Options {
	o := d.Options(owner, concurrency)
	o.BuildID = CapacityBuild
	o.Workflows = map[string]drt.WorkflowFunc{WorkflowDeployment: d.workflowV3, WorkflowRetry: d.retryV3, resumeReleaseWorkflow: d.resumeReleaseV3, cloudSyncWorkflow: d.syncV3}
	o.Activities[ActivityCapacity] = d.assessCapacity
	o.Activities[cloudSyncPrepareActivity] = d.syncPrepare
	o.Activities[cloudSyncFinishActivity] = d.syncFinish
	o.Activities[deploymentStateActivity] = d.deploymentState
	o.Activities[ActivityDeployStart] = d.capacityStart
	o.Activities[ActivityDeploySource] = d.capacitySource
	o.Activities[ActivityDeployAction] = d.capacityAction
	o.Activities[ActivityDeployFinish] = d.capacityFinish
	return o
}
func (d *Deployments) assessCapacity(ctx context.Context, _ drt.ActivityInfo, raw []byte) ([]byte, error) {
	var in deployInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	dep, err := d.boundDeployer()
	if err != nil {
		return nil, err
	}
	c, ok := dep.(capacityDeployer)
	if !ok {
		return nil, errors.New("capacity assessment is unavailable")
	}
	result, err := c.AssessWorkflowCapacity(CapacityContext(ctx), in.OrgID, in.DeploymentID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}
func capacityResult(err error) ([]byte, error) {
	if err == nil {
		return json.Marshal(capacityEffectResult{Status: "running"})
	}
	var fence CapacityEffectError
	if errors.As(err, &fence) {
		return json.Marshal(capacityEffectResult{Status: fence.CapacityStatus()})
	}
	return nil, err
}
func (d *Deployments) capacityStart(ctx context.Context, info drt.ActivityInfo, raw []byte) ([]byte, error) {
	return d.deployStart(CapacityContext(ctx), info, raw)
}
func (d *Deployments) capacitySource(ctx context.Context, info drt.ActivityInfo, raw []byte) ([]byte, error) {
	_, err := d.deploySource(CapacityContext(ctx), info, raw)
	return capacityResult(err)
}
func (d *Deployments) capacityAction(ctx context.Context, info drt.ActivityInfo, raw []byte) ([]byte, error) {
	_, err := d.deployAction(CapacityContext(ctx), info, raw)
	return capacityResult(err)
}
func (d *Deployments) capacityFinish(ctx context.Context, info drt.ActivityInfo, raw []byte) ([]byte, error) {
	_, err := d.deployFinish(CapacityContext(ctx), info, raw)
	if err != nil {
		return capacityResult(err)
	}
	dep, e := d.boundDeployer()
	if e != nil {
		return nil, e
	}
	if checker, ok := dep.(interface {
		WorkflowCapacityState(context.Context, string, string) (CapacityAssessment, error)
	}); ok {
		var in deployInput
		if decodeErr := json.Unmarshal(raw, &in); decodeErr != nil {
			return nil, decodeErr
		}
		state, e := checker.WorkflowCapacityState(ctx, in.OrgID, in.DeploymentID)
		if e != nil {
			return nil, e
		}
		if state.Status == "paused" || state.Status == "cancelled" {
			return json.Marshal(capacityEffectResult{Status: state.Status})
		}
	}
	return capacityResult(nil)
}

func awaitCapacity(w *drt.Workflow, dep deployInput, prefix string, interval time.Duration) (bool, error) {
	if interval <= 0 || interval > capacityInterval {
		return false, errors.New("invalid persisted capacity reconciliation interval")
	}
	for n := 0; ; n++ {
		id := fmt.Sprintf("%s-capacity-%d", prefix, n)
		var result CapacityAssessment
		if err := callInto(w, id, ActivityCapacity, dep, activityTimeout, 0, activityAttempts, &result); err != nil {
			return false, err
		}
		switch result.Status {
		case "running":
			return true, nil
		case "cancelled":
			return false, nil
		case "paused":
		default:
			return false, fmt.Errorf("unexpected capacity state %q", result.Status)
		}
		signal := w.ReceiveSignal(id+"-wake", SignalCapacity)
		timer := w.Timer(id+"-timer", interval)
		selected := w.Select(id+"-await", signal, timer)
		if _, err := selected.Get(); err != nil {
			return false, err
		}
	}
}
func runStepsV3(w *drt.Workflow, org string, started deployStarted, interval time.Duration) error {
	reason, stopped, err := capacityEffects(w, org, started, interval)
	if err != nil || stopped {
		return err
	}
	return finishCapacity(w, org, started.DeploymentID, reason, interval)
}

// capacityEffects stores the source and runs each action once capacity
// admits it, until one fails. It returns why one failed, or stopped when
// capacity cancelled the deployment, which then has nothing left to settle.
func capacityEffects(w *drt.Workflow, org string, started deployStarted, interval time.Duration) (reason string, stopped bool, err error) {
	dep := deployInput{OrgID: org, DeploymentID: started.DeploymentID}
	effects := []struct {
		id, name           string
		input              deployInput
		timeout, heartbeat time.Duration
		attempts           int64
	}{{"source", ActivityDeploySource, dep, sourceTimeout, 0, activityAttempts}}
	for _, group := range started.Steps {
		for _, id := range group {
			step := dep
			step.ActionID = id
			effects = append(effects, struct {
				id, name           string
				input              deployInput
				timeout, heartbeat time.Duration
				attempts           int64
			}{id, ActivityDeployAction, step, actionTimeout, actionHeartbeat, actionAttempts})
		}
	}
	for _, effect := range effects {
		for attempt := 0; ; attempt++ {
			id := fmt.Sprintf("%s-effect-%s-%d", started.DeploymentID, effect.id, attempt)
			ready, awaitErr := awaitCapacity(w, dep, id, interval)
			if awaitErr != nil {
				return "", false, awaitErr
			}
			if !ready {
				return "", true, nil
			}
			var result capacityEffectResult
			if callErr := callInto(w, id, effect.name, effect.input, effect.timeout, effect.heartbeat, effect.attempts, &result); callErr != nil {
				reason = callErr.Error()
				break
			}
			if result.Status == "cancelled" {
				return "", true, nil
			}
			if result.Status == "paused" {
				continue
			}
			break
		}
		if reason != "" {
			return reason, false, nil
		}
	}
	return "", false, nil
}

// finishCapacity settles the deployment, failed with reason or succeeded,
// once capacity admits it. Finish can race a pause too. It must re-assess
// instead of settling paused work.
func finishCapacity(w *drt.Workflow, org, deploymentID, reason string, interval time.Duration) error {
	dep := deployInput{OrgID: org, DeploymentID: deploymentID}
	for attempt := 0; ; attempt++ {
		id := fmt.Sprintf("%s-finish-%d", deploymentID, attempt)
		ready, err := awaitCapacity(w, dep, id, interval)
		if err != nil {
			return err
		}
		if !ready {
			return nil
		}
		done := dep
		done.Reason = reason
		var result capacityEffectResult
		if finishErr := callInto(w, id, ActivityDeployFinish, done, activityTimeout, 0, activityAttempts, &result); finishErr != nil {
			return finishErr
		}
		if result.Status != "paused" {
			return nil
		}
	}
}

// worker is the client that starts, signals and cancels runs of build. The
// engine runs DeploymentBuild; each later build has a worker of its own.
func (d *Deployments) worker(build string) (*drt.Worker, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	w := d.workers[build]
	if w == nil {
		return nil, fmt.Errorf("no workflow worker runs build %q", build)
	}
	return w, nil
}

// BindWorker keeps the immutable client for build's starts, signals and
// cancellations. Supervisors own polling replacements.
func (d *Deployments) BindWorker(build string, worker *drt.Worker) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.workers == nil {
		d.workers = map[string]*drt.Worker{}
	}
	d.workers[build] = worker
}

// BindCapacityWorker binds the v3 build's worker.
func (d *Deployments) BindCapacityWorker(worker *drt.Worker) {
	d.BindWorker(CapacityBuild, worker)
}

func (d *Deployments) SignalCapacity(ctx context.Context, org, dep string, revision int64) error {
	if d.runs == nil {
		return errors.New("deployment run store is unavailable")
	}
	route, err := d.runs.ForDeployment(ctx, org, dep)
	if err != nil {
		return err
	}
	execution, err := d.currentExecution(ctx, route.Request.Key)
	if err != nil {
		return err
	}
	if execution.State != durable.StateRunning {
		return nil
	}
	raw, err := json.Marshal(CapacityAssessment{Revision: revision})
	if err != nil {
		return err
	}
	err = d.signalRun(ctx, durable.SignalRequest{Key: execution.Key, BuildID: execution.BuildID, RequestID: fmt.Sprintf("capacity:%s:%s:%d:%s:%s", org, dep, revision, execution.WorkflowID, execution.RunID), Name: SignalCapacity, Input: raw})
	return err
}
