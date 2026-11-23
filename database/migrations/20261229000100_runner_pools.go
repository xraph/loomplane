package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Runner pools group runners: nodes with the runner role that build each
// image in a fresh Firecracker microVM and relay deploys. A pool belongs to
// the platform (org_id empty), an organization, or one project of it, and an
// organization or project pool is named in plans by its runner connection.
// The pool pins the runtime identity and architecture of its first checked
// runner, so every build it takes runs on the same reviewed toolchain.
// A runner belongs to one pool for its whole life.
func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		for _, stmt := range []string{
			`ALTER TABLE loomplane_nodes DROP CONSTRAINT IF EXISTS loomplane_nodes_role_check`,
			`ALTER TABLE loomplane_nodes ADD CONSTRAINT loomplane_nodes_role_check CHECK (role IN ('node','edge','runner'))`,
			`CREATE TABLE IF NOT EXISTS loomplane_runner_pools (
	id TEXT PRIMARY KEY,
	org_id TEXT NOT NULL DEFAULT '',
	scope TEXT NOT NULL CHECK (scope IN ('platform','organization','project')),
	project_id TEXT NOT NULL DEFAULT '',
	name TEXT NOT NULL,
	connection_id TEXT NOT NULL DEFAULT '',
	mode TEXT NOT NULL DEFAULT 'self_hosted' CHECK (mode IN ('self_hosted','on_demand')),
	architecture TEXT NOT NULL DEFAULT '',
	runtime_identity TEXT NOT NULL DEFAULT '',
	settings JSONB NOT NULL DEFAULT '{}',
	revision BIGINT NOT NULL DEFAULT 1,
	created_by TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL,
	CONSTRAINT loomplane_runner_pools_owner CHECK ((scope = 'platform') = (org_id = '')),
	CONSTRAINT loomplane_runner_pools_project CHECK ((scope = 'project') = (project_id <> '')),
	CONSTRAINT loomplane_runner_pools_connection CHECK (scope = 'platform' OR connection_id <> '')
)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_runner_pools_name ON loomplane_runner_pools (org_id, name)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_runner_pools_connection_id ON loomplane_runner_pools (connection_id) WHERE connection_id <> ''`,
			`CREATE TABLE IF NOT EXISTS loomplane_runner_members (
	node_id TEXT PRIMARY KEY REFERENCES loomplane_nodes(id) ON DELETE CASCADE,
	pool_id TEXT NOT NULL REFERENCES loomplane_runner_pools(id) ON DELETE RESTRICT,
	org_id TEXT NOT NULL,
	architecture TEXT NOT NULL DEFAULT '',
	runtime_identity TEXT NOT NULL DEFAULT '',
	evidence JSONB NOT NULL DEFAULT '{}',
	reason TEXT NOT NULL DEFAULT '',
	checked_at TIMESTAMPTZ,
	builds INTEGER NOT NULL DEFAULT 0,
	last_build_at TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL
)`,
			`CREATE INDEX IF NOT EXISTS loomplane_runner_members_pool ON loomplane_runner_members (pool_id)`,
		} {
			if _, err := db.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	}, func(ctx context.Context, db *migrate.DB) error {
		if _, err := db.Exec(ctx, `DO $$ BEGIN
	IF EXISTS (SELECT 1 FROM loomplane_runner_pools) OR EXISTS (SELECT 1 FROM loomplane_nodes WHERE role = 'runner') THEN
		RAISE EXCEPTION 'runner pools or runners exist; remove them before rolling back';
	END IF;
END $$`); err != nil {
			return err
		}
		for _, stmt := range []string{
			`DROP TABLE IF EXISTS loomplane_runner_members`,
			`DROP TABLE IF EXISTS loomplane_runner_pools`,
			`ALTER TABLE loomplane_nodes DROP CONSTRAINT IF EXISTS loomplane_nodes_role_check`,
			`ALTER TABLE loomplane_nodes ADD CONSTRAINT loomplane_nodes_role_check CHECK (role IN ('node','edge'))`,
		} {
			if _, err := db.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	})
}
