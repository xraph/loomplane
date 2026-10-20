package migrations

import "github.com/xraph/grove/migrate"

// NotificationsMigration captures future deployment completions atomically.
// Historical deployments are deliberately not mailed when this is installed.
func NotificationsMigration() *migrate.Migration {
	return statements("20261018000500", "deployment_notifications", notificationsUp, notificationsDown)
}

var notificationsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_notification_preferences (
 org_id TEXT NOT NULL,user_id TEXT NOT NULL,deployment_email BOOLEAN NOT NULL DEFAULT TRUE,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(org_id,user_id))`,
	`CREATE TABLE IF NOT EXISTS loomplane_notification_outbox (
 id TEXT PRIMARY KEY,org_id TEXT NOT NULL,project_id TEXT NOT NULL,user_id TEXT NOT NULL,
 deployment_id TEXT NOT NULL,attempt INTEGER NOT NULL,payload JSONB NOT NULL,wire_payload JSONB,
 inbox_id TEXT NOT NULL DEFAULT '',inbox_done BOOLEAN NOT NULL DEFAULT FALSE,
 webhook_done BOOLEAN NOT NULL DEFAULT FALSE,email_state TEXT NOT NULL DEFAULT 'pending',
 attempts INTEGER NOT NULL DEFAULT 0,next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 lease_until TIMESTAMPTZ,lease_owner TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',
 completed_at TIMESTAMPTZ,created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(deployment_id,attempt))`,
	`CREATE INDEX IF NOT EXISTS loomplane_notification_outbox_pending ON loomplane_notification_outbox(next_attempt_at) WHERE completed_at IS NULL`,
	`CREATE OR REPLACE FUNCTION loomplane_notification_capture() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.status NOT IN ('succeeded','failed') THEN RETURN NULL; END IF;
 IF TG_OP='UPDATE' AND OLD.status=NEW.status AND OLD.retries=NEW.retries THEN RETURN NULL; END IF;
 INSERT INTO loomplane_notification_outbox(id,org_id,project_id,user_id,deployment_id,attempt,payload)
 VALUES(NEW.id||':'||NEW.retries,NEW.org_id,NEW.project_id,NEW.started_by,NEW.id,NEW.retries,
 jsonb_build_object('deployment_id',NEW.id,'project_id',NEW.project_id,'environment_id',NEW.environment_id,
 'plan_id',NEW.plan_id,'status',NEW.status,'attempt',NEW.retries)) ON CONFLICT(deployment_id,attempt) DO NOTHING;
 RETURN NULL; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_notification_capture AFTER INSERT OR UPDATE ON loomplane_deployments FOR EACH ROW EXECUTE FUNCTION loomplane_notification_capture()`,
}
var notificationsDown = []string{
	`DROP TRIGGER IF EXISTS loomplane_notification_capture ON loomplane_deployments`,
	`DROP FUNCTION IF EXISTS loomplane_notification_capture()`,
	`DROP TABLE IF EXISTS loomplane_notification_outbox`,
	`DROP TABLE IF EXISTS loomplane_notification_preferences`,
}
