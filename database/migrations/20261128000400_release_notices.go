package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// The outbox allows one row per kind, subject and person, so subject_id here
// is the deployment, its retry count and, for progress, the step. The push
// still names the deployment itself as its subject.
//
// A canary that moves, rolls back or can't roll back tells the people who
// care. The starter and the approver (never Loomplane's own automation) hear
// about progress and rollbacks, and owners and admins hear about rollbacks
// and failed rollbacks. Progress after the first step is push-only, so a
// canary leaves one inbox item however many steps it takes.
//
// A production deployment that fails also reaches every owner and admin
// other than the starter. Each copy is webhook_done, because the starter's
// row sends the one webhook.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			if _, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_release_notice() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
	v_status TEXT := NEW.canary->>'status';
	v_old TEXT := OLD.canary->>'status';
	v_step INT := COALESCE((NEW.canary->>'step')::int,-1);
	v_old_step INT := COALESCE((OLD.canary->>'step')::int,-1);
	v_count INT := COALESCE(jsonb_array_length(NEW.canary->'steps'),0);
	v_kind TEXT; v_who TEXT; v_suffix TEXT := ''; v_inbox_done BOOLEAN := FALSE; v_email TEXT := 'pending';
	v_weight INT := 0; v_previous INT := 0;
BEGIN
	IF v_status IS NULL THEN RETURN NULL; END IF;
	IF v_status='rolled_back' AND v_old IS DISTINCT FROM v_status THEN
		v_kind := 'release.rolled_back'; v_who := 'all';
		IF v_step>=0 THEN v_weight := COALESCE((NEW.canary->'steps'->>v_step)::int,0); END IF;
	ELSIF v_status='revert_failed' AND v_old IS DISTINCT FROM v_status THEN
		v_kind := 'release.revert_failed'; v_who := 'admins';
		IF v_step>=0 THEN v_weight := COALESCE((NEW.canary->'steps'->>v_step)::int,0); END IF;
	ELSIF v_status='ramping' AND v_step>=0 AND (v_old IS DISTINCT FROM 'ramping' OR v_step>v_old_step) THEN
		v_kind := 'release.progress'; v_who := 'sp'; v_suffix := ':'||v_step; v_email := 'skipped';
		v_inbox_done := v_step>0;
		v_weight := COALESCE((NEW.canary->>'weight')::int,(NEW.canary->'steps'->>v_step)::int,0);
		IF v_step>0 THEN v_previous := COALESCE((NEW.canary->'steps'->>(v_step-1))::int,0); END IF;
	ELSIF v_status='promoted' AND v_old IS DISTINCT FROM v_status THEN
		v_kind := 'release.progress'; v_who := 'sp'; v_suffix := ':'||v_count; v_email := 'skipped'; v_inbox_done := TRUE;
		v_step := v_count; v_weight := 100;
		IF v_count>0 THEN v_previous := COALESCE((NEW.canary->'steps'->>(v_count-1))::int,0); END IF;
	ELSE
		RETURN NULL;
	END IF;
	INSERT INTO loomplane_notification_outbox(id,org_id,project_id,user_id,deployment_id,attempt,payload,kind,subject_id,inbox_done,webhook_done,email_state)
	SELECT v_kind||':'||NEW.id||':'||NEW.retries||v_suffix||':'||r.user_id,NEW.org_id,NEW.project_id,r.user_id,
		v_kind||':'||NEW.id||':'||NEW.retries||v_suffix||':'||r.user_id,0,
		jsonb_build_object('deployment_id',NEW.id,'project_id',NEW.project_id,'environment_id',NEW.environment_id,'plan_id',NEW.plan_id,
			'status',v_status,'step',v_step,'weight',v_weight,'previous_weight',v_previous,'reason',COALESCE(NEW.canary->>'reason','')),
		v_kind,NEW.id||':'||NEW.retries||v_suffix,v_inbox_done,TRUE,v_email
	FROM (
		SELECT NEW.started_by AS user_id WHERE v_who IN ('all','sp') AND NEW.started_by<>'' AND NEW.started_by NOT LIKE 'loomplane:%'
		UNION
		SELECT p.decided_by FROM loomplane_plans p WHERE p.id=NEW.plan_id AND v_who IN ('all','sp') AND p.decided_by<>'' AND p.decided_by NOT LIKE 'loomplane:%'
		UNION
		SELECT m.user_id FROM authsome_members m WHERE m.org_id=NEW.org_id AND m.role IN ('owner','admin') AND v_who IN ('all','admins')
	) r
	ON CONFLICT DO NOTHING;
	RETURN NULL;
END $$`); err != nil {
				return fmt.Errorf("create function loomplane_release_notice: %w", err)
			}
			if _, err := db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_release_notice
AFTER UPDATE OF canary ON loomplane_deployments
FOR EACH ROW WHEN (OLD.canary IS DISTINCT FROM NEW.canary)
EXECUTE FUNCTION loomplane_release_notice()`); err != nil {
				return fmt.Errorf("create trigger loomplane_release_notice: %w", err)
			}
			if _, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_notification_capture() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.status NOT IN ('succeeded','failed') THEN RETURN NULL; END IF;
	IF TG_OP='UPDATE' AND OLD.status=NEW.status AND OLD.retries=NEW.retries THEN RETURN NULL; END IF;

	INSERT INTO loomplane_notification_outbox(id,org_id,project_id,user_id,deployment_id,attempt,payload)
	VALUES(NEW.id||':'||NEW.retries,NEW.org_id,NEW.project_id,NEW.started_by,NEW.id,NEW.retries,
		jsonb_build_object('deployment_id',NEW.id,'project_id',NEW.project_id,'environment_id',NEW.environment_id,
		'plan_id',NEW.plan_id,'status',NEW.status,'attempt',NEW.retries))
	ON CONFLICT(deployment_id,attempt) DO NOTHING;

	IF NEW.status='failed' AND EXISTS (SELECT 1 FROM loomplane_environments e WHERE e.id=NEW.environment_id AND e.kind='production') THEN
		INSERT INTO loomplane_notification_outbox(id,org_id,project_id,user_id,deployment_id,attempt,payload,webhook_done)
		SELECT NEW.id||':'||NEW.retries||':'||m.user_id,NEW.org_id,NEW.project_id,m.user_id,NEW.id||':'||NEW.retries||':'||m.user_id,NEW.retries,
			jsonb_build_object('deployment_id',NEW.id,'project_id',NEW.project_id,'environment_id',NEW.environment_id,
			'plan_id',NEW.plan_id,'status',NEW.status,'attempt',NEW.retries),TRUE
		FROM (SELECT DISTINCT user_id FROM authsome_members WHERE org_id=NEW.org_id AND role IN ('owner','admin') AND user_id<>NEW.started_by) m
		ON CONFLICT DO NOTHING;
	END IF;

	RETURN NULL;
END $$`); err != nil {
				return fmt.Errorf("replace function loomplane_notification_capture: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			if _, err := db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_release_notice ON loomplane_deployments`); err != nil {
				return fmt.Errorf("drop trigger loomplane_release_notice: %w", err)
			}
			if _, err := db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_release_notice()`); err != nil {
				return fmt.Errorf("drop function loomplane_release_notice: %w", err)
			}
			if _, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_notification_capture() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.status NOT IN ('succeeded','failed') THEN RETURN NULL; END IF;
	IF TG_OP='UPDATE' AND OLD.status=NEW.status AND OLD.retries=NEW.retries THEN RETURN NULL; END IF;

	INSERT INTO loomplane_notification_outbox(id,org_id,project_id,user_id,deployment_id,attempt,payload)
	VALUES(NEW.id||':'||NEW.retries,NEW.org_id,NEW.project_id,NEW.started_by,NEW.id,NEW.retries,
		jsonb_build_object('deployment_id',NEW.id,'project_id',NEW.project_id,'environment_id',NEW.environment_id,
		'plan_id',NEW.plan_id,'status',NEW.status,'attempt',NEW.retries))
	ON CONFLICT(deployment_id,attempt) DO NOTHING;

	RETURN NULL;
END $$`); err != nil {
				return fmt.Errorf("restore function loomplane_notification_capture: %w", err)
			}
			return nil
		},
	)
}
