package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, serverCapacitySQL)
			if err != nil {
				return fmt.Errorf("create function loomplane_server_capacity: %w", err)
			}

			_, err = db.Exec(ctx, reservePoolPlanSQL)
			if err != nil {
				return fmt.Errorf("create function loomplane_reserve_pool_plan: %w", err)
			}

			_, err = db.Exec(ctx, poolBindingCapacitySQL)
			if err != nil {
				return fmt.Errorf("create function loomplane_pool_binding_capacity: %w", err)
			}
			return nil
		},
		nil,
	)
}
