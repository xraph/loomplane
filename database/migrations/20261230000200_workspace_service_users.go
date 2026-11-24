package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// The users Loomplane makes on a workspace service, one for each project's
// service that gets a user of its own (access: own_user), with the database
// it was given. environments are the project's environments whose service
// uses it now: one server can serve several, and the user keeps its login
// until the last lets go. created_database says whether Loomplane made the
// database, so dropping the user drops only a database it made. revoked_at
// is set while no service uses the user and it can't log in. The password
// is in Vault, never here.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewCreateTable("loomplane_catalog_endpoint_users").IfNotExists().
				Column("endpoint_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_catalog_endpoints", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("name", migrate.Text, migrate.NotNull()).
				Column("database_name", migrate.Text, migrate.NotNull()).
				Column("created_database", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("service", migrate.Text, migrate.NotNull()).
				Column("environments", migrate.ArrayOf(migrate.Text), migrate.NotNull(), migrate.DefaultExpr("'{}'")).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("revoked_at", migrate.Timestamp).
				PrimaryKey("endpoint_id", "name").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropTable("loomplane_catalog_endpoint_users").IfExists().Exec(ctx)
		},
	)
}
