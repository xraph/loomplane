package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		return db.NewAlterTable("loomplane_variables").AddColumn("resource_output", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().Exec(ctx)
	}, func(ctx context.Context, db *migrate.DB) error {
		return db.NewAlterTable("loomplane_variables").DropColumn("resource_output").IfExists().Exec(ctx)
	})
}
