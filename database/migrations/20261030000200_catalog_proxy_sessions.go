package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// A session on a catalog service's admin, opened through Loomplane's
// proxy. Only the SHA-256 of the session's token is kept; the token itself
// is in the URL the browser holds and nowhere else. expires_at moves with
// each use: IdleTTL after last_used_at, never past the absolute lifetime
// from created_at.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_catalog_proxy_sessions").IfNotExists().
				Column("token_hash", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("resource", migrate.Text, migrate.NotNull()).
				Column("kind", migrate.Text, migrate.NotNull()).
				Column("user_id", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("last_used_at", migrate.Timestamp, migrate.NotNull()).
				Column("expires_at", migrate.Timestamp, migrate.NotNull()).
				Exec(ctx)
			if err != nil {
				return err
			}

			// Revoking a resource's sessions, and pruning expired ones.
			err = db.NewCreateIndex("loomplane_catalog_proxy_sessions_resource").IfNotExists().
				On("loomplane_catalog_proxy_sessions").
				Columns("org_id", "environment_id", "resource").
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateIndex("loomplane_catalog_proxy_sessions_expiry").IfNotExists().
				On("loomplane_catalog_proxy_sessions").
				Columns("expires_at").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropTable("loomplane_catalog_proxy_sessions").IfExists().Exec(ctx)
		},
	)
}
