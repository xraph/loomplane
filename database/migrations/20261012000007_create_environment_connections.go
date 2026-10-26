package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Which connection an environment deploys to each cloud with, when the
// organization has more than one. No foreign key to the connection: a
// removed one stays named here, so the next plan says so instead of
// quietly picking another account.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewCreateTable("loomplane_environment_connections").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_projects", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("environment_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_environments", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("cloud", migrate.Text, migrate.NotNull()).
				Column("connection_id", migrate.Text, migrate.NotNull()).
				PrimaryKey("environment_id", "cloud").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropTable("loomplane_environment_connections").IfExists().Exec(ctx)
		},
	)
}
