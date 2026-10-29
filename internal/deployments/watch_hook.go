package deployments

import (
	"context"
	"time"

	"github.com/xraph/loomplane/internal/planning"
)

// WatchDecision is what a post-deploy watch decided about a deployment
// before it finished.
type WatchDecision struct {
	// Armed says the deployment gets a watch once it's stored succeeded.
	Armed bool
	// Duration is how long the watch runs.
	Duration time.Duration
}

// WatchHook arms a post-deploy watch for a deployment that succeeded.
// releasewatch's Service is the real one; it lives there so this package
// doesn't import it. Both calls are idempotent: a retried finish asks
// again, and a deployment never gets two watches.
type WatchHook interface {
	// Prepare decides, before the finish cleans up its route releases,
	// whether the deployment gets a watch.
	Prepare(ctx context.Context, d *Deployment, plan *planning.Plan) (WatchDecision, error)
	// Arm records the watch for the deployment as stored once it
	// succeeded, so the watch starts when it finished.
	Arm(ctx context.Context, d *Deployment, decision WatchDecision) error
}

// prepareWatch asks the watch hook about d, when there is one.
func (r *Runner) prepareWatch(ctx context.Context, d *Deployment) (WatchDecision, error) {
	if r.watches == nil {
		return WatchDecision{}, nil
	}
	plan, err := r.plans.GetPlan(ctx, d.OrgID, d.PlanID)
	if err != nil {
		return WatchDecision{}, err
	}
	return r.watches.Prepare(ctx, d, plan)
}

// armWatch arms the watch for settled, the deployment as stored once it
// succeeded.
func (r *Runner) armWatch(ctx context.Context, settled *Deployment, decision WatchDecision) error {
	if r.watches == nil || !decision.Armed {
		return nil
	}
	return r.watches.Arm(ctx, settled, decision)
}

// rearmWatch prepares and arms the watch for a deployment a replayed
// finish found stored succeeded: the finish that stored it may have
// failed before it armed one.
func (r *Runner) rearmWatch(ctx context.Context, settled *Deployment) error {
	decision, err := r.prepareWatch(ctx, settled)
	if err != nil {
		return err
	}
	return r.armWatch(ctx, settled, decision)
}
