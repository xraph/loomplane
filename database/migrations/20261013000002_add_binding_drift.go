package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// What a binding's drift check needs and finds: the connection that
// deployed it, the revision and commit that went live, and what the last
// check saw.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_bindings").
				AddColumn("connection_id", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				AddColumn("revision", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				AddColumn("commit_sha", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				AddColumn("drift", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				AddColumn("drift_message", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				AddColumn("observed_at", migrate.Timestamp).IfNotExists().
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_bindings").
				DropColumn("observed_at").IfExists().
				DropColumn("drift_message").IfExists().
				DropColumn("drift").IfExists().
				DropColumn("commit_sha").IfExists().
				DropColumn("revision").IfExists().
				DropColumn("connection_id").IfExists().
				Exec(ctx)
		},
	)
}
