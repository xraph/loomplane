package orchestration

import (
	"context"
	"time"

	"github.com/xraph/loomplane/internal/releasehealth"
)

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
	// reason.
	CanaryRevert(ctx context.Context, orgID, deploymentID, reason string) error
}
