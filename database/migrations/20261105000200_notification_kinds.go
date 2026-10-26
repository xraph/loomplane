package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// Outbox rows were all deployment notices until domain notices joined them.
var notificationKindsUp = []string{
	`ALTER TABLE loomplane_notification_outbox ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'deployment'`,
}

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			for i, q := range notificationKindsUp {
				if _, err := db.Exec(ctx, q); err != nil {
					return fmt.Errorf("notification_kinds statement %d: %w", i+1, err)
				}
			}
			return nil
		},
		nil,
	)
}
