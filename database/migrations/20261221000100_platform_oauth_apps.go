package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Cloud sign-in apps belong to the platform, not to each organization.
// There is one row per cloud, with its client ID and, when the cloud
// refused it, why. The secret stays in Vault. The per-organization apps
// and their problem table are dropped, and each connection now records the
// client ID it signed in with, so a change of app can be told from a
// rotation of its secret.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_platform_oauth_apps").IfNotExists().
				Column("cloud", migrate.Text, migrate.PrimaryKey()).
				Column("client_id", migrate.Text, migrate.NotNull()).
				Column("slug", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("updated_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				Column("problem", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("problem_at", migrate.Timestamp).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_cloud_connections").
				AddColumn("oauth_client_id", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_oauth_app_problems").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewDropTable("loomplane_oauth_apps").IfExists().Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_oauth_apps").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("cloud", migrate.Text, migrate.NotNull()).
				Column("client_id", migrate.Text, migrate.NotNull()).
				Column("slug", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("updated_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				PrimaryKey("org_id", "cloud").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_oauth_app_problems").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("cloud", migrate.Text, migrate.NotNull()).
				Column("message", migrate.Text, migrate.NotNull()).
				Column("at", migrate.Timestamp, migrate.NotNull()).
				PrimaryKey("org_id", "cloud").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_cloud_connections").
				DropColumn("oauth_client_id").IfExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewDropTable("loomplane_platform_oauth_apps").IfExists().Exec(ctx)
		},
	)
}
