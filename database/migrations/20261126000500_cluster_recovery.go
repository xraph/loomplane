package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE INDEX IF NOT EXISTS loomplane_cluster_operations_account ON loomplane_cluster_operations(org_id,(body->'input'->>'provider'),(body->'input'->>'account_id')) WHERE status IN ('accepted','running','waiting_provider','needs_attention');`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DROP INDEX IF EXISTS loomplane_cluster_operations_account;`)
		return err
	})
}
