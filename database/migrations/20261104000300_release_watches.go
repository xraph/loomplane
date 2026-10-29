package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A release watch is one succeeded release's post-deploy watch: which
// services it judges, its verdicts, and the automatic rollback a breach
// planned. A deployment has at most one. Each insert, delete and save is
// a realtime release_watch event, through the generic state event trigger,
// whose function this leaves as it is. A save bumps revision; a tick that
// only claims a watch stamps checked_at without one, and emits nothing.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_release_watches").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_environments", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("environment", migrate.Text, migrate.NotNull()).
				Column("deployment_id", migrate.Text, migrate.NotNull(), migrate.Unique(),
					migrate.References("loomplane_deployments", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("plan_id", migrate.Text, migrate.NotNull()).
				Column("status", migrate.Text, migrate.NotNull(),
					migrate.Check("status IN ('watching','passed','inconclusive','superseded','rolling_back','rolled_back','rollback_blocked','rollback_failed')")).
				Column("verdict", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("reason", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("thresholds", migrate.JSON, migrate.NotNull()).
				Column("services", migrate.JSON, migrate.NotNull()).
				Column("unwatched", migrate.JSON, migrate.NotNull(), migrate.Default("[]")).
				Column("held", migrate.JSON, migrate.NotNull(), migrate.Default("[]")).
				Column("held_released", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("unverified", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("rollback_plan_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("rollback_approved_at", migrate.Timestamp).
				Column("starts_at", migrate.Timestamp, migrate.NotNull()).
				Column("ends_at", migrate.Timestamp, migrate.NotNull()).
				Column("bound_at", migrate.Timestamp, migrate.NotNull()).
				Column("checked_at", migrate.Timestamp).
				Column("settled_at", migrate.Timestamp).
				Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Default(1)).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Exec(ctx)
			if err != nil {
				return err
			}

			// The tick reads open watches; the history reads an
			// environment's newest.
			err = db.NewCreateIndex("loomplane_release_watches_open").IfNotExists().
				On("loomplane_release_watches").
				Columns("org_id", "environment_id").
				Where("status IN ('watching','rolling_back')").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_release_watches_held").IfNotExists().
				On("loomplane_release_watches").
				Columns("status").
				Where("NOT held_released AND held <> '[]'").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_release_watches_environment").IfNotExists().
				On("loomplane_release_watches").
				Columns("org_id", "environment_id", "starts_at").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_state_event
AFTER INSERT OR DELETE ON loomplane_release_watches
FOR EACH ROW EXECUTE FUNCTION loomplane_state_event_trigger('release_watch')`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_state_event on loomplane_release_watches: %w", err)
			}
			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_state_event_update
AFTER UPDATE ON loomplane_release_watches
FOR EACH ROW WHEN (OLD.revision IS DISTINCT FROM NEW.revision)
EXECUTE FUNCTION loomplane_state_event_trigger('release_watch')`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_state_event_update on loomplane_release_watches: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $$ BEGIN
	IF to_regclass('loomplane_release_watches') IS NOT NULL THEN
		DROP TRIGGER IF EXISTS loomplane_state_event_update ON loomplane_release_watches;
		DROP TRIGGER IF EXISTS loomplane_state_event ON loomplane_release_watches;
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_state_event on loomplane_release_watches: %w", err)
			}
			return db.NewDropTable("loomplane_release_watches").IfExists().Exec(ctx)
		},
	)
}
