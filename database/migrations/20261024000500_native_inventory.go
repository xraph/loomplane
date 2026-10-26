package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_native_sources").
				AddColumn("state", migrate.Text, migrate.NotNull(), migrate.Default("never")).IfNotExists().
				AddColumn("complete", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).IfNotExists().
				AddColumn("checked_at", migrate.Timestamp).IfNotExists().
				AddColumn("observed_at", migrate.Timestamp).IfNotExists().
				AddColumn("error", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_native_points").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey(), migrate.DefaultExpr("gen_random_uuid()::text")).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("source_id", migrate.Text, migrate.NotNull()).
				Column("provider_id", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("body", migrate.JSON, migrate.NotNull(), migrate.Check("jsonb_typeof(body)='object'")).
				Column("present", migrate.Boolean, migrate.NotNull(), migrate.Default(true)).
				Column("last_seen_at", migrate.Timestamp, migrate.NotNull()).
				Unique("source_id", "provider_id", "created_at").
				ForeignKey("org_id", "source_id").
				References("loomplane_native_sources", "org_id", "id").OnDelete(migrate.Cascade).
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateIndex("loomplane_native_points_source").IfNotExists().
				On("loomplane_native_points").
				Columns("org_id", "source_id").
				ColumnDesc("created_at").
				ColumnDesc("id").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $native_inventory_down$
BEGIN
	IF to_regclass('loomplane_native_points') IS NOT NULL THEN
		LOCK TABLE loomplane_native_points IN ACCESS EXCLUSIVE MODE;
		IF EXISTS(SELECT 1 FROM loomplane_native_points) THEN
			RAISE EXCEPTION 'native recovery points are still retained' USING ERRCODE='55000';
		END IF;

		DROP TABLE loomplane_native_points;
	END IF;
END $native_inventory_down$`)
			if err != nil {
				return fmt.Errorf("drop native recovery points: %w", err)
			}
			return nil
		},
	)
}
