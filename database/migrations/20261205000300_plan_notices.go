package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A plan that turns ready tells the people who can approve it, minus the
// person who made it, after a two minute wait so nobody is pinged for a plan
// they are already looking at. Plans made by Loomplane's own automation skip
// the wait. A plan that leaves ready queues a push-only notice for each of
// those rows, so a phone that showed Approve can be told it was decided.
//
// The roles are the ones security.MemberRolesWith(DeploymentApprove) lists,
// and a Go test pins that list. The 'owner','admin' list in the function
// below must match security.MemberRolesWith(deployment:approve).
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			if _, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_plan_notice() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF OLD.status='planning' AND NEW.status='ready' THEN
		INSERT INTO loomplane_notification_outbox(id,org_id,project_id,user_id,deployment_id,attempt,payload,kind,subject_id,inbox_done,webhook_done,email_state,next_attempt_at)
		SELECT 'plan.waiting:'||NEW.id||':'||m.user_id,NEW.org_id,NEW.project_id,m.user_id,'plan.waiting:'||NEW.id||':'||m.user_id,0,
			jsonb_build_object('plan_id',NEW.id,'project_id',NEW.project_id,'environment_id',NEW.environment_id,'environment',NEW.environment,'revision',NEW.revision),
			'plan.waiting',NEW.id,FALSE,TRUE,'pending',
			CASE WHEN NEW.created_by LIKE 'loomplane:%' THEN clock_timestamp() ELSE clock_timestamp()+interval '2 minutes' END
		FROM (SELECT DISTINCT user_id FROM authsome_members WHERE org_id=NEW.org_id AND role IN ('owner','admin') AND user_id<>NEW.created_by) m
		ON CONFLICT(deployment_id,attempt) DO NOTHING;
	ELSIF OLD.status='ready' THEN
		INSERT INTO loomplane_notification_outbox(id,org_id,project_id,user_id,deployment_id,attempt,payload,kind,subject_id,inbox_done,webhook_done,email_state)
		SELECT 'plan.decided:'||NEW.id||':'||w.user_id,NEW.org_id,NEW.project_id,w.user_id,'plan.decided:'||NEW.id||':'||w.user_id,0,
			jsonb_build_object('plan_id',NEW.id,'project_id',NEW.project_id,'environment_id',NEW.environment_id,'environment',NEW.environment,'revision',NEW.revision,'status',NEW.status,'decided_by',NEW.decided_by),
			'plan.decided',NEW.id,TRUE,TRUE,'skipped'
		FROM loomplane_notification_outbox w WHERE w.kind='plan.waiting' AND w.subject_id=NEW.id
		ON CONFLICT(deployment_id,attempt) DO NOTHING;
	END IF;
	RETURN NULL;
EXCEPTION WHEN OTHERS THEN
	-- A notice must never fail the plan write that caused it, but a dropped
	-- notice leaves a trace. Error text and code only, never row data.
	RAISE WARNING 'loomplane_plan_notice failed: % (SQLSTATE %)', SQLERRM, SQLSTATE;
	RETURN NULL;
END $$`); err != nil {
				return fmt.Errorf("create function loomplane_plan_notice: %w", err)
			}
			if _, err := db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_plan_notice
AFTER UPDATE OF status ON loomplane_plans
FOR EACH ROW WHEN (OLD.status IS DISTINCT FROM NEW.status)
EXECUTE FUNCTION loomplane_plan_notice()`); err != nil {
				return fmt.Errorf("create trigger loomplane_plan_notice: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			if _, err := db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_plan_notice ON loomplane_plans`); err != nil {
				return fmt.Errorf("drop trigger loomplane_plan_notice: %w", err)
			}
			if _, err := db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_plan_notice()`); err != nil {
				return fmt.Errorf("drop function loomplane_plan_notice: %w", err)
			}
			return nil
		},
	)
}
