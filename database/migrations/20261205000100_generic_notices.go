package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// Outbox rows were notices about a deployment, a connection or a domain,
// each keyed by its own deployment_id. A notice of any kind names its
// subject instead, and one is queued once per kind, subject and person.
// The deployment_id and attempt columns keep their unique pair, which
// every existing kind relies on, so they get defaults and a generic row
// fills them with its own id. Preferences gain a second email switch, for
// the agent's notices.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			for _, q := range []string{
				`ALTER TABLE loomplane_notification_outbox ADD COLUMN IF NOT EXISTS subject_id TEXT NOT NULL DEFAULT ''`,
				`ALTER TABLE loomplane_notification_outbox ALTER COLUMN deployment_id SET DEFAULT ''`,
				`ALTER TABLE loomplane_notification_outbox ALTER COLUMN attempt SET DEFAULT 0`,
				`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_notification_outbox_subject ON loomplane_notification_outbox(kind, subject_id, user_id) WHERE subject_id<>''`,
				`ALTER TABLE loomplane_notification_preferences ADD COLUMN IF NOT EXISTS agent_email BOOLEAN NOT NULL DEFAULT TRUE`,
			} {
				if _, err := db.Exec(ctx, q); err != nil {
					return fmt.Errorf("generic notices: %w", err)
				}
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			for _, q := range []string{
				`DROP INDEX IF EXISTS loomplane_notification_outbox_subject`,
				`ALTER TABLE loomplane_notification_outbox DROP COLUMN IF EXISTS subject_id`,
				`ALTER TABLE loomplane_notification_preferences DROP COLUMN IF EXISTS agent_email`,
			} {
				if _, err := db.Exec(ctx, q); err != nil {
					return fmt.Errorf("generic notices down: %w", err)
				}
			}
			return nil
		},
	)
}
