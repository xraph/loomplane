package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_host_ports").IfNotExists().
				Column("scope", migrate.Text, migrate.NotNull()).
				Column("port", migrate.Integer, migrate.NotNull(), migrate.Check("port BETWEEN 1 AND 65535")).
				Column("owner", migrate.Text, migrate.NotNull()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("connection_id", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				PrimaryKey("scope", "port").
				ForeignKey("org_id", "connection_id").References("loomplane_cloud_connections", "org_id", "id").OnDelete(migrate.Restrict).
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateIndex("loomplane_host_ports_owner").IfNotExists().
				On("loomplane_host_ports").
				Columns("org_id", "owner").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropTable("loomplane_host_ports").IfExists().Exec(ctx)
		},
	)
}
