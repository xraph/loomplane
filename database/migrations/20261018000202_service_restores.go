package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateIndex("loomplane_service_backups_scope_id").Unique().IfNotExists().
				On("loomplane_service_backups").
				Columns("org_id", "project_id", "id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_service_restores").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("backup_id", migrate.Text, migrate.NotNull()).
				Column("status", migrate.Text, migrate.NotNull(),
					migrate.Check("status IN ('queued','running','succeeded','failed','deleting','cleanup_failed','deleted')")).
				Column("external_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("error", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("request_id", migrate.Text, migrate.NotNull()).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("expires_at", migrate.Timestamp, migrate.NotNull()).
				Column("verified_at", migrate.Timestamp).
				Column("duration_ms", migrate.BigInt, migrate.NotNull(), migrate.Default(0)).
				Column("lease_until", migrate.Timestamp).
				Unique("org_id", "project_id", "request_id").
				ForeignKey("org_id", "project_id", "backup_id").
				References("loomplane_service_backups", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateIndex("loomplane_service_restores_scope").IfNotExists().
				On("loomplane_service_restores").
				Columns("org_id", "project_id").
				ColumnDesc("created_at").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropTable("loomplane_service_restores").IfExists().Exec(ctx)
		},
	)
}
