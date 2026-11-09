package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if err := db.NewCreateTable("loomplane_fleet_operation_inputs").
			Column("operation_id", migrate.Text, migrate.PrimaryKey()).
			Column("org_id", migrate.Text, migrate.NotNull()).
			Column("fleet_id", migrate.Text, migrate.NotNull()).
			Column("instance_id", migrate.Text, migrate.NotNull()).
			Column("payload", migrate.Text, migrate.NotNull(), migrate.Check("octet_length(payload) BETWEEN 1 AND 2097152")).
			Column("created_at", migrate.Timestamp, migrate.NotNull()).
			ForeignKey("org_id", "fleet_id", "instance_id", "operation_id").References("loomplane_fleet_operations", "org_id", "fleet_id", "instance_id", "id").OnDelete(migrate.Restrict).Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateTable("loomplane_fleet_operation_outbox").
			Column("operation_id", migrate.Text, migrate.PrimaryKey()).
			Column("org_id", migrate.Text, migrate.NotNull()).
			Column("fleet_id", migrate.Text, migrate.NotNull()).
			Column("instance_id", migrate.Text, migrate.NotNull()).
			Column("next_at", migrate.Timestamp, migrate.NotNull()).
			Column("published_at", migrate.Timestamp).
			ForeignKey("org_id", "fleet_id", "instance_id", "operation_id").References("loomplane_fleet_operations", "org_id", "fleet_id", "instance_id", "id").OnDelete(migrate.Restrict).Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateIndex("loomplane_fleet_operation_pending").On("loomplane_fleet_operation_outbox").Columns("next_at", "operation_id").Where("published_at IS NULL").Exec(ctx); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `CREATE FUNCTION loomplane_fleet_operation_input_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fleet operation inputs are immutable' USING ERRCODE='23514'; END $$`); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `CREATE TRIGGER loomplane_fleet_operation_input_immutable BEFORE UPDATE OR DELETE ON loomplane_fleet_operation_inputs FOR EACH ROW EXECUTE FUNCTION loomplane_fleet_operation_input_immutable()`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		if _, err := db.Exec(ctx, `DO $$ BEGIN LOCK TABLE loomplane_fleet_operation_inputs IN ACCESS EXCLUSIVE MODE; IF EXISTS(SELECT 1 FROM loomplane_fleet_operation_inputs) THEN RAISE EXCEPTION 'fleet operation recovery inputs must be retained'; END IF; END $$`); err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_fleet_operation_outbox").Exec(ctx); err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_fleet_operation_inputs").Exec(ctx); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `DROP FUNCTION loomplane_fleet_operation_input_immutable()`)
		return err
	})
}
