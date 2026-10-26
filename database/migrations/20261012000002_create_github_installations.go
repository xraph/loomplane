package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// A GitHub App installation an organization has connected. The same
// installation may be connected to several organizations, each by someone
// GitHub confirmed can see it. Nothing secret is stored: tokens are minted
// per analysis.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewCreateTable("loomplane_github_installations").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("installation_id", migrate.BigInt, migrate.NotNull()).
				Column("account", migrate.Text, migrate.NotNull()).
				Column("account_type", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				PrimaryKey("org_id", "installation_id").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropTable("loomplane_github_installations").IfExists().Exec(ctx)
		},
	)
}
