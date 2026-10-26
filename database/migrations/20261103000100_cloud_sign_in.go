package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Connecting a cloud by signing in. method says how a connection got its
// credential. An app is the OAuth client an organization registered with a
// cloud; only its client ID is here, the secret is in Vault. A sign-in is
// one pending attempt, and its state is single use. Nothing secret is
// stored in any of these tables.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_cloud_connections").
				AddColumn("method", migrate.Text, migrate.NotNull(), migrate.Default("token")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_oauth_apps").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("cloud", migrate.Text, migrate.NotNull()).
				Column("client_id", migrate.Text, migrate.NotNull()).
				Column("updated_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				PrimaryKey("org_id", "cloud").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_oauth_sign_ins").IfNotExists().
				Column("state", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("user_id", migrate.Text, migrate.NotNull()).
				Column("cloud", migrate.Text, migrate.NotNull()).
				Column("name", migrate.Text, migrate.NotNull()).
				Column("connection_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("expires_at", migrate.Timestamp, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewAlterTable("loomplane_notification_outbox").
				AddColumn("kind", migrate.Text, migrate.NotNull(), migrate.Default("deployment")).IfNotExists().
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_notification_outbox").
				DropColumn("kind").IfExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_oauth_sign_ins").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_oauth_apps").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewAlterTable("loomplane_cloud_connections").
				DropColumn("method").IfExists().
				Exec(ctx)
		},
	)
}
