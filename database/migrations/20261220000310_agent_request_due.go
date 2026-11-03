package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// The agent requests' sweep reads pending requests a page at a time, by
// when they were made, so a page starts where the last one stopped without
// scanning the requests settled long ago.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewCreateIndex("loomplane_agent_requests_due").IfNotExists().
				On("loomplane_agent_requests").
				Columns("created_at", "id").
				Where("status='pending'").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DROP INDEX IF EXISTS loomplane_agent_requests_due`)
			return err
		},
	)
}
