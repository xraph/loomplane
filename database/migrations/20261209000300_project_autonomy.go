package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// How much the AI agent may do on its own in a project: whether it may
// promote a release it was asked for to production without asking first,
// and how long the release must have soaked in staging before it may.
// Every project starts at "ask first", with no minimum soak. The soak is
// at most a week; the check is added NOT VALID and then validated, so the
// table is never locked against writes for a full scan.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_projects").
				AddColumn("autonomous_promotion", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).IfNotExists().
				AddColumn("min_staging_soak_seconds", migrate.BigInt, migrate.NotNull(), migrate.Default(0)).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}
			_, err = db.Exec(ctx, `DO $$
BEGIN
	IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='loomplane_projects_min_staging_soak') THEN
		ALTER TABLE loomplane_projects ADD CONSTRAINT loomplane_projects_min_staging_soak
			CHECK (min_staging_soak_seconds BETWEEN 0 AND 604800) NOT VALID;
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("add constraint loomplane_projects_min_staging_soak: %w", err)
			}
			if _, err = db.Exec(ctx, `ALTER TABLE loomplane_projects VALIDATE CONSTRAINT loomplane_projects_min_staging_soak`); err != nil {
				return fmt.Errorf("validate constraint loomplane_projects_min_staging_soak: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			if _, err := db.Exec(ctx, `ALTER TABLE loomplane_projects DROP CONSTRAINT IF EXISTS loomplane_projects_min_staging_soak`); err != nil {
				return fmt.Errorf("drop constraint loomplane_projects_min_staging_soak: %w", err)
			}
			return db.NewAlterTable("loomplane_projects").
				DropColumn("min_staging_soak_seconds").IfExists().
				DropColumn("autonomous_promotion").IfExists().
				Exec(ctx)
		},
	)
}
