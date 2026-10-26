package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// How often a failed deployment was retried.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_deployments").
				AddColumn("retries", migrate.Integer, migrate.NotNull(), migrate.Default(0)).IfNotExists().
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_deployments").
				DropColumn("retries").IfExists().
				Exec(ctx)
		},
	)
}
