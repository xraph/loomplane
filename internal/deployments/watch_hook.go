package deployments

import (
	"context"
	"fmt"
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

// prepareWatch asks the watch hook about d, when there is one. A watch
// it can't prepare never fails the finish: it logs the error's type and
// the IDs, since the error's text may say more than a log should, and
// arms nothing.
func (r *Runner) prepareWatch(ctx context.Context, d *Deployment) WatchDecision {
	if r.watches == nil {
		return WatchDecision{}
	}
	plan, err := r.plans.GetPlan(ctx, d.OrgID, d.PlanID)
	var decision WatchDecision
	if err == nil {
		decision, err = r.watches.Prepare(ctx, d, plan)
	}
	if err != nil {
		if r.warn != nil {
			r.warn(fmt.Sprintf("no post-deploy watch for deployment %s of organization %s: preparing it failed with %T", d.ID, d.OrgID, err))
		}
		return WatchDecision{}
	}
	return decision
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
	return r.armWatch(ctx, settled, r.prepareWatch(ctx, settled))
}
