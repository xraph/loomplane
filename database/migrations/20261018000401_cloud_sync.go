package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// deploymentGuardSQL is the deployment guard this migration installs.
// deployment_capacity_wait builds its own versions from it.
const deploymentGuardSQL = `CREATE OR REPLACE FUNCTION loomplane_deployment_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
	owner TEXT;
	plan_sync TEXT;
BEGIN
	IF NEW.status<>'running' THEN RETURN NEW; END IF;

	PERFORM 1 FROM loomplane_environments WHERE org_id=NEW.org_id AND id=NEW.environment_id FOR UPDATE;
	SELECT sync_id INTO owner FROM loomplane_environment_sync_guards WHERE org_id=NEW.org_id AND environment_id=NEW.environment_id;
	SELECT coalesce(result->>'cloud_sync_id','') INTO plan_sync FROM loomplane_plans WHERE org_id=NEW.org_id AND id=NEW.plan_id;

	IF owner IS NOT NULL AND owner<>plan_sync THEN
		RAISE EXCEPTION 'environment waits for Cloud sync %',owner USING ERRCODE='55P03';
	END IF;
	IF EXISTS(SELECT 1 FROM loomplane_deployments WHERE org_id=NEW.org_id AND environment_id=NEW.environment_id AND status='running' AND plan_id<>NEW.plan_id) THEN
		RAISE EXCEPTION 'environment has a running deployment' USING ERRCODE='55P03';
	END IF;

	RETURN NEW;
END $$`

// cloudSyncDeleteGuardSQL is the delete guard this migration installs.
// cloud_sync_recovery_guards widens it and puts it back on the way down.
const cloudSyncDeleteGuardSQL = `CREATE OR REPLACE FUNCTION loomplane_cloud_sync_delete_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
	active BOOLEAN;
BEGIN
	SELECT EXISTS(SELECT 1 FROM loomplane_cloud_syncs WHERE org_id=OLD.org_id AND project_id=OLD.project_id AND status NOT IN ('succeeded','reverted') AND (cloud_id=OLD.id OR target_id=OLD.id))
		OR EXISTS(SELECT 1 FROM loomplane_cloud_previews WHERE org_id=OLD.org_id AND project_id=OLD.project_id AND created_at>now()-interval '24 hours' AND (body->'input'->>'cloud_id'=OLD.id OR body->'input'->>'target_id'=OLD.id OR body->'before_assignment'->>'cloud_id'=OLD.id))
		INTO active;
	IF active THEN
		RAISE EXCEPTION 'Cloud configuration has retained migration references' USING ERRCODE='23503';
	END IF;

	RETURN OLD;
END $$`

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_cloud_assignments").
				DropNotNull("cloud_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_applied_variables").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("deployment_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_deployments", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("action_id", migrate.Text, migrate.NotNull()).
				Column("secret_ref", migrate.Text, migrate.NotNull()).
				PrimaryKey("org_id", "deployment_id", "action_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_cloud_previews").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("revision", migrate.Text, migrate.NotNull()).
				Column("body", migrate.JSON, migrate.NotNull()).
				Column("snapshot", migrate.JSON, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Unique("org_id", "project_id", "id").
				ForeignKey("org_id", "project_id").References("loomplane_projects", "org_id", "id").OnDelete(migrate.Cascade).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_cloud_preview_connections").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("preview_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_cloud_previews", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("connection_id", migrate.Text, migrate.NotNull()).
				PrimaryKey("preview_id", "connection_id").
				ForeignKey("org_id", "connection_id").References("loomplane_cloud_connections", "org_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_cloud_syncs").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("preview_id", migrate.Text, migrate.NotNull()).
				Column("idempotency_key", migrate.Text, migrate.NotNull()).
				Column("status", migrate.Text, migrate.NotNull()).
				Column("target_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("cloud_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("body", migrate.JSON, migrate.NotNull()).
				Column("snapshot", migrate.JSON, migrate.NotNull()).
				Column("version", migrate.BigInt, migrate.NotNull(), migrate.Default(1)).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Unique("org_id", "project_id", "id").
				Unique("org_id", "project_id", "idempotency_key").
				Unique("preview_id").
				ForeignKey("org_id", "project_id", "preview_id").
				References("loomplane_cloud_previews", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_cloud_sync_active_target").IfNotExists().Unique().
				On("loomplane_cloud_syncs").
				Columns("org_id", "project_id", "target_id").
				Where("target_id<>'' AND status NOT IN ('succeeded','reverted','reverting')").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_environment_sync_guards").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("sync_id", migrate.Text, migrate.NotNull()).
				PrimaryKey("org_id", "environment_id").
				ForeignKey("org_id", "project_id", "environment_id").
				References("loomplane_environments", "org_id", "project_id", "id").OnDelete(migrate.Cascade).
				ForeignKey("org_id", "project_id", "sync_id").
				References("loomplane_cloud_syncs", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_cloud_sync_connections").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("sync_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_cloud_syncs", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("connection_id", migrate.Text, migrate.NotNull()).
				PrimaryKey("sync_id", "connection_id").
				ForeignKey("org_id", "connection_id").References("loomplane_cloud_connections", "org_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			// The same environment row orders approval, start, retry and sync acceptance.
			_, err = db.Exec(ctx, deploymentGuardSQL)
			if err != nil {
				return fmt.Errorf("create function loomplane_deployment_guard: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_deployment_sync_guard
BEFORE INSERT OR UPDATE OF status ON loomplane_deployments
FOR EACH ROW EXECUTE FUNCTION loomplane_deployment_guard()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_deployment_sync_guard: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_approval_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.status='approved' THEN
		PERFORM 1 FROM loomplane_environments WHERE org_id=NEW.org_id AND id=NEW.environment_id FOR UPDATE;
	END IF;
	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_approval_guard: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_plan_approval_guard
BEFORE INSERT OR UPDATE OF status ON loomplane_plans
FOR EACH ROW EXECUTE FUNCTION loomplane_approval_guard()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_plan_approval_guard: %w", err)
			}

			_, err = db.Exec(ctx, cloudSyncDeleteGuardSQL)
			if err != nil {
				return fmt.Errorf("create function loomplane_cloud_sync_delete_guard: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_cloud_sync_profile_delete
BEFORE DELETE ON loomplane_cloud_profiles
FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_sync_delete_guard()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_cloud_sync_profile_delete: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_cloud_sync_target_delete
BEFORE DELETE ON loomplane_cloud_targets
FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_sync_delete_guard()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_cloud_sync_target_delete: %w", err)
			}

			return db.NewCreateTable("loomplane_cloud_sync_bindings").IfNotExists().
				Column("sync_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_cloud_syncs", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("binding_id", migrate.Text, migrate.NotNull()).
				Column("after_value", migrate.JSON, migrate.NotNull()).
				PrimaryKey("sync_id", "binding_id").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_cloud_sync_profile_delete ON loomplane_cloud_profiles`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_cloud_sync_profile_delete: %w", err)
			}

			_, err = db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_cloud_sync_target_delete ON loomplane_cloud_targets`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_cloud_sync_target_delete: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_cloud_sync_delete_guard()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_cloud_sync_delete_guard: %w", err)
			}

			err = db.NewDropTable("loomplane_cloud_preview_connections").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_cloud_sync_bindings").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_plan_approval_guard ON loomplane_plans`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_plan_approval_guard: %w", err)
			}

			_, err = db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_deployment_sync_guard ON loomplane_deployments`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_deployment_sync_guard: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_approval_guard()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_approval_guard: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_deployment_guard()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_deployment_guard: %w", err)
			}

			err = db.NewDropTable("loomplane_cloud_sync_connections").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_environment_sync_guards").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_cloud_syncs").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_cloud_previews").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewDropTable("loomplane_applied_variables").IfExists().Exec(ctx)
		},
	)
}
