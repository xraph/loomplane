package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Where a deployed service answers, so the services that need it can be
// given its address.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_bindings").
				AddColumn("url", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_bindings").
				DropColumn("url").IfExists().
				Exec(ctx)
		},
	)
}
