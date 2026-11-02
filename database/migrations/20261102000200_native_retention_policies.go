package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_native_retention_policies").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("source_id", migrate.Text, migrate.NotNull()).
				Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Check("revision>0 AND revision<2147483647")).
				Column("policy", migrate.JSON, migrate.NotNull(), migrate.Check("jsonb_typeof(policy)='object'")).
				Column("enabled", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("authorized_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Column("next_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Column("last_error", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("last_retirement_id", migrate.Text).
				Unique("org_id", "source_id").
				ForeignKey("org_id", "source_id").
				References("loomplane_native_sources", "org_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("last_retirement_id").
				References("loomplane_native_retirements", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_native_retention_due").IfNotExists().
				On("loomplane_native_retention_policies").
				Columns("next_at", "id").
				Where("enabled").
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateTable("loomplane_native_retention_revisions").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("policy_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_native_retention_policies", "id"), migrate.OnDelete(migrate.Restrict)).
				Column("revision", migrate.BigInt, migrate.NotNull()).
				Column("policy", migrate.JSON, migrate.NotNull()).
				Column("enabled", migrate.Boolean, migrate.NotNull()).
				Column("authorized_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				PrimaryKey("policy_id", "revision").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $native_retention_down$
BEGIN
	IF to_regclass('loomplane_native_retention_policies') IS NOT NULL THEN
		LOCK TABLE loomplane_native_retention_policies IN ACCESS EXCLUSIVE MODE;
		IF EXISTS(SELECT 1 FROM loomplane_native_retention_policies) THEN
			RAISE EXCEPTION 'native retention policy history is retained' USING ERRCODE='55000';
		END IF;
	END IF;

	DROP TABLE IF EXISTS loomplane_native_retention_revisions;
	DROP TABLE IF EXISTS loomplane_native_retention_policies;
END $native_retention_down$`)
			if err != nil {
				return fmt.Errorf("drop native retention policies: %w", err)
			}
			return nil
		},
	)
}
