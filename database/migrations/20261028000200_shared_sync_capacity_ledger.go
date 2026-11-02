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
			return nil
		},
		nil,
	)
}
