package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Who signed in, and what each sign-in needs. signed_in_by is the
// Loomplane user behind an OAuth connection, so the connection can be
// flagged when they leave. A Vercel app has a slug; a pending sign-in
// carries the settings picked before it started and its PKCE verifier; and
// a problem says why an organization's app stopped working. Nothing
// secret is stored here.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_cloud_connections").
				AddColumn("signed_in_by", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_oauth_apps").
				AddColumn("slug", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_oauth_sign_ins").
				AddColumn("settings", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).IfNotExists().
				AddColumn("verifier", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
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

			return db.NewCreateIndex("loomplane_oauth_sign_ins_expires_at").IfNotExists().
				On("loomplane_oauth_sign_ins").
				Columns("expires_at").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewDropIndex("loomplane_oauth_sign_ins_expires_at").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_oauth_app_problems").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_oauth_sign_ins").
				DropColumn("verifier").IfExists().
				DropColumn("settings").IfExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_oauth_apps").
				DropColumn("slug").IfExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewAlterTable("loomplane_cloud_connections").
				DropColumn("signed_in_by").IfExists().
				Exec(ctx)
		},
	)
}
