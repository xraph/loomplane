package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Nodes are machines that join an org and hold one outbound connection. A
// node's name is unique among live nodes of an org, so a revoked node frees
// its name. A session row exists only while a replica holds the connection.
func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS loomplane_nodes (
	id TEXT PRIMARY KEY,
	org_id TEXT NOT NULL,
	name TEXT NOT NULL,
	public_key BYTEA NOT NULL,
	key_fingerprint TEXT NOT NULL,
	identity_generation BIGINT NOT NULL DEFAULT 1,
	status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','online','offline','draining','revoked')),
	endpoints JSONB NOT NULL DEFAULT '[]',
	ingress_mode TEXT NOT NULL DEFAULT 'none' CHECK (ingress_mode IN ('none','direct','relay')),
	role TEXT NOT NULL DEFAULT 'node' CHECK (role IN ('node','edge')),
	edge_scope TEXT NOT NULL DEFAULT '' CHECK (edge_scope IN ('','org','instance')),
	labels JSONB NOT NULL DEFAULT '{}',
	version TEXT NOT NULL DEFAULT '',
	protocol_version INTEGER NOT NULL DEFAULT 0,
	inventory JSONB NOT NULL DEFAULT '{}',
	usage JSONB NOT NULL DEFAULT '{}',
	session_epoch BIGINT NOT NULL DEFAULT 0,
	last_seen_at TIMESTAMPTZ,
	joined_by TEXT NOT NULL DEFAULT '',
	joined_by_key_id TEXT NOT NULL DEFAULT '',
	revoked_at TIMESTAMPTZ,
	revoked_by TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL,
	CONSTRAINT loomplane_nodes_edge_scope CHECK (edge_scope = '' OR role = 'edge')
)`); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS loomplane_nodes_live_name ON loomplane_nodes (org_id, name) WHERE revoked_at IS NULL`); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `CREATE INDEX IF NOT EXISTS loomplane_nodes_org ON loomplane_nodes (org_id, created_at)`); err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `CREATE INDEX IF NOT EXISTS loomplane_nodes_joined_key ON loomplane_nodes (org_id, joined_by_key_id) WHERE joined_by_key_id <> ''`); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS loomplane_node_sessions (
	node_id TEXT PRIMARY KEY REFERENCES loomplane_nodes(id) ON DELETE CASCADE,
	epoch BIGINT NOT NULL,
	replica TEXT NOT NULL DEFAULT '',
	remote_addr TEXT NOT NULL DEFAULT '',
	connected_at TIMESTAMPTZ NOT NULL
)`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		if _, err := db.Exec(ctx, `DO $$ BEGIN LOCK TABLE loomplane_nodes,loomplane_node_sessions IN ACCESS EXCLUSIVE MODE; IF EXISTS(SELECT 1 FROM loomplane_nodes) THEN RAISE EXCEPTION 'nodes must be retained'; END IF; END $$`); err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_node_sessions").Exec(ctx); err != nil {
			return err
		}
		return db.NewDropTable("loomplane_nodes").Exec(ctx)
	})
}
