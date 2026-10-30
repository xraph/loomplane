package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS loomplane_dns_zones_scope_identity ON loomplane_dns_zones(org_id,id)`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		// PostgreSQL rejects removal while a scoped foreign key still depends on it.
		_, err := db.Exec(ctx, `DROP INDEX IF EXISTS loomplane_dns_zones_scope_identity`)
		return err
	})
}
