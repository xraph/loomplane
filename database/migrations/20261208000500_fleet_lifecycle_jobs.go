package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if err := db.NewCreateIndex("loomplane_fleet_operation_scope").Unique().On("loomplane_fleet_operations").Columns("org_id", "fleet_id", "instance_id", "id").Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateTable("loomplane_fleet_job_runs").
			Column("id", migrate.Text, migrate.PrimaryKey()).Column("org_id", migrate.Text, migrate.NotNull()).Column("fleet_id", migrate.Text, migrate.NotNull()).Column("instance_id", migrate.Text, migrate.NotNull()).Column("operation_id", migrate.Text, migrate.NotNull()).
			Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
			Column("digest", migrate.Text, migrate.NotNull()).Column("body", migrate.Text, migrate.NotNull(), migrate.Check("octet_length(body) BETWEEN 1 AND 524288")).
			Column("attempt", migrate.Integer, migrate.NotNull(), migrate.Check("attempt BETWEEN 1 AND 5")).Column("issued", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).Column("cleaned", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
			Column("receipt", migrate.Text, migrate.NotNull(), migrate.Check("octet_length(receipt) BETWEEN 1 AND 4096")).
			Unique("org_id", "id").ForeignKey("org_id", "fleet_id", "instance_id", "operation_id").References("loomplane_fleet_operations", "org_id", "fleet_id", "instance_id", "id").OnDelete(migrate.Restrict).Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateTable("loomplane_fleet_job_receipts").Column("run_id", migrate.Text, migrate.NotNull(), migrate.References("loomplane_fleet_job_runs", "id"), migrate.OnDelete(migrate.Restrict)).Column("attempt", migrate.Integer, migrate.NotNull()).Column("body", migrate.Text, migrate.NotNull()).Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).PrimaryKey("run_id", "attempt").Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateTable("loomplane_fleet_schema_locks").Column("org_id", migrate.Text, migrate.NotNull()).Column("resource_id", migrate.Text, migrate.NotNull()).Column("run_id", migrate.Text, migrate.NotNull()).PrimaryKey("org_id", "resource_id").ForeignKey("org_id", "run_id").References("loomplane_fleet_job_runs", "org_id", "id").OnDelete(migrate.Restrict).Exec(ctx); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `CREATE FUNCTION loomplane_fleet_job_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_TABLE_NAME='loomplane_fleet_job_receipts' OR TG_OP='DELETE' THEN RAISE EXCEPTION 'lifecycle receipts are immutable' USING ERRCODE='23514'; END IF;
 IF (NEW.id,NEW.org_id,NEW.fleet_id,NEW.instance_id,NEW.operation_id,NEW.digest,NEW.body,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.org_id,OLD.fleet_id,OLD.instance_id,OLD.operation_id,OLD.digest,OLD.body,OLD.created_at) THEN RAISE EXCEPTION 'lifecycle job identity is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW; END $$`); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `CREATE TRIGGER loomplane_fleet_job_immutable BEFORE UPDATE OR DELETE ON loomplane_fleet_job_runs FOR EACH ROW EXECUTE FUNCTION loomplane_fleet_job_immutable()`); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `CREATE TRIGGER loomplane_fleet_receipt_immutable BEFORE UPDATE OR DELETE ON loomplane_fleet_job_receipts FOR EACH ROW EXECUTE FUNCTION loomplane_fleet_job_immutable()`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		if _, err := db.Exec(ctx, `DO $$ BEGIN LOCK TABLE loomplane_fleet_job_runs IN ACCESS EXCLUSIVE MODE; IF EXISTS(SELECT 1 FROM loomplane_fleet_job_runs) THEN RAISE EXCEPTION 'lifecycle receipts must be retained'; END IF; END $$`); err != nil {
			return err
		}
		for _, table := range []string{"loomplane_fleet_schema_locks", "loomplane_fleet_job_receipts", "loomplane_fleet_job_runs"} {
			if err := db.NewDropTable(table).Exec(ctx); err != nil {
				return err
			}
		}
		if _, err := db.Exec(ctx, `DROP FUNCTION loomplane_fleet_job_immutable()`); err != nil {
			return err
		}
		return db.NewDropIndex("loomplane_fleet_operation_scope").Exec(ctx)
	})
}
