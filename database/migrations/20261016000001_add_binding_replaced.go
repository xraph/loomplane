package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// What an action ran as before it moved and still needs removing, kept
// with what runs it now: a list of bindings, empty for most.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_bindings").
				AddColumn("replaced", migrate.JSON, migrate.NotNull(), migrate.Default("[]")).IfNotExists().
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_bindings").
				DropColumn("replaced").IfExists().
				Exec(ctx)
		},
	)
}
