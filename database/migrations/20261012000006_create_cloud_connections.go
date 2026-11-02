package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// A cloud account an organization deploys to. The credential isn't here:
// it's in Vault, encrypted, under the organization's and the connection's
// IDs. details holds what's safe to show about it, such as a service
// account's email.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewCreateTable("loomplane_cloud_connections").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("cloud", migrate.Text, migrate.NotNull()).
				Column("name", migrate.Text, migrate.NotNull()).
				Column("account", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("details", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).
				Column("status", migrate.Text, migrate.NotNull()).
				Column("missing", migrate.JSON, migrate.NotNull(), migrate.Default("[]")).
				Column("message", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("checked_at", migrate.Timestamp).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Unique("org_id", "name").Named("loomplane_cloud_connections_org_name").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropTable("loomplane_cloud_connections").IfExists().Exec(ctx)
		},
	)
}
