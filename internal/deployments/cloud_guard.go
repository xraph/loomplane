package deployments

import "context"

// EnvironmentAvailable observes the same database guard enforced by insertion
// and retry triggers. The trigger remains authoritative after this read.
func (s *PgStore) EnvironmentAvailable(ctx context.Context, org, environment, planID string) (bool, error) {
	var available bool
	err := s.db.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM loomplane_environment_sync_guards g WHERE g.org_id=$1 AND g.environment_id=$2 AND g.sync_id<>coalesce((SELECT result->>'cloud_sync_id' FROM loomplane_plans WHERE id=$3),'')) AND NOT EXISTS(SELECT 1 FROM loomplane_deployments WHERE org_id=$1 AND environment_id=$2 AND status='running' AND plan_id<>$3)`, org, environment, planID).Scan(&available)
	return available, err
}

// ReleaseAvailable lets queued durable work wait without starting a deployment.
func (r *Runner) ReleaseAvailable(ctx context.Context, org, planID string) (bool, error) {
	plan, err := r.plans.GetPlan(ctx, org, planID)
	if err != nil {
		return false, err
	}
	guard, ok := r.store.(interface {
		EnvironmentAvailable(context.Context, string, string, string) (bool, error)
	})
	if !ok {
		return true, nil
	}
	return guard.EnvironmentAvailable(ctx, org, plan.EnvironmentID, plan.ID)
}
