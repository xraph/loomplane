package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_pool_execution").
				AddColumn("generation", migrate.BigInt, migrate.NotNull(), migrate.Default(0)).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			// Upgrade the approval function as well as fresh installations.
			_, err = db.Exec(ctx, reservePoolPlanSQL)
			if err != nil {
				return fmt.Errorf("create function loomplane_reserve_pool_plan: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_pool_execution").
				DropColumn("generation").IfExists().
				Exec(ctx)
		},
	)
}
