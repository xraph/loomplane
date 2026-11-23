package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Runner waits are the builds waiting for a runner of a pool to be free,
// so each can say how many are ahead of it, whichever replica runs it. A
// wait is removed when its build gets a runner or ends; one left by a
// crashed worker expires.
func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		for _, stmt := range []string{
			`CREATE TABLE IF NOT EXISTS loomplane_runner_waits (
	operation_id TEXT PRIMARY KEY,
	org_id TEXT NOT NULL,
	pool TEXT NOT NULL,
	since TIMESTAMPTZ NOT NULL,
	seen_at TIMESTAMPTZ NOT NULL
)`,
			`CREATE INDEX IF NOT EXISTS loomplane_runner_waits_pool ON loomplane_runner_waits (pool, since)`,
		} {
			if _, err := db.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DROP TABLE IF EXISTS loomplane_runner_waits`)
		return err
	})
}
