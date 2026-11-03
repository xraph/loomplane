package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// A node's key belongs to that node for good. A revoked node keeps its row,
// so the index covers revoked nodes too: a machine that rejoins with the key
// of a node Loomplane revoked is refused, and has to make a fresh key.
func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS loomplane_nodes_key_fingerprint ON loomplane_nodes (key_fingerprint)`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DROP INDEX IF EXISTS loomplane_nodes_key_fingerprint`)
		return err
	})
}
