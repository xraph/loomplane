package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if err := db.NewAlterTable("loomplane_projects").AddColumn("deployment_mode", migrate.Text, migrate.NotNull(), migrate.Default("application"), migrate.Check("deployment_mode IN ('application','fleet')")).Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateTable("loomplane_fleets").
			Column("id", migrate.Text, migrate.PrimaryKey()).
			Column("org_id", migrate.Text, migrate.NotNull()).
			Column("project_id", migrate.Text, migrate.NotNull()).
			Column("control_project_id", migrate.Text).
			Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Check("revision>0")).
			Column("status", migrate.Text, migrate.NotNull(), migrate.Check("status IN ('active','paused','retired')")).
			Column("created_at", migrate.Timestamp, migrate.NotNull()).
			Column("updated_at", migrate.Timestamp, migrate.NotNull()).
			Unique("org_id", "id").
			Unique("org_id", "project_id").
			Unique("org_id", "project_id", "id").
			ForeignKey("org_id", "project_id").References("loomplane_projects", "org_id", "id").OnDelete(migrate.Restrict).
			ForeignKey("org_id", "control_project_id").References("loomplane_projects", "org_id", "id").OnDelete(migrate.Restrict).
			Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateTable("loomplane_fleet_stages").
			Column("id", migrate.Text, migrate.PrimaryKey()).
			Column("org_id", migrate.Text, migrate.NotNull()).
			Column("project_id", migrate.Text, migrate.NotNull()).
			Column("fleet_id", migrate.Text, migrate.NotNull()).
			Column("name", migrate.Text, migrate.NotNull(), migrate.Check("length(name) BETWEEN 1 AND 64")).
			Column("environment_kind", migrate.Text, migrate.NotNull(), migrate.Check("environment_kind IN ('production','staging','preview','development')")).
			Column("max_instances", migrate.Integer, migrate.NotNull(), migrate.Check("max_instances>=0")).
			Column("policy_revision", migrate.BigInt, migrate.NotNull(), migrate.Check("policy_revision>0")).
			Column("created_at", migrate.Timestamp, migrate.NotNull()).
			Unique("org_id", "fleet_id", "id").
			Unique("org_id", "fleet_id", "name").
			ForeignKey("org_id", "project_id", "fleet_id").References("loomplane_fleets", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
			Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateTable("loomplane_fleet_stage_clouds").
			Column("org_id", migrate.Text, migrate.NotNull()).
			Column("fleet_id", migrate.Text, migrate.NotNull()).
			Column("stage_id", migrate.Text, migrate.NotNull()).
			Column("cloud_id", migrate.Text, migrate.NotNull()).
			PrimaryKey("org_id", "stage_id", "cloud_id").
			ForeignKey("org_id", "fleet_id", "stage_id").References("loomplane_fleet_stages", "org_id", "fleet_id", "id").OnDelete(migrate.Restrict).
			ForeignKey("org_id", "cloud_id").References("loomplane_cloud_profiles", "org_id", "id").OnDelete(migrate.Restrict).
			Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateTable("loomplane_fleet_instances").
			Column("id", migrate.Text, migrate.PrimaryKey()).
			Column("org_id", migrate.Text, migrate.NotNull()).
			Column("project_id", migrate.Text, migrate.NotNull()).
			Column("fleet_id", migrate.Text, migrate.NotNull()).
			Column("stage_id", migrate.Text, migrate.NotNull()).
			Column("environment_id", migrate.Text, migrate.NotNull()).
			Column("external_ref", migrate.Text, migrate.NotNull(), migrate.Check("length(external_ref) BETWEEN 1 AND 128")).
			Column("profile_id", migrate.Text, migrate.NotNull(), migrate.Check("profile_id<>''")).
			Column("desired_release_id", migrate.Text, migrate.NotNull(), migrate.Check("desired_release_id<>''")).
			Column("observed_release_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
			Column("status", migrate.Text, migrate.NotNull()).
			Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Check("revision>0")).
			Column("desired_generation", migrate.BigInt, migrate.NotNull(), migrate.Check("desired_generation>0")).
			Column("observed_generation", migrate.BigInt, migrate.NotNull(), migrate.Check("observed_generation>=0 AND observed_generation<=desired_generation")).
			Column("ownership_epoch", migrate.BigInt, migrate.NotNull(), migrate.Check("ownership_epoch>0")).
			Column("created_at", migrate.Timestamp, migrate.NotNull()).
			Column("updated_at", migrate.Timestamp, migrate.NotNull()).
			Unique("org_id", "fleet_id", "id").
			Unique("org_id", "fleet_id", "external_ref").
			Unique("environment_id").
			ForeignKey("org_id", "project_id", "fleet_id").References("loomplane_fleets", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
			ForeignKey("org_id", "fleet_id", "stage_id").References("loomplane_fleet_stages", "org_id", "fleet_id", "id").OnDelete(migrate.Restrict).
			ForeignKey("org_id", "project_id", "environment_id").References("loomplane_environments", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
			Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateTable("loomplane_fleet_operations").
			Column("id", migrate.Text, migrate.PrimaryKey()).
			Column("org_id", migrate.Text, migrate.NotNull()).
			Column("fleet_id", migrate.Text, migrate.NotNull()).
			Column("instance_id", migrate.Text, migrate.NotNull()).
			Column("kind", migrate.Text, migrate.NotNull()).
			Column("status", migrate.Text, migrate.NotNull()).
			Column("idempotency_key", migrate.Text, migrate.NotNull(), migrate.Check("length(idempotency_key) BETWEEN 1 AND 128")).
			Column("request_hash", migrate.Text, migrate.NotNull()).
			Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Check("generation>0")).
			Column("error_code", migrate.Text, migrate.NotNull(), migrate.Default("")).
			Column("created_at", migrate.Timestamp, migrate.NotNull()).
			Unique("org_id", "fleet_id", "kind", "idempotency_key").
			ForeignKey("org_id", "fleet_id", "instance_id").References("loomplane_fleet_instances", "org_id", "fleet_id", "id").OnDelete(migrate.Restrict).
			Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateIndex("loomplane_fleet_instance_stage").On("loomplane_fleet_instances").Columns("org_id", "fleet_id", "stage_id", "status").Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateIndex("loomplane_fleet_active_mutation").Unique().On("loomplane_fleet_operations").Columns("org_id", "instance_id").Where("status NOT IN ('succeeded','failed','cancelled')").Exec(ctx); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `CREATE FUNCTION loomplane_fleet_project_mode() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.deployment_mode<>'fleet' AND EXISTS(SELECT 1 FROM loomplane_fleets WHERE org_id=OLD.org_id AND project_id=OLD.id) THEN
 RAISE EXCEPTION 'fleet project mode is managed by its fleet' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;`); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `CREATE TRIGGER loomplane_fleet_project_mode BEFORE UPDATE OF deployment_mode ON loomplane_projects FOR EACH ROW EXECUTE FUNCTION loomplane_fleet_project_mode();`); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `CREATE FUNCTION loomplane_fleet_instance_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.id,NEW.org_id,NEW.project_id,NEW.fleet_id,NEW.stage_id,NEW.environment_id,NEW.external_ref) IS DISTINCT FROM (OLD.id,OLD.org_id,OLD.project_id,OLD.fleet_id,OLD.stage_id,OLD.environment_id,OLD.external_ref) THEN
 RAISE EXCEPTION 'instance identity is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;`); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `CREATE TRIGGER loomplane_fleet_instance_identity BEFORE UPDATE ON loomplane_fleet_instances FOR EACH ROW EXECUTE FUNCTION loomplane_fleet_instance_identity();`); err != nil {
			return err
		}
		return nil
	}, func(ctx context.Context, db *migrate.DB) error {
		if _, err := db.Exec(ctx, `DO $$ BEGIN LOCK TABLE loomplane_fleets IN ACCESS EXCLUSIVE MODE; IF EXISTS(SELECT 1 FROM loomplane_fleets) THEN RAISE EXCEPTION 'remove fleets before downgrade'; END IF; END $$;`); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `DROP TRIGGER loomplane_fleet_project_mode ON loomplane_projects`); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `DROP FUNCTION loomplane_fleet_project_mode()`); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `DROP TRIGGER loomplane_fleet_instance_identity ON loomplane_fleet_instances`); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `DROP FUNCTION loomplane_fleet_instance_identity()`); err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_fleet_operations").Exec(ctx); err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_fleet_instances").Exec(ctx); err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_fleet_stage_clouds").Exec(ctx); err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_fleet_stages").Exec(ctx); err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_fleets").Exec(ctx); err != nil {
			return err
		}
		return db.NewAlterTable("loomplane_projects").DropColumn("deployment_mode").Exec(ctx)
	})
}
