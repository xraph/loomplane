package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xraph/dispatch/durable"
	drt "github.com/xraph/dispatch/durable/runtime"
	"github.com/xraph/dispatch/engine"

	"github.com/xraph/loomplane/internal/realtime"
)

// The deployment workflow runs on Dispatch's durable runtime: history
// replay, durable timers and signals, so a plan can wait days for approval
// without holding a goroutine, and a restart resumes it where it was.
const (
	// DurableNamespace and DurableQueue route Loomplane's durable work.
	DurableNamespace = "loomplane"
	DurableQueue     = "loomplane-deployments"
	// DeploymentBuild names the deployment workflow's code, not Loomplane's
	// version. A run only replays on workers with the same build, so bump
	// it when the workflow's steps change, and keep the old code until the
	// old build's runs have finished. An upgrade that doesn't touch the
	// workflow keeps it, and plans waiting for approval carry on.
	DeploymentBuild = "deployment-v2"

	WorkflowDeployment = "loomplane.deployment"
	// WorkflowRetry runs a failed deployment's remaining steps again.
	WorkflowRetry  = "loomplane.deployment.retry"
	ActivityPlan   = "loomplane.plan"
	ActivityDecide = "loomplane.plan.decide"
	ActivityExpire = "loomplane.plan.expire"
	ActivityFail   = "loomplane.plan.fail"

	// Activities after approval: record the deployment, store the source,
	// carry out each action, settle it.
	ActivityDeployStart  = "loomplane.deploy.start"
	ActivityDeploySource = "loomplane.deploy.source"
	ActivityDeployAction = "loomplane.deploy.action"
	ActivityDeployFinish = "loomplane.deploy.finish"

	// SignalDecision carries an approval or a rejection.
	SignalDecision = "decision"

	// PlanReady is the status of a plan waiting for a decision, as
	// internal/planning names it.
	PlanReady = "ready"
	// PlanFailed is the status of a plan whose planning failed.
	PlanFailed = "failed"
	// PlanApproved is the status of an approved plan.
	PlanApproved = "approved"

	// runID is the only run of each plan's workflow.
	runID = "1"
	// maxDecisions bounds how many decisions a plan waits through when
	// each one fails to apply, such as one naming an old revision.
	maxDecisions = 20
	// activityAttempts covers a database restart or a deploy of Loomplane.
	activityAttempts = 8
	activityTimeout  = time.Minute

	// An action builds and deploys, which can take a while; its executor
	// heartbeats as it polls the cloud, and a retry resumes the cloud's
	// operation from the last heartbeat.
	actionTimeout   = 45 * time.Minute
	actionHeartbeat = 3 * time.Minute
	actionAttempts  = 3
	sourceTimeout   = 15 * time.Minute
)

// DeploymentInput starts a plan's workflow.
type DeploymentInput struct {
	OrgID  string `json:"org_id"`
	PlanID string `json:"plan_id"`
	// Window is how long a ready plan waits for a decision. It's part of
	// the input rather than read from configuration, so a replay waits
	// exactly as long as the first run did.
	Window time.Duration `json:"window"`
}

// Decision is an approval or a rejection, as the signal carries it.
type Decision struct {
	Approve bool `json:"approve"`
	// Revision is the plan revision the decider reviewed.
	Revision string `json:"revision"`
	By       string `json:"by"`
	Note     string `json:"note,omitempty"`
}

type decideInput struct {
	DeploymentInput
	Decision Decision `json:"decision"`
}

type failInput struct {
	DeploymentInput
	Reason string `json:"reason"`
}

// Outcome is where a plan's workflow left it.
type Outcome struct {
	Status string `json:"status"`
	// Deployment is the deployment an approved plan started, if any.
	Deployment string `json:"deployment,omitempty"`
}

// Deployer carries out approved plans. *deployments.Runner is the real
// one. Each method is idempotent, like the Planner's.
type Deployer interface {
	// Start records the plan's deployment and lists its actions by plan
	// step. No deployment means nothing to carry out.
	Start(ctx context.Context, orgID, planID string) (deploymentID string, steps [][]string, err error)
	FetchSource(ctx context.Context, orgID, deploymentID string) error
	// Run carries out one action; resume is the last heartbeat's details.
	Run(ctx context.Context, orgID, deploymentID, actionID string, resume []byte, heartbeat func([]byte) error) error
	// Finish settles the deployment: failed with reason, or succeeded.
	Finish(ctx context.Context, orgID, deploymentID, reason string) error
}

type deployStarted struct {
	DeploymentID string     `json:"deployment_id"`
	Steps        [][]string `json:"steps"`
}

type deployInput struct {
	OrgID        string `json:"org_id"`
	DeploymentID string `json:"deployment_id"`
	ActionID     string `json:"action_id,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// Planner does the deployment workflow's work. *planning.Service is the
// real one. Each method is idempotent: an activity may run more than once.
type Planner interface {
	// Plan computes a planning plan and returns its new status: ready,
	// blocked or failed.
	Plan(ctx context.Context, orgID, planID string) (string, error)
	// ApplyDecision settles a ready plan and returns its status. A
	// decision that doesn't apply leaves it ready.
	ApplyDecision(ctx context.Context, orgID, planID string, d Decision) (string, error)
	// Expire settles a plan nobody decided in time.
	Expire(ctx context.Context, orgID, planID string) (string, error)
	// Fail records why planning couldn't finish.
	Fail(ctx context.Context, orgID, planID, reason string) error
}

// ErrNotRunning is returned for a signal or cancellation of a plan whose
// workflow has finished.
var ErrNotRunning = errors.New("the plan's workflow has finished")

// Deployments is the deployment workflow: its handlers, and how to start
// and signal it.
//
// Dispatch needs the handlers when its extension is built, which is before
// the extension that owns the planner exists. So the handlers find the
// planner here when they run, and the planning extension binds it while it
// registers. Nothing runs before then: Dispatch only takes work once every
// extension has started.
type Deployments struct {
	cloudSync      CloudReconciler
	infrastructure InfrastructureReconciler
	mu             sync.RWMutex
	planner        Planner
	deployer       Deployer
	engineOf       func() *engine.Engine
}

// NewDeployments returns the workflow, unbound.
func NewDeployments() *Deployments { return &Deployments{} }

// Bind gives the workflow its planner and deployer, and the engine to
// start runs on.
func (d *Deployments) Bind(p Planner, dep Deployer, engineOf func() *engine.Engine) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.planner, d.deployer, d.engineOf = p, dep, engineOf
}

func (d *Deployments) boundDeployer() (Deployer, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.deployer == nil {
		return nil, errors.New("the deployment workflow has no deployer yet")
	}
	return d.deployer, nil
}

func (d *Deployments) bound() (Planner, func() *engine.Engine, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.planner == nil || d.engineOf == nil {
		return nil, nil, errors.New("the deployment workflow has no planner yet")
	}
	return d.planner, d.engineOf, nil
}

// Options are the durable runtime options for the dispatch extension:
// this workflow's handlers, routed to its queue and build.
func (d *Deployments) Options(owner string, concurrency int) drt.Options {
	return drt.Options{
		Namespace: DurableNamespace, Queue: DurableQueue, BuildID: DeploymentBuild, Owner: owner,
		Concurrency: concurrency,
		Workflows:   map[string]drt.WorkflowFunc{WorkflowDeployment: d.workflow, WorkflowRetry: d.retry, cloudSyncWorkflow: d.syncWorkflow, infrastructureWorkflow: d.infrastructureRun, resumeReleaseWorkflow: d.resumeRelease, routeWorkflow: d.routeRun, registrationWorkflow: d.registrationRun},
		Activities: map[string]drt.ActivityFunc{
			ActivityPlan:      d.plan,
			cloudSyncActivity: d.syncActivity, infrastructureActivity: d.infrastructureReconcile, releaseAvailableActivity: d.releaseAvailable, routeActivity: d.routeReconcile, registrationActivity: d.registrationReconcile,
			ActivityDecide: d.decide,
			ActivityExpire: d.expire,
			ActivityFail:   d.fail,

			ActivityDeployStart:  d.deployStart,
			ActivityDeploySource: d.deploySource,
			ActivityDeployAction: d.deployAction,
			ActivityDeployFinish: d.deployFinish,
		},
	}
}

// workflow is the deployment workflow. It must stay deterministic: every
// read and write goes through an activity, and command IDs depend only on
// what came before.
func (d *Deployments) workflow(w *drt.Workflow, input []byte) ([]byte, error) {
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
			return deploy(w, in)
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

func finished(status string) ([]byte, error) {
	return json.Marshal(Outcome{Status: status})
}

// deploy carries out an approved plan: its actions one at a time, in the
// plan's order. A failed action stops the deployment and settles it as
// failed; the workflow itself still finishes, approved.
func deploy(w *drt.Workflow, in DeploymentInput) ([]byte, error) {
	var started deployStarted
	if err := callInto(w, "deploy-start", ActivityDeployStart, in, activityTimeout, 0, activityAttempts, &started); err != nil {
		return nil, err
	}
	if started.DeploymentID == "" {
		return finished(PlanApproved)
	}
	if err := runSteps(w, in.OrgID, started); err != nil {
		return nil, err
	}
	return json.Marshal(Outcome{Status: PlanApproved, Deployment: started.DeploymentID})
}

// runSteps stores the source, runs each action in order until one fails,
// and settles the deployment. Actions that already succeeded return at
// once, which is what lets a retry run the same steps again.
func runSteps(w *drt.Workflow, orgID string, started deployStarted) error {
	dep := deployInput{OrgID: orgID, DeploymentID: started.DeploymentID}
	reason := ""
	if err := callInto(w, "deploy-source", ActivityDeploySource, dep, sourceTimeout, 0, activityAttempts, nil); err != nil {
		reason = "The source couldn't be stored: " + err.Error()
	}
	for _, group := range started.Steps {
		if reason != "" {
			break
		}
		for _, id := range group {
			step := dep
			step.ActionID = id
			if err := callInto(w, "deploy-"+id, ActivityDeployAction, step, actionTimeout, actionHeartbeat, actionAttempts, nil); err != nil {
				reason = err.Error()
				break
			}
		}
	}
	done := dep
	done.Reason = reason
	return callInto(w, "deploy-finish", ActivityDeployFinish, done, activityTimeout, 0, activityAttempts, nil)
}

// retryInput restarts a failed deployment's remaining steps.
type retryInput struct {
	OrgID        string     `json:"org_id"`
	DeploymentID string     `json:"deployment_id"`
	Steps        [][]string `json:"steps"`
}

// retry is the retry workflow: the deployment was reopened before it
// started, so it runs the steps again, and those that succeeded the first
// time return at once.
func (d *Deployments) retry(w *drt.Workflow, input []byte) ([]byte, error) {
	var in retryInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("decode retry input: %w", err)
	}
	if err := runSteps(w, in.OrgID, deployStarted{DeploymentID: in.DeploymentID, Steps: in.Steps}); err != nil {
		return nil, err
	}
	return json.Marshal(Outcome{Status: PlanApproved, Deployment: in.DeploymentID})
}

// Retry starts a reopened deployment's steps again. retryID names this
// retry, so a repeated request starts it once.
func (d *Deployments) Retry(ctx context.Context, orgID, deploymentID, retryID string, steps [][]string) error {
	eng, err := d.engine()
	if err != nil {
		return err
	}
	input, err := json.Marshal(retryInput{OrgID: orgID, DeploymentID: deploymentID, Steps: steps})
	if err != nil {
		return fmt.Errorf("encode retry input: %w", err)
	}
	_, err = eng.StartDurableWorkflow(ctx, durable.StartRequest{
		Key:       durable.Key{Namespace: DurableNamespace, WorkflowID: retryID, RunID: runID},
		RequestID: "start:" + retryID, WorkflowType: WorkflowRetry, BuildID: DeploymentBuild,
		Queue: DurableQueue, Input: input,
	})
	if err != nil {
		return fmt.Errorf("start retry workflow: %w", err)
	}
	return nil
}

// callInto runs an activity and decodes its output into out, if out isn't
// nil.
func callInto(w *drt.Workflow, id, name string, in any, timeout, heartbeat time.Duration, attempts int64, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("encode %s input: %w", name, err)
	}
	res, err := w.ActivityWithOptions(id, name, "", data, drt.ActivityOptions{
		StartToCloseTimeout: timeout, HeartbeatTimeout: heartbeat,
		RetryPolicy: &drt.RetryPolicy{InitialInterval: time.Second, BackoffCoefficient: 2, MaximumAttempts: attempts},
	}).Get()
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(res, out); err != nil {
		return fmt.Errorf("decode %s output: %w", name, err)
	}
	return nil
}

// call runs an activity and decodes its outcome.
func call(w *drt.Workflow, id, name string, in any) (string, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return "", fmt.Errorf("encode %s input: %w", name, err)
	}
	out, err := w.ActivityWithOptions(id, name, "", data, drt.ActivityOptions{
		StartToCloseTimeout: activityTimeout,
		RetryPolicy:         &drt.RetryPolicy{InitialInterval: time.Second, BackoffCoefficient: 2, MaximumAttempts: activityAttempts},
	}).Get()
	if err != nil {
		return "", err
	}
	var o Outcome
	if err := json.Unmarshal(out, &o); err != nil {
		return "", fmt.Errorf("decode %s outcome: %w", name, err)
	}
	return o.Status, nil
}

func (d *Deployments) plan(ctx context.Context, _ drt.ActivityInfo, input []byte) ([]byte, error) {
	var in DeploymentInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	p, _, err := d.bound()
	if err != nil {
		return nil, err
	}
	status, err := p.Plan(ctx, in.OrgID, in.PlanID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Outcome{Status: status})
}

func (d *Deployments) decide(ctx context.Context, _ drt.ActivityInfo, input []byte) ([]byte, error) {
	var in decideInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	p, _, err := d.bound()
	if err != nil {
		return nil, err
	}
	status, err := p.ApplyDecision(ctx, in.OrgID, in.PlanID, in.Decision)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Outcome{Status: status})
}

func (d *Deployments) expire(ctx context.Context, _ drt.ActivityInfo, input []byte) ([]byte, error) {
	var in DeploymentInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	p, _, err := d.bound()
	if err != nil {
		return nil, err
	}
	status, err := p.Expire(ctx, in.OrgID, in.PlanID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Outcome{Status: status})
}

func (d *Deployments) fail(ctx context.Context, _ drt.ActivityInfo, input []byte) ([]byte, error) {
	var in failInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	p, _, err := d.bound()
	if err != nil {
		return nil, err
	}
	if err := p.Fail(ctx, in.OrgID, in.PlanID, in.Reason); err != nil {
		return nil, err
	}
	return json.Marshal(Outcome{Status: PlanFailed})
}

func (d *Deployments) engine() (*engine.Engine, error) {
	_, engineOf, err := d.bound()
	if err != nil {
		return nil, err
	}
	eng := engineOf()
	if eng == nil {
		return nil, errors.New("dispatch has no engine")
	}
	return eng, nil
}

// Start starts a plan's workflow. Starting one twice is harmless: the
// request ID is the plan's, so Dispatch returns the first start's receipt.
func (d *Deployments) Start(ctx context.Context, orgID, planID string, window time.Duration) error {
	eng, err := d.engine()
	if err != nil {
		return err
	}
	input, err := json.Marshal(DeploymentInput{OrgID: orgID, PlanID: planID, Window: window})
	if err != nil {
		return fmt.Errorf("encode deployment input: %w", err)
	}
	_, err = eng.StartDurableWorkflow(ctx, durable.StartRequest{
		Key:       durable.Key{Namespace: DurableNamespace, WorkflowID: planID, RunID: runID},
		RequestID: "start:" + planID, WorkflowType: WorkflowDeployment, BuildID: DeploymentBuild,
		Queue: DurableQueue, Input: input,
	})
	if err != nil {
		return fmt.Errorf("start deployment workflow: %w", err)
	}
	return nil
}

// Decide signals a plan's workflow with a decision. requestID makes a
// retried request safe: Dispatch accepts each ID once.
func (d *Deployments) Decide(ctx context.Context, planID, requestID string, dec Decision) error {
	eng, err := d.engine()
	if err != nil {
		return err
	}
	input, err := json.Marshal(dec)
	if err != nil {
		return fmt.Errorf("encode decision: %w", err)
	}
	_, err = eng.SignalDurableWorkflow(ctx, durable.SignalRequest{
		Key:       durable.Key{Namespace: DurableNamespace, WorkflowID: planID, RunID: runID},
		RequestID: requestID, BuildID: DeploymentBuild, Name: SignalDecision, Input: input,
	})
	if errors.Is(err, durable.ErrClosed) || errors.Is(err, durable.ErrNotFound) {
		return ErrNotRunning
	}
	if err != nil {
		return fmt.Errorf("signal deployment workflow: %w", err)
	}
	return nil
}

// Cancel stops a plan's workflow, for a plan another one superseded.
func (d *Deployments) Cancel(ctx context.Context, planID, reason string) error {
	eng, err := d.engine()
	if err != nil {
		return err
	}
	_, err = eng.RequestCancelDurableWorkflow(ctx, durable.CancelExecutionRequest{
		Key:       durable.Key{Namespace: DurableNamespace, WorkflowID: planID, RunID: runID},
		RequestID: "cancel:" + planID, BuildID: DeploymentBuild, Reason: reason,
	})
	if errors.Is(err, durable.ErrClosed) || errors.Is(err, durable.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cancel deployment workflow: %w", err)
	}
	return nil
}

func (d *Deployments) deployStart(ctx context.Context, _ drt.ActivityInfo, input []byte) ([]byte, error) {
	var in DeploymentInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	dep, err := d.boundDeployer()
	if err != nil {
		return nil, err
	}
	id, steps, err := dep.Start(ctx, in.OrgID, in.PlanID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(deployStarted{DeploymentID: id, Steps: steps})
}

func (d *Deployments) deploySource(ctx context.Context, _ drt.ActivityInfo, input []byte) ([]byte, error) {
	var in deployInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	dep, err := d.boundDeployer()
	if err != nil {
		return nil, err
	}
	return []byte("{}"), dep.FetchSource(ctx, in.OrgID, in.DeploymentID)
}

func (d *Deployments) deployAction(ctx context.Context, info drt.ActivityInfo, input []byte) ([]byte, error) {
	var in deployInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	dep, err := d.boundDeployer()
	if err != nil {
		return nil, err
	}
	heartbeat := func(details []byte) error { return info.Heartbeat(ctx, details) }
	ctx = realtime.WithActivityAttempt(ctx, info.Attempt)
	return []byte("{}"), dep.Run(ctx, in.OrgID, in.DeploymentID, in.ActionID, info.HeartbeatDetails(), heartbeat)
}

func (d *Deployments) deployFinish(ctx context.Context, _ drt.ActivityInfo, input []byte) ([]byte, error) {
	var in deployInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	dep, err := d.boundDeployer()
	if err != nil {
		return nil, err
	}
	return []byte("{}"), dep.Finish(ctx, in.OrgID, in.DeploymentID, in.Reason)
}
