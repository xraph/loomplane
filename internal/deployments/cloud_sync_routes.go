package deployments

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// ReconcileCloudPlanRoutes keeps the source runtime until every participant is ready.
// The common finalizer reclaims the accepted attempt and resumes its exact handoffs.
func (r *Runner) ReconcileCloudPlanRoutes(ctx context.Context, org, planID string, checkpoint func(cloudprofiles.SyncRoute) error, heartbeat func([]byte) error) error {
	ctx, _, stop := keepHeartbeat(ctx, nil, heartbeat, 30*time.Second)
	defer stop()
	plan, err := r.plans.GetPlan(ctx, org, planID)
	if err != nil {
		return err
	}
	if plan.CloudSyncID == "" {
		return errors.New("routing requires a Cloud sync plan")
	}
	authorizer, ok := r.cloudSyncAuth.(interface {
		ValidateCloudSyncRoutes(context.Context, *planning.Plan) (security.Principal, error)
	})
	if !ok {
		return errors.New("the Cloud routing authorization is unavailable")
	}
	actor, err := authorizer.ValidateCloudSyncRoutes(ctx, plan)
	if err != nil {
		return err
	}
	d, err := r.store.ForPlan(ctx, org, planID)
	if err != nil {
		return err
	}
	if err := reviewedActionsComplete(d, plan); err != nil {
		return err
	}
	for _, a := range plan.Actions {
		if len(a.Routes) == 0 {
			continue
		}
		if err := r.finalizeReleaseAction(ctx, d, plan, a, false, checkpoint, &actor); err != nil {
			return err
		}
	}
	return nil
}
