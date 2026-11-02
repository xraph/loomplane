package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// A plan remembers the branch each repository is read from, for targets
// that build a branch rather than a commit. Plans made before this read
// "", the default branch, which is what they were analysed from.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_plan_sources").
				AddColumn("ref", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_plan_sources").
				DropColumn("ref").IfExists().
				Exec(ctx)
		},
	)
}
