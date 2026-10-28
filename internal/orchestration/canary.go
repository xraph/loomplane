package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	drt "github.com/xraph/dispatch/durable/runtime"

	"github.com/xraph/loomplane/internal/releasehealth"
)

const (
	// CanaryBuild is the deployment workflow build that ramps canaries:
	// v3's steps, then the ramp.
	CanaryBuild = "deployment-v4"
	// CurrentBuild is what new plan, retry and resume-release runs start
	// on. A retry keeps its plan's build.
	CurrentBuild = CanaryBuild

	ActivityCanaryRamp    = "loomplane.canary.ramp.v4"
	ActivityCanaryWeigh   = "loomplane.canary.weigh.v4"
	ActivityCanaryJudge   = "loomplane.canary.judge.v4"
	ActivityCanaryPromote = "loomplane.canary.promote.v4"
	ActivityCanaryRevert  = "loomplane.canary.revert.v4"
	ActivityCanarySettle  = "loomplane.canary.settle.v4"

	// Weighing waits for every candidate's cloud to move its traffic.
	canaryEffectTimeout = 15 * time.Minute
	// A judgement reads each cloud's monitoring once per service.
	canaryJudgeTimeout = 5 * time.Minute
	// canarySettleRounds is how many times a promote or revert is tried,
	// a minute apart, before the ramp says traffic may still be split.
	canarySettleRounds = 60
)

// canarySettleWait is how long a promote or revert waits before it's tried
// again. Tests shorten it, so the tests that change it must not run in
// parallel with each other or with any other v4 workflow test.
var canarySettleWait = time.Minute

// canaryInput is what each canary activity after the ramp's read takes.
type canaryInput struct {
	OrgID        string               `json:"org_id"`
	DeploymentID string               `json:"deployment_id"`
	Step         int                  `json:"step,omitempty"`
	Weight       int                  `json:"weight,omitempty"`
	Attempt      int                  `json:"attempt,omitempty"`
	Window       releasehealth.Window `json:"window"`
	Reason       string               `json:"reason,omitempty"`
}

type canaryContextKey struct{}

// CanaryContext marks an activity of a workflow build that ramps canaries.
// A deployment records a canary only when it starts under this mark, so a
// run on an older build never deploys candidates that nothing would ramp
// or revert.
func CanaryContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, canaryContextKey{}, true)
}

// IsCanaryContext reports whether ctx carries CanaryContext's mark.
func IsCanaryContext(ctx context.Context) bool {
	v, ok := ctx.Value(canaryContextKey{}).(bool)
	return ok && v
}

// CanaryRamp is what the workflow ramps a deployment's canary through. No
// steps means the deployment has no canary.
type CanaryRamp struct {
	// Steps are the candidate's percent of traffic at each step.
	Steps []int `json:"steps,omitempty"`
	// Hold is how long each step is watched before it's judged.
	Hold time.Duration `json:"hold,omitempty"`
	// Lag is how much longer the first judgement of a step waits, for the
	// cloud's metrics to arrive.
	Lag time.Duration `json:"lag,omitempty"`
	// MaxInconclusive is how many more holds an inconclusive step gets
	// before it fails safe.
	MaxInconclusive int `json:"max_inconclusive,omitempty"`
}

// CanaryJudgement is one step's verdict across every service the canary
// ramps: releasehealth's pass, fail or inconclusive, and why.
type CanaryJudgement struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason,omitempty"`
}

// CanaryDeployer ramps a deployment's canary. *deployments.Runner is the
// real one. Each method is idempotent, since each is a durable activity
// that may run again.
type CanaryDeployer interface {
	// CanaryRamp reads the ramp. It's empty for a deployment without a
	// canary.
	CanaryRamp(ctx context.Context, orgID, deploymentID string) (CanaryRamp, error)
	// CanaryWeigh gives every candidate weight percent of its service's
	// traffic for step, and returns when it did.
	CanaryWeigh(ctx context.Context, orgID, deploymentID string, step, weight int) (time.Time, error)
	// CanaryJudge judges window w of a step's attempt.
	CanaryJudge(ctx context.Context, orgID, deploymentID string, step, attempt int, w releasehealth.Window) (CanaryJudgement, error)
	// CanaryPromote makes every candidate what runs.
	CanaryPromote(ctx context.Context, orgID, deploymentID string) error
	// CanaryRevert moves all traffic back to every baseline, because of
	// reason. A deployment without a canary has nothing to revert.
	CanaryRevert(ctx context.Context, orgID, deploymentID, reason string) error
	// CanarySettle ends a canary whose promote or revert the workflow gave
	// up on, whatever the deployment's status, with reason. A canary
	// already settled is left as it is.
	CanarySettle(ctx context.Context, orgID, deploymentID, reason string) error
}

// CanaryOptions are the durable runtime options of CanaryBuild: v3's
// activities, the canary's, and v4's workflows.
func (d *Deployments) CanaryOptions(owner string, concurrency int) drt.Options {
	o := d.CapacityOptions(owner, concurrency)
	o.BuildID = CanaryBuild
	o.Workflows = map[string]drt.WorkflowFunc{WorkflowDeployment: d.workflowV4, WorkflowRetry: d.retryV4, resumeReleaseWorkflow: d.resumeReleaseV4}
	o.Activities[ActivityDeployStart] = d.canaryStart
	o.Activities[ActivityCanaryRamp] = d.canaryRamp
	o.Activities[ActivityCanaryWeigh] = d.canaryWeigh
	o.Activities[ActivityCanaryJudge] = d.canaryJudge
	o.Activities[ActivityCanaryPromote] = d.canaryPromote
	o.Activities[ActivityCanaryRevert] = d.canaryRevert
	o.Activities[ActivityCanarySettle] = d.canarySettle
	return o
}

func (d *Deployments) canaryDeployer() (CanaryDeployer, error) {
	dep, err := d.boundDeployer()
	if err != nil {
		return nil, err
	}
	c, ok := dep.(CanaryDeployer)
	if !ok {
		return nil, errors.New("canary releases are unavailable")
	}
	return c, nil
}

// canaryStart records the deployment under CanaryContext, so it records a
// canary this build will ramp.
func (d *Deployments) canaryStart(ctx context.Context, info drt.ActivityInfo, raw []byte) ([]byte, error) {
	return d.deployStart(CanaryContext(CapacityContext(ctx)), info, raw)
}

func (d *Deployments) canaryRamp(ctx context.Context, _ drt.ActivityInfo, raw []byte) ([]byte, error) {
	var in deployInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	c, err := d.canaryDeployer()
	if err != nil {
		return nil, err
	}
	ramp, err := c.CanaryRamp(ctx, in.OrgID, in.DeploymentID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(ramp)
}

func (d *Deployments) canaryWeigh(ctx context.Context, _ drt.ActivityInfo, raw []byte) ([]byte, error) {
	var in canaryInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	c, err := d.canaryDeployer()
	if err != nil {
		return nil, err
	}
	applied, err := c.CanaryWeigh(ctx, in.OrgID, in.DeploymentID, in.Step, in.Weight)
	if err != nil {
		return nil, err
	}
	return json.Marshal(applied)
}

func (d *Deployments) canaryJudge(ctx context.Context, _ drt.ActivityInfo, raw []byte) ([]byte, error) {
	var in canaryInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	c, err := d.canaryDeployer()
	if err != nil {
		return nil, err
	}
	j, err := c.CanaryJudge(ctx, in.OrgID, in.DeploymentID, in.Step, in.Attempt, in.Window)
	if err != nil {
		return nil, err
	}
	return json.Marshal(j)
}

func (d *Deployments) canaryPromote(ctx context.Context, _ drt.ActivityInfo, raw []byte) ([]byte, error) {
	var in canaryInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	c, err := d.canaryDeployer()
	if err != nil {
		return nil, err
	}
	return []byte("{}"), c.CanaryPromote(ctx, in.OrgID, in.DeploymentID)
}

func (d *Deployments) canaryRevert(ctx context.Context, _ drt.ActivityInfo, raw []byte) ([]byte, error) {
	var in canaryInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	c, err := d.canaryDeployer()
	if err != nil {
		return nil, err
	}
	return []byte("{}"), c.CanaryRevert(ctx, in.OrgID, in.DeploymentID, in.Reason)
}

func (d *Deployments) canarySettle(ctx context.Context, _ drt.ActivityInfo, raw []byte) ([]byte, error) {
	var in canaryInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	c, err := d.canaryDeployer()
	if err != nil {
		return nil, err
	}
	return []byte("{}"), c.CanarySettle(ctx, in.OrgID, in.DeploymentID, in.Reason)
}
