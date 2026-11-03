package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// The Bitbucket workspaces an organization has connected, and the push
// webhooks Loomplane keeps on their repositories. The same workspace may be
// connected to several organizations, each by someone Bitbucket confirmed
// belongs to it. No secret is stored in either table: the refresh token
// lives in Vault, and a webhook's secret is derived when a delivery
// arrives.
//
// A webhook row with status off stays so a source can say why push
// re-analysis isn't running. Deliveries to it are refused.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_bitbucket_workspaces").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("workspace_uuid", migrate.Text, migrate.NotNull()).
				Column("workspace_slug", migrate.Text, migrate.NotNull()).
				Column("workspace_name", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("avatar_url", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("bitbucket_user_uuid", migrate.Text, migrate.NotNull()).
				Column("bitbucket_user_name", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("status", migrate.Text, migrate.NotNull(), migrate.Default("ready"), migrate.Check("status IN ('ready','reconnect_needed')")).
				Column("message", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Unique("org_id", "workspace_uuid").
				Exec(ctx)
			if err != nil {
				return err
			}

			// Sources name a workspace by slug, in whatever case they were typed.
			err = db.NewCreateIndex("loomplane_bitbucket_workspaces_slug").IfNotExists().
				On("loomplane_bitbucket_workspaces").
				Columns("org_id").
				ColumnExpr("lower(workspace_slug)").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_bitbucket_webhooks").IfNotExists().
				Column("hook_id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("connection_id", migrate.Text, migrate.NotNull()).
				Column("repository", migrate.Text, migrate.NotNull()).
				Column("bitbucket_hook_uuid", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("status", migrate.Text, migrate.NotNull(), migrate.Check("status IN ('on','off')")).
				Column("reason", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Exec(ctx)
			if err != nil {
				return err
			}

			// A repository has one hook per organization. Bitbucket names it in
			// its own case and a source keeps the case it was added with, so the
			// key is lowercased.
			err = db.NewCreateIndex("loomplane_bitbucket_webhooks_repository").IfNotExists().Unique().
				On("loomplane_bitbucket_webhooks").
				Columns("org_id").
				ColumnExpr("lower(repository)").
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateIndex("loomplane_bitbucket_webhooks_connection").IfNotExists().
				On("loomplane_bitbucket_webhooks").
				Columns("org_id", "connection_id").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			if err := db.NewDropTable("loomplane_bitbucket_webhooks").IfExists().Exec(ctx); err != nil {
				return err
			}
			return db.NewDropTable("loomplane_bitbucket_workspaces").IfExists().Exec(ctx)
		},
	)
}
