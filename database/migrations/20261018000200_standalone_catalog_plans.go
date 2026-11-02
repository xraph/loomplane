package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_plans").
				AddColumn("catalog", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).IfNotExists().
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_plans").
				DropColumn("catalog").IfExists().
				Exec(ctx)
		},
	)
}
