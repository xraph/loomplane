package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// An outbox row gains a push step. push_state says where it stands,
// push_devices remembers what each phone got so a retried row never pushes
// twice, and push_collapse is the collapse ID the push used. inbox_id is
// the inbox item the row wrote. Rows that were already finished never push.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			for _, q := range []string{
				`ALTER TABLE loomplane_notification_outbox ADD COLUMN IF NOT EXISTS push_state TEXT NOT NULL DEFAULT 'pending' CHECK (push_state IN ('pending','sent','skipped','failed'))`,
				`ALTER TABLE loomplane_notification_outbox ADD COLUMN IF NOT EXISTS push_devices JSONB NOT NULL DEFAULT '{}'`,
				`ALTER TABLE loomplane_notification_outbox ADD COLUMN IF NOT EXISTS push_collapse TEXT NOT NULL DEFAULT ''`,
				`ALTER TABLE loomplane_notification_outbox ADD COLUMN IF NOT EXISTS inbox_id TEXT NOT NULL DEFAULT ''`,
				`UPDATE loomplane_notification_outbox SET push_state='skipped' WHERE completed_at IS NOT NULL AND push_state='pending'`,
			} {
				if _, err := db.Exec(ctx, q); err != nil {
					return fmt.Errorf("push outbox: %w", err)
				}
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			for _, q := range []string{
				`ALTER TABLE loomplane_notification_outbox DROP COLUMN IF EXISTS inbox_id`,
				`ALTER TABLE loomplane_notification_outbox DROP COLUMN IF EXISTS push_collapse`,
				`ALTER TABLE loomplane_notification_outbox DROP COLUMN IF EXISTS push_devices`,
				`ALTER TABLE loomplane_notification_outbox DROP COLUMN IF EXISTS push_state`,
			} {
				if _, err := db.Exec(ctx, q); err != nil {
					return fmt.Errorf("push outbox down: %w", err)
				}
			}
			return nil
		},
	)
}
