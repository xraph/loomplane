package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// Composite keys keep every Cloud, target and assignment within its project.
// A target retains its account through a restrictive foreign key, independently
// of plans and deployment bindings.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateIndex("loomplane_projects_scope_id").IfNotExists().
				On("loomplane_projects").
				Columns("org_id", "id").
				Unique().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_cloud_profiles").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("name", migrate.Text, migrate.NotNull(), migrate.Check("length(trim(name)) BETWEEN 1 AND 64")).
				Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Check("revision>0")).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Unique("org_id", "project_id", "id").
				Unique("org_id", "project_id", "name").
				ForeignKey("org_id", "project_id").
				References("loomplane_projects", "org_id", "id").OnDelete(migrate.Cascade).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_cloud_targets").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("cloud_id", migrate.Text, migrate.NotNull()).
				Column("name", migrate.Text, migrate.NotNull(),
					migrate.Check("length(name)<=48 AND name ~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*$'")).
				Column("display_name", migrate.Text, migrate.NotNull(),
					migrate.Check("length(trim(display_name)) BETWEEN 1 AND 64")).
				Column("provider", migrate.Text, migrate.NotNull(), migrate.Check("provider<>''")).
				Column("connection_id", migrate.Text, migrate.NotNull()).
				Column("region", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Check("revision>0")).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Unique("org_id", "project_id", "cloud_id", "name").
				Unique("org_id", "project_id", "cloud_id", "id").
				ForeignKey("org_id", "project_id", "cloud_id").
				References("loomplane_cloud_profiles", "org_id", "project_id", "id").OnDelete(migrate.Cascade).
				ForeignKey("org_id", "connection_id").
				References("loomplane_cloud_connections", "org_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_cloud_targets_connection").IfNotExists().
				On("loomplane_cloud_targets").
				Columns("org_id", "connection_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_cloud_assignments").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("cloud_id", migrate.Text, migrate.NotNull()).
				Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Check("revision>0")).
				PrimaryKey("org_id", "project_id", "environment_id").
				ForeignKey("org_id", "project_id", "environment_id").
				References("loomplane_environments", "org_id", "project_id", "id").OnDelete(migrate.Cascade).
				ForeignKey("org_id", "project_id", "cloud_id").
				References("loomplane_cloud_profiles", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_cloud_assignments_cloud").IfNotExists().
				On("loomplane_cloud_assignments").
				Columns("org_id", "project_id", "cloud_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_cloud_revision_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.revision<>OLD.revision+1 THEN
		RAISE EXCEPTION 'cloud revision must increment by one' USING ERRCODE='23514';
	END IF;

	IF NEW.org_id<>OLD.org_id OR NEW.project_id<>OLD.project_id THEN
		RAISE EXCEPTION 'cloud scope is immutable' USING ERRCODE='23514';
	END IF;

	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_cloud_revision_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_cloud_profile_revision
BEFORE UPDATE ON loomplane_cloud_profiles
FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_revision_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_cloud_profile_revision: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_cloud_target_revision
BEFORE UPDATE ON loomplane_cloud_targets
FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_revision_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_cloud_target_revision: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_cloud_assignment_revision
BEFORE UPDATE ON loomplane_cloud_assignments
FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_revision_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_cloud_assignment_revision: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_cloud_target_identity_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.id<>OLD.id OR NEW.cloud_id<>OLD.cloud_id OR NEW.name<>OLD.name THEN
		RAISE EXCEPTION 'target identity is immutable' USING ERRCODE='23514';
	END IF;

	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_cloud_target_identity_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_cloud_target_identity
BEFORE UPDATE ON loomplane_cloud_targets
FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_target_identity_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_cloud_target_identity: %w", err)
			}

			// FOR UPDATE conflicts with the assignment's parent FK lock. Deletion and
			// assignment insertion cannot both succeed while observing no assignment.
			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_cloud_target_delete_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	PERFORM 1 FROM loomplane_cloud_profiles WHERE org_id=OLD.org_id AND project_id=OLD.project_id AND id=OLD.cloud_id FOR UPDATE;

	IF EXISTS(SELECT 1 FROM loomplane_cloud_assignments WHERE org_id=OLD.org_id AND project_id=OLD.project_id AND cloud_id=OLD.cloud_id)
	THEN
		RAISE EXCEPTION 'target belongs to an assigned cloud' USING ERRCODE='23503';
	END IF;

	RETURN OLD;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_cloud_target_delete_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_cloud_target_delete
BEFORE DELETE ON loomplane_cloud_targets
FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_target_delete_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_cloud_target_delete: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			if err := db.NewDropTable("loomplane_cloud_assignments").IfExists().Exec(ctx); err != nil {
				return err
			}

			if err := db.NewDropTable("loomplane_cloud_targets").IfExists().Exec(ctx); err != nil {
				return err
			}

			if err := db.NewDropTable("loomplane_cloud_profiles").IfExists().Exec(ctx); err != nil {
				return err
			}

			_, err := db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_cloud_target_delete_trigger()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_cloud_target_delete_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_cloud_target_identity_trigger()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_cloud_target_identity_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_cloud_revision_trigger()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_cloud_revision_trigger: %w", err)
			}

			return db.NewDropIndex("loomplane_projects_scope_id").IfExists().Exec(ctx)
		},
	)
}
