package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// A node that joined with a token scoped to a Cloud waits here to be adopted
// into it. The token's minter is the approval: approved_by and
// approved_by_key_id are who the adoption runs as. One row per node, which
// is how a second connect knows the node was adopted already. A row records
// an approval, so down refuses to drop rows that hold one.
func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS loomplane_node_adoptions (
	node_id TEXT PRIMARY KEY REFERENCES loomplane_nodes(id) ON DELETE CASCADE,
	org_id TEXT NOT NULL,
	cloud_id TEXT NOT NULL DEFAULT '',
	approved_by TEXT NOT NULL,
	approved_by_key_id TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL CHECK (status IN ('waiting','applied','review','failed')),
	reason TEXT NOT NULL DEFAULT '',
	server_id TEXT NOT NULL DEFAULT '',
	operation_id TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL,
	CONSTRAINT loomplane_node_adoptions_cloud CHECK (cloud_id <> '' OR status = 'failed')
)`); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `CREATE INDEX IF NOT EXISTS loomplane_node_adoptions_cloud ON loomplane_node_adoptions (org_id, cloud_id, status)`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		if _, err := db.Exec(ctx, `DO $$ BEGIN LOCK TABLE loomplane_node_adoptions IN ACCESS EXCLUSIVE MODE; IF EXISTS(SELECT 1 FROM loomplane_node_adoptions) THEN RAISE EXCEPTION 'node adoptions must be retained'; END IF; END $$`); err != nil {
			return err
		}
		return db.NewDropTable("loomplane_node_adoptions").Exec(ctx)
	})
}
