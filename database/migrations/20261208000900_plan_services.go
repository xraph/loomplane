package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// A plan can name the manifest services it moves, so a deploy of one
// service leaves the others as they run (internal/planning). An empty list
// moves every service, which is what every plan made before this did.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_plans").
				AddColumn("services", migrate.JSON, migrate.NotNull(), migrate.Default("[]")).IfNotExists().
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_plans").
				DropColumn("services").IfExists().
				Exec(ctx)
		},
	)
}
