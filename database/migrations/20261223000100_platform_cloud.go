package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// The tables behind platform-owned Firecracker hosts: regions, the hosts in
// them, the organizations allowed to use them, where each workload is placed
// and the egress counters read back from hosts. A workspace sees the platform
// through a read-only projection in the ordinary Cloud tables: a profile
// marked platform_owned and a target with runtime 'platform'. Only the
// control plane writes those rows, inside a transaction that ran
// SET LOCAL loomplane.platform_write = 'on'. A trigger refuses everything else.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_platform_regions").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("name", migrate.Text, migrate.NotNull(), migrate.Unique()).
				Column("display_name", migrate.Text, migrate.NotNull()).
				Column("status", migrate.Text, migrate.NotNull(), migrate.Check("status IN ('active','closed')")).
				Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Check("revision>0")).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_platform_hosts").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("region_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_platform_regions", "id"), migrate.OnDelete(migrate.Restrict)).
				Column("endpoint", migrate.Text, migrate.NotNull()).
				Column("host_id", migrate.Text, migrate.NotNull(), migrate.Unique()).
				Column("status", migrate.Text, migrate.NotNull(), migrate.Check("status IN ('pending','ready','draining','retired')")).
				Column("evidence", migrate.JSON, migrate.NotNull(), migrate.DefaultExpr("'{}'::json")).
				Column("allotment", migrate.JSON, migrate.NotNull(), migrate.DefaultExpr("'{}'::json")).
				Column("client_fingerprint", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Default(0)).
				Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Check("revision>0")).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_platform_tenants").IfNotExists().
				Column("org_id", migrate.Text, migrate.PrimaryKey()).
				Column("status", migrate.Text, migrate.NotNull(), migrate.Check("status IN ('enabled','disabled')")).
				Column("limits", migrate.JSON).
				Column("suspend_reasons", migrate.ArrayOf(migrate.Text), migrate.NotNull(), migrate.DefaultExpr("'{}'")).
				Column("suspended_at", migrate.Timestamp).
				Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Check("revision>0")).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_platform_placements").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("resource", migrate.Text, migrate.NotNull()).
				Column("kind", migrate.Text, migrate.NotNull(), migrate.Check("kind IN ('service','build')")).
				Column("region_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_platform_regions", "id"), migrate.OnDelete(migrate.Restrict)).
				Column("host_id", migrate.Text,
					migrate.References("loomplane_platform_hosts", "id"), migrate.OnDelete(migrate.Restrict)).
				Column("vcpus", migrate.Integer, migrate.NotNull()).
				Column("memory_mib", migrate.Integer, migrate.NotNull()).
				Column("disk_mib", migrate.Integer, migrate.NotNull()).
				Column("state", migrate.Text, migrate.NotNull(), migrate.Check("state IN ('reserved','running','stopped','suspended','released')")).
				Column("restore_running", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("waiting_capacity", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("instance_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Default(0)).
				Column("suspended_at", migrate.Timestamp).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_platform_placements_resource").IfNotExists().
				On("loomplane_platform_placements").
				Columns("org_id", "environment_id", "resource").
				Unique().
				Where("state<>'released'").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_platform_placements_host").IfNotExists().
				On("loomplane_platform_placements").
				Columns("host_id").
				Where("state<>'released'").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_platform_egress").IfNotExists().
				Column("placement_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_platform_placements", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("counter_id", migrate.Text, migrate.NotNull()).
				Column("last_bytes", migrate.BigInt, migrate.NotNull(), migrate.Default(0)).
				PrimaryKey("placement_id", "counter_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_cloud_profiles").
				AddColumn("platform_owned", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `DO $$
BEGIN
	IF EXISTS(SELECT 1 FROM pg_constraint WHERE conname='loomplane_target_runtime') THEN
		ALTER TABLE loomplane_cloud_targets DROP CONSTRAINT loomplane_target_runtime;
	END IF;
	ALTER TABLE loomplane_cloud_targets ADD CONSTRAINT loomplane_target_runtime
		CHECK((runtime='managed' AND pool_id IS NULL AND connection_id IS NOT NULL) OR (runtime='server' AND pool_id IS NOT NULL AND connection_id IS NULL AND provider='server') OR (runtime='platform' AND pool_id IS NULL AND connection_id IS NULL AND provider='platform'));
END $$`)
			if err != nil {
				return fmt.Errorf("replace constraint loomplane_target_runtime: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_platform_projection_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
	refuse boolean := false;
BEGIN
	IF current_setting('loomplane.platform_write', true) = 'on' THEN
		IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
		RETURN NEW;
	END IF;

	IF TG_TABLE_NAME = 'loomplane_cloud_profiles' THEN
		IF TG_OP = 'INSERT' THEN
			refuse := NEW.platform_owned;
		ELSIF TG_OP = 'UPDATE' THEN
			refuse := OLD.platform_owned OR NEW.platform_owned;
		ELSE
			refuse := OLD.platform_owned;
		END IF;
	ELSE
		IF TG_OP = 'INSERT' THEN
			refuse := NEW.runtime = 'platform';
		ELSE
			refuse := OLD.runtime = 'platform'
				OR EXISTS(SELECT 1 FROM loomplane_cloud_profiles WHERE org_id = OLD.org_id AND project_id = OLD.project_id AND id = OLD.cloud_id AND platform_owned);
			IF TG_OP = 'UPDATE' THEN
				refuse := refuse OR NEW.runtime = 'platform';
			END IF;
		END IF;
	END IF;

	IF refuse THEN
		RAISE EXCEPTION 'platform cloud rows are written by the control plane only' USING ERRCODE = '42501';
	END IF;
	IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_platform_projection_guard: %w", err)
			}

			// The a_ puts these ahead of the revision triggers, which Postgres
			// runs in name order.
			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_cloud_a_profile_platform_guard
BEFORE INSERT OR UPDATE OR DELETE ON loomplane_cloud_profiles
FOR EACH ROW EXECUTE FUNCTION loomplane_platform_projection_guard()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_cloud_a_profile_platform_guard: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_cloud_a_target_platform_guard
BEFORE INSERT OR UPDATE OR DELETE ON loomplane_cloud_targets
FOR EACH ROW EXECUTE FUNCTION loomplane_platform_projection_guard()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_cloud_a_target_platform_guard: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			for _, stmt := range []string{
				`DROP TRIGGER IF EXISTS loomplane_cloud_a_target_platform_guard ON loomplane_cloud_targets`,
				`DROP TRIGGER IF EXISTS loomplane_cloud_a_profile_platform_guard ON loomplane_cloud_profiles`,
				`DROP FUNCTION IF EXISTS loomplane_platform_projection_guard()`,
				// The projection cannot outlive the runtime the old constraint allows.
				`DELETE FROM loomplane_cloud_targets WHERE runtime='platform'`,
				`DELETE FROM loomplane_cloud_profiles WHERE platform_owned`,
			} {
				if _, err := db.Exec(ctx, stmt); err != nil {
					return fmt.Errorf("%s: %w", stmt, err)
				}
			}

			err := db.NewAlterTable("loomplane_cloud_profiles").
				DropColumn("platform_owned").IfExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `DO $$
BEGIN
	IF EXISTS(SELECT 1 FROM pg_constraint WHERE conname='loomplane_target_runtime') THEN
		ALTER TABLE loomplane_cloud_targets DROP CONSTRAINT loomplane_target_runtime;
	END IF;
	ALTER TABLE loomplane_cloud_targets ADD CONSTRAINT loomplane_target_runtime
		CHECK((runtime='managed' AND pool_id IS NULL AND connection_id IS NOT NULL) OR (runtime='server' AND pool_id IS NOT NULL AND connection_id IS NULL AND provider='server'));
END $$`)
			if err != nil {
				return fmt.Errorf("restore constraint loomplane_target_runtime: %w", err)
			}

			for _, table := range []string{
				"loomplane_platform_egress", "loomplane_platform_placements", "loomplane_platform_tenants",
				"loomplane_platform_hosts", "loomplane_platform_regions",
			} {
				if err := db.NewDropTable(table).IfExists().Exec(ctx); err != nil {
					return err
				}
			}
			return nil
		},
	)
}
