package migrations

import (
	"context"
	"fmt"

	conduitpg "github.com/xraph/forge/extensions/conduit/providers/postgres"
	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			for i, stmt := range conduitpg.Migrations("loomplane_realtime_broker") {
				_, err := db.Exec(ctx, stmt)
				if err != nil {
					return fmt.Errorf("conduit broker schema statement %d: %w", i+1, err)
				}
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DROP SCHEMA IF EXISTS loomplane_realtime_broker CASCADE`)
			if err != nil {
				return fmt.Errorf("drop schema loomplane_realtime_broker: %w", err)
			}
			return nil
		},
	)
}
