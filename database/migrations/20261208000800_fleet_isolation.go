package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if err := db.NewCreateTable("loomplane_fleet_namespaces").
			Column("operation_id", migrate.Text, migrate.PrimaryKey()).
			Column("org_id", migrate.Text, migrate.NotNull()).Column("project_id", migrate.Text, migrate.NotNull()).Column("fleet_id", migrate.Text, migrate.NotNull()).Column("instance_id", migrate.Text, migrate.NotNull()).Column("environment_id", migrate.Text, migrate.NotNull()).
			Column("cloud_id", migrate.Text, migrate.NotNull()).Column("cluster_id", migrate.Text, migrate.NotNull()).Column("namespace", migrate.Text, migrate.NotNull()).
			Column("ownership_epoch", migrate.BigInt, migrate.NotNull(), migrate.Check("ownership_epoch>0")).
			Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Check("generation>0")).Column("snapshot_digest", migrate.Text, migrate.NotNull()).
			Column("authority", migrate.Text, migrate.NotNull(), migrate.Check("octet_length(authority) BETWEEN 1 AND 32768")).Column("body", migrate.Text, migrate.NotNull(), migrate.Check("octet_length(body) BETWEEN 1 AND 65536")).
			Unique("org_id", "instance_id", "ownership_epoch").Unique("org_id", "cluster_id", "namespace").
			ForeignKey("org_id", "operation_id").References("loomplane_cluster_operations", "org_id", "id").OnDelete(migrate.Restrict).
			ForeignKey("org_id", "project_id", "fleet_id").References("loomplane_fleets", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
			ForeignKey("org_id", "fleet_id", "instance_id").References("loomplane_fleet_instances", "org_id", "fleet_id", "id").OnDelete(migrate.Restrict).
			ForeignKey("org_id", "project_id", "environment_id").References("loomplane_environments", "org_id", "project_id", "id").OnDelete(migrate.Restrict).Exec(ctx); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `CREATE FUNCTION loomplane_fleet_namespace_identity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' OR (NEW.operation_id,NEW.org_id,NEW.project_id,NEW.fleet_id,NEW.instance_id,NEW.environment_id,NEW.cloud_id,NEW.cluster_id,NEW.namespace,NEW.ownership_epoch,NEW.body) IS DISTINCT FROM (OLD.operation_id,OLD.org_id,OLD.project_id,OLD.fleet_id,OLD.instance_id,OLD.environment_id,OLD.cloud_id,OLD.cluster_id,OLD.namespace,OLD.ownership_epoch,OLD.body) THEN RAISE EXCEPTION 'instance namespace identity is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW; END $$`); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `CREATE TRIGGER loomplane_fleet_namespace_identity BEFORE UPDATE OR DELETE ON loomplane_fleet_namespaces FOR EACH ROW EXECUTE FUNCTION loomplane_fleet_namespace_identity()`); err != nil {
			return err
		}
		if err := db.NewCreateTable("loomplane_fleet_resource_bindings").
			Column("id", migrate.Text, migrate.PrimaryKey()).
			Column("org_id", migrate.Text, migrate.NotNull()).Column("project_id", migrate.Text, migrate.NotNull()).Column("fleet_id", migrate.Text, migrate.NotNull()).Column("instance_id", migrate.Text, migrate.NotNull()).Column("environment_id", migrate.Text, migrate.NotNull()).
			Column("name", migrate.Text, migrate.NotNull()).Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Check("generation>0")).Column("ownership_epoch", migrate.BigInt, migrate.NotNull(), migrate.Check("ownership_epoch>0")).
			Column("ownership", migrate.Text, migrate.NotNull(), migrate.Check("ownership IN ('owned','attached','shared')")).
			Column("resource_id", migrate.Text, migrate.NotNull(), migrate.Check("resource_id<>''")).Column("incarnation", migrate.Text, migrate.NotNull(), migrate.Check("incarnation<>''")).
			Column("principal_id", migrate.Text, migrate.NotNull(), migrate.Check("principal_id<>''")).
			Column("endpoint_id", migrate.Text, migrate.NotNull(), migrate.References("loomplane_catalog_endpoints", "id"), migrate.OnDelete(migrate.Restrict)).
			Column("body", migrate.Text, migrate.NotNull(), migrate.Check("octet_length(body) BETWEEN 1 AND 16384")).
			Unique("org_id", "instance_id", "generation", "name").
			ForeignKey("org_id", "project_id", "fleet_id").References("loomplane_fleets", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
			ForeignKey("org_id", "fleet_id", "instance_id").References("loomplane_fleet_instances", "org_id", "fleet_id", "id").OnDelete(migrate.Restrict).
			ForeignKey("org_id", "project_id", "environment_id").References("loomplane_environments", "org_id", "project_id", "id").OnDelete(migrate.Restrict).Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateTable("loomplane_fleet_resource_principals").
			Column("org_id", migrate.Text, migrate.NotNull()).Column("fleet_id", migrate.Text, migrate.NotNull()).Column("instance_id", migrate.Text, migrate.NotNull()).
			Column("resource_id", migrate.Text, migrate.NotNull()).Column("incarnation", migrate.Text, migrate.NotNull()).Column("principal_id", migrate.Text, migrate.NotNull()).
			PrimaryKey("org_id", "resource_id", "incarnation", "principal_id").
			ForeignKey("org_id", "fleet_id", "instance_id").References("loomplane_fleet_instances", "org_id", "fleet_id", "id").OnDelete(migrate.Restrict).Exec(ctx); err != nil {
			return err
		}
		for _, table := range []string{"loomplane_fleet_resource_bindings", "loomplane_fleet_resource_principals"} {
			if _, err := db.Exec(ctx, `CREATE TRIGGER `+table+`_immutable BEFORE UPDATE OR DELETE ON `+table+` FOR EACH ROW EXECUTE FUNCTION loomplane_fleet_plan_input_immutable()`); err != nil {
				return err
			}
		}
		return nil
	}, func(ctx context.Context, db *migrate.DB) error {
		if _, err := db.Exec(ctx, `DO $$ BEGIN LOCK TABLE loomplane_fleet_namespaces IN ACCESS EXCLUSIVE MODE; IF EXISTS(SELECT 1 FROM loomplane_fleet_namespaces) THEN RAISE EXCEPTION 'instance namespace history must be retained'; END IF; END $$`); err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_fleet_namespaces").Exec(ctx); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `DROP FUNCTION loomplane_fleet_namespace_identity()`); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `DO $$ BEGIN LOCK TABLE loomplane_fleet_resource_bindings,loomplane_fleet_resource_principals IN ACCESS EXCLUSIVE MODE; IF EXISTS(SELECT 1 FROM loomplane_fleet_resource_bindings) OR EXISTS(SELECT 1 FROM loomplane_fleet_resource_principals) THEN RAISE EXCEPTION 'instance resource ownership must be retained'; END IF; END $$`); err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_fleet_resource_bindings").Exec(ctx); err != nil {
			return err
		}
		return db.NewDropTable("loomplane_fleet_resource_principals").Exec(ctx)
	})
}
