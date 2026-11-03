package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// A node signs a challenge nonce to connect. Any replica can check a nonce's
// MAC and expiry on its own; this table makes each nonce single use across
// replicas. Rows past their expiry are swept, and a nonce past its expiry is
// refused before it gets here, so dropping the table loses nothing that lasts.
func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS loomplane_node_nonces (
	nonce TEXT PRIMARY KEY,
	expires_at TIMESTAMPTZ NOT NULL
)`); err != nil {
			return err
		}
		_, err := db.Exec(ctx, `CREATE INDEX IF NOT EXISTS loomplane_node_nonces_expiry ON loomplane_node_nonces (expires_at)`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		return db.NewDropTable("loomplane_node_nonces").Exec(ctx)
	})
}
