package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if err := db.NewAlterTable("loomplane_plans").DropConstraint("loomplane_plans_kind_check").DropNotNull("manifest_id").Exec(ctx); err != nil {
			return err
		}
		if err := db.NewAlterTable("loomplane_plans").AddCheck("kind IN ('release','promotion','rollback','instance')").Named("loomplane_plans_kind_check").AddCheck("(kind = 'instance' AND manifest_id IS NULL AND NOT catalog) OR (kind <> 'instance' AND manifest_id IS NOT NULL)").Named("loomplane_plans_input_kind").Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateTable("loomplane_fleet_plan_inputs").
			Column("plan_id", migrate.Text, migrate.PrimaryKey(), migrate.References("loomplane_plans", "id"), migrate.OnDelete(migrate.Restrict)).
			Column("org_id", migrate.Text, migrate.NotNull()).Column("fleet_id", migrate.Text, migrate.NotNull()).Column("instance_id", migrate.Text, migrate.NotNull()).
			Column("snapshot", migrate.Text, migrate.NotNull(), migrate.Check("octet_length(snapshot) BETWEEN 1 AND 2097152")).
			ForeignKey("org_id", "fleet_id", "instance_id").References("loomplane_fleet_instances", "org_id", "fleet_id", "id").OnDelete(migrate.Restrict).Exec(ctx); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `CREATE FUNCTION loomplane_fleet_plan_input_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'instance plan inputs are immutable' USING ERRCODE='23514'; END $$`); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `CREATE TRIGGER loomplane_fleet_plan_input_immutable BEFORE UPDATE OR DELETE ON loomplane_fleet_plan_inputs FOR EACH ROW EXECUTE FUNCTION loomplane_fleet_plan_input_immutable()`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		if _, err := db.Exec(ctx, `DO $$ BEGIN LOCK TABLE loomplane_plans IN ACCESS EXCLUSIVE MODE; IF EXISTS(SELECT 1 FROM loomplane_plans WHERE kind='instance') THEN RAISE EXCEPTION 'instance plans must be retained'; END IF; END $$`); err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_fleet_plan_inputs").Exec(ctx); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `DROP FUNCTION loomplane_fleet_plan_input_immutable()`); err != nil {
			return err
		}
		if err := db.NewAlterTable("loomplane_plans").DropConstraint("loomplane_plans_input_kind").DropConstraint("loomplane_plans_kind_check").SetNotNull("manifest_id").Exec(ctx); err != nil {
			return err
		}
		return db.NewAlterTable("loomplane_plans").AddCheck("kind IN ('release','promotion','rollback')").Named("loomplane_plans_kind_check").Exec(ctx)
	})
}
