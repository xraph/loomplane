package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewCreateTable("loomplane_cloud_capacity_accounts").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("provider", migrate.Text, migrate.NotNull()).
				Column("account_id", migrate.Text, migrate.NotNull()).
				Column("servers", migrate.BigInt, migrate.Check("servers>=0")).
				Column("cpu_millis", migrate.BigInt, migrate.Check("cpu_millis>=0")).
				Column("memory_bytes", migrate.BigInt, migrate.Check("memory_bytes>=0")).
				Column("storage_bytes", migrate.BigInt, migrate.Check("storage_bytes>=0")).
				Column("quota_servers", migrate.BigInt, migrate.Check("quota_servers>=0")).
				Column("quota_cpu_millis", migrate.BigInt, migrate.Check("quota_cpu_millis>=0")).
				Column("quota_memory_bytes", migrate.BigInt, migrate.Check("quota_memory_bytes>=0")).
				Column("quota_storage_bytes", migrate.BigInt, migrate.Check("quota_storage_bytes>=0")).
				Column("checked_at", migrate.Timestamp, migrate.NotNull()).
				Column("body", migrate.JSON, migrate.NotNull()).
				PrimaryKey("org_id", "provider", "account_id").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropTable("loomplane_cloud_capacity_accounts").IfExists().Exec(ctx)
		},
	)
}
