package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// An external endpoint a catalog service can connect to instead of a
// dependency Loomplane provisions. The DSN, password included, is in Vault
// under the endpoint's ID; the row holds only what isn't secret.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewCreateTable("loomplane_catalog_endpoints").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("name", migrate.Text, migrate.NotNull()).
				Column("engine", migrate.Text, migrate.NotNull()).
				Column("host", migrate.Text, migrate.NotNull()).
				Column("port", migrate.Integer, migrate.NotNull(), migrate.Check("port BETWEEN 1 AND 65535")).
				Column("database", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("username", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("tls", migrate.Text, migrate.NotNull()).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("revision", migrate.Integer, migrate.NotNull(), migrate.Default(1)).
				Unique("org_id", "name").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropTable("loomplane_catalog_endpoints").IfExists().Exec(ctx)
		},
	)
}
