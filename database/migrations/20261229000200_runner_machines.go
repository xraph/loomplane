package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Runner machines are the machines an on-demand runner pool starts in a
// cloud account when builds need runners, and removes after. Each is
// recorded before the cloud is asked for it, under an ID the cloud's
// operation tag carries, so a crash between the two never loses one. A cold
// machine takes one build and goes; a warm one takes builds until it is
// replaced.
func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		for _, stmt := range []string{
			`CREATE TABLE IF NOT EXISTS loomplane_runner_machines (
	id TEXT PRIMARY KEY,
	pool_id TEXT NOT NULL REFERENCES loomplane_runner_pools(id) ON DELETE RESTRICT,
	org_id TEXT NOT NULL,
	name TEXT NOT NULL,
	provider_id TEXT NOT NULL DEFAULT '',
	region TEXT NOT NULL,
	size TEXT NOT NULL,
	warm BOOLEAN NOT NULL DEFAULT false,
	state TEXT NOT NULL CHECK (state IN ('creating','booting','ready','retiring','gone')),
	node_id TEXT NOT NULL DEFAULT '',
	in_flight INTEGER NOT NULL DEFAULT 0,
	builds INTEGER NOT NULL DEFAULT 0,
	reason TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL,
	joined_at TIMESTAMPTZ,
	active_at TIMESTAMPTZ NOT NULL,
	gone_at TIMESTAMPTZ
)`,
			`CREATE INDEX IF NOT EXISTS loomplane_runner_machines_pool ON loomplane_runner_machines (pool_id) WHERE state <> 'gone'`,
			`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_runner_machines_node ON loomplane_runner_machines (node_id) WHERE node_id <> ''`,
		} {
			if _, err := db.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	}, func(ctx context.Context, db *migrate.DB) error {
		if _, err := db.Exec(ctx, `DO $$ BEGIN
	IF EXISTS (SELECT 1 FROM loomplane_runner_machines WHERE state <> 'gone') THEN
		RAISE EXCEPTION 'runner machines are still running; remove their pools first';
	END IF;
END $$`); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `DROP TABLE IF EXISTS loomplane_runner_machines`)
		return err
	})
}
