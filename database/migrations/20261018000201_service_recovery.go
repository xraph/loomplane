package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateIndex("loomplane_environments_scope_id").Unique().IfNotExists().
				On("loomplane_environments").
				Columns("org_id", "project_id", "id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_service_backups").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("connection_id", migrate.Text, migrate.NotNull()).
				Column("name", migrate.Text, migrate.NotNull()).
				Column("status", migrate.Text, migrate.NotNull(),
					migrate.Check("status IN ('queued','running','succeeded','failed')")).
				Column("attempt", migrate.Integer, migrate.NotNull(), migrate.Default(0)).
				Column("error", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("request_id", migrate.Text, migrate.NotNull()).
				Column("source", migrate.JSON, migrate.NotNull()).
				Column("object", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).
				Column("token", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("lease_until", migrate.Timestamp).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("completed_at", migrate.Timestamp).
				Column("restored_at", migrate.Timestamp).
				Unique("org_id", "project_id", "request_id").
				ForeignKey("org_id", "project_id", "environment_id").
				References("loomplane_environments", "org_id", "project_id", "id").OnDelete(migrate.Restrict).
				ForeignKey("org_id", "connection_id").
				References("loomplane_cloud_connections", "org_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateIndex("loomplane_service_backups_scope").IfNotExists().
				On("loomplane_service_backups").
				Columns("org_id", "project_id").
				ColumnDesc("created_at").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropTable("loomplane_service_backups").IfExists().Exec(ctx)
		},
	)
}
