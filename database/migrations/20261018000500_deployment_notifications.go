package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// This migration captures future deployment completions atomically.
// Historical deployments are deliberately not mailed when this is installed.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_notification_preferences").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("user_id", migrate.Text, migrate.NotNull()).
				Column("deployment_email", migrate.Boolean, migrate.NotNull(), migrate.Default(true)).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				PrimaryKey("org_id", "user_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_notification_outbox").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("user_id", migrate.Text, migrate.NotNull()).
				Column("deployment_id", migrate.Text, migrate.NotNull()).
				Column("attempt", migrate.Integer, migrate.NotNull()).
				Column("payload", migrate.JSON, migrate.NotNull()).
				Column("wire_payload", migrate.JSON).
				Column("inbox_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("inbox_done", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("webhook_done", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("email_state", migrate.Text, migrate.NotNull(), migrate.Default("pending")).
				Column("attempts", migrate.Integer, migrate.NotNull(), migrate.Default(0)).
				Column("next_attempt_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				Column("lease_until", migrate.Timestamp).
				Column("lease_owner", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("last_error", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("completed_at", migrate.Timestamp).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				Unique("deployment_id", "attempt").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_notification_outbox_pending").IfNotExists().
				On("loomplane_notification_outbox").
				Columns("next_attempt_at").
				Where("completed_at IS NULL").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_notification_capture() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.status NOT IN ('succeeded','failed') THEN RETURN NULL; END IF;
	IF TG_OP='UPDATE' AND OLD.status=NEW.status AND OLD.retries=NEW.retries THEN RETURN NULL; END IF;

	INSERT INTO loomplane_notification_outbox(id,org_id,project_id,user_id,deployment_id,attempt,payload)
	VALUES(NEW.id||':'||NEW.retries,NEW.org_id,NEW.project_id,NEW.started_by,NEW.id,NEW.retries,
		jsonb_build_object('deployment_id',NEW.id,'project_id',NEW.project_id,'environment_id',NEW.environment_id,
		'plan_id',NEW.plan_id,'status',NEW.status,'attempt',NEW.retries))
	ON CONFLICT(deployment_id,attempt) DO NOTHING;

	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_notification_capture: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_notification_capture
AFTER INSERT OR UPDATE ON loomplane_deployments
FOR EACH ROW EXECUTE FUNCTION loomplane_notification_capture()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_notification_capture: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_notification_capture ON loomplane_deployments`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_notification_capture: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_notification_capture()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_notification_capture: %w", err)
			}

			err = db.NewDropTable("loomplane_notification_outbox").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewDropTable("loomplane_notification_preferences").IfExists().Exec(ctx)
		},
	)
}

// NotificationsMigration is deployment_notifications as App registered it,
// for tests outside this package that install just the outbox.
func NotificationsMigration() *migrate.Migration {
	for _, m := range App.Migrations() {
		if m.Name == "deployment_notifications" {
			return m
		}
	}
	panic("migrations: deployment_notifications is not registered")
}
