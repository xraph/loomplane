package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_service_backups").
				DropConstraint("loomplane_service_backups_status_check").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_service_backups").
				AddCheck("status IN ('queued','running','succeeded','failed','retiring','retired')").
				Named("loomplane_service_backups_status_check").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_backup_retirements").
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("backup_id", migrate.Text, migrate.NotNull()).
				Column("source_key", migrate.Text, migrate.NotNull()).
				Column("witness_id", migrate.Text, migrate.NotNull()).
				Column("request_id", migrate.Text, migrate.NotNull()).
				Column("policy", migrate.JSON, migrate.NotNull()).
				Column("object", migrate.JSON, migrate.NotNull()).
				Column("witness_object", migrate.JSON, migrate.NotNull()).
				Column("status", migrate.Text, migrate.NotNull(),
					migrate.Check("status IN ('queued','running','failed','succeeded')")).
				Column("attempt", migrate.Integer, migrate.NotNull(), migrate.Default(0), migrate.Check("attempt>=0")).
				Column("token", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("lease_until", migrate.Timestamp).
				Column("next_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Column("error", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Column("completed_at", migrate.Timestamp).
				Unique("org_id", "backup_id").
				Unique("org_id", "project_id", "request_id").
				Check("backup_id<>witness_id").
				ForeignKey("org_id", "project_id", "backup_id").
				References("loomplane_service_backups", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "project_id", "witness_id").
				References("loomplane_service_backups", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_backup_retirements_source_active").Unique().
				On("loomplane_backup_retirements").
				Columns("org_id", "source_key").
				Where("status<>'succeeded'").
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateIndex("loomplane_backup_retirements_pending").
				On("loomplane_backup_retirements").
				Columns("next_at", "created_at").
				Where("status<>'succeeded'").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $$
BEGIN
	IF EXISTS(SELECT 1 FROM loomplane_service_backups WHERE status IN ('retiring','retired')) THEN
		RAISE EXCEPTION 'backup retirement history must be preserved';
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("check backup retirement history: %w", err)
			}

			err = db.NewDropTable("loomplane_backup_retirements").Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_service_backups").
				DropConstraint("loomplane_service_backups_status_check").
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewAlterTable("loomplane_service_backups").
				AddCheck("status IN ('queued','running','succeeded','failed')").
				Named("loomplane_service_backups_status_check").
				Exec(ctx)
		},
	)
}
