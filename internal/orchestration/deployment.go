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
	DeploymentBuild = "deployment-v1"

	WorkflowDeployment = "loomplane.deployment"
	ActivityPlan       = "loomplane.plan"
	ActivityDecide     = "loomplane.plan.decide"
	ActivityExpire     = "loomplane.plan.expire"
	ActivityFail       = "loomplane.plan.fail"

	// SignalDecision carries an approval or a rejection.
	SignalDecision = "decision"

	// PlanReady is the status of a plan waiting for a decision, as
	// internal/planning names it.
	PlanReady = "ready"
	// PlanFailed is the status of a plan whose planning failed.
	PlanFailed = "failed"

	// runID is the only run of each plan's workflow.
	runID = "1"
	// maxDecisions bounds how many decisions a plan waits through when
	// each one fails to apply, such as one naming an old revision.
	maxDecisions = 20
	// activityAttempts covers a database restart or a deploy of Loomplane.
	activityAttempts = 8
	activityTimeout  = time.Minute
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
	mu       sync.RWMutex
	planner  Planner
	engineOf func() *engine.Engine
}

// NewDeployments returns the workflow, unbound.
func NewDeployments() *Deployments { return &Deployments{} }

// Bind gives the workflow its planner, and the engine to start runs on.
func (d *Deployments) Bind(p Planner, engineOf func() *engine.Engine) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.planner, d.engineOf = p, engineOf
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
		Workflows:   map[string]drt.WorkflowFunc{WorkflowDeployment: d.workflow},
		Activities: map[string]drt.ActivityFunc{
			ActivityPlan:   d.plan,
			ActivityDecide: d.decide,
			ActivityExpire: d.expire,
			ActivityFail:   d.fail,
		},
	}
}

// workflow is the deployment workflow. It must stay deterministic: every
// read and write goes through an activity, and command IDs depend only on
// what came before. Milestone 4 adds the steps after approval.
func (d *Deployments) workflow(w *drt.Workflow, input []byte) ([]byte, error) {
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
		if status != PlanReady {
			// Approved: milestone 4 provisions, builds and deploys here.
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
