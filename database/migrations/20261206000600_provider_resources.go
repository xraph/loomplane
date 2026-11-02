package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Inventory survives account disconnection. Account locks fence duplicate
// connections, and history retains safe outcomes without provider credentials.
func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if err := db.NewCreateTable("loomplane_provider_resource_accounts").
			Column("org_id", migrate.Text, migrate.NotNull()).
			Column("provider", migrate.Text, migrate.NotNull()).
			Column("account_identity", migrate.Text, migrate.NotNull()).
			Column("sequence", migrate.BigInt, migrate.NotNull(), migrate.Default(0)).
			PrimaryKey("org_id", "provider", "account_identity").Exec(ctx); err != nil {
			return err
		}
		for _, table := range []string{"loomplane_provider_resources", "loomplane_provider_resource_syncs"} {
			if err := db.NewCreateTable(table).
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("provider", migrate.Text, migrate.NotNull()).
				Column("account_identity", migrate.Text, migrate.NotNull()).
				Column("body", migrate.JSON, migrate.NotNull()).Exec(ctx); err != nil {
				return err
			}
			if err := db.NewCreateIndex(table+"_account").On(table).Columns("org_id", "provider", "account_identity").Exec(ctx); err != nil {
				return err
			}
		}
		return nil
	}, func(ctx context.Context, db *migrate.DB) error {
		for _, table := range []string{"loomplane_provider_resource_syncs", "loomplane_provider_resources", "loomplane_provider_resource_accounts"} {
			if err := db.NewDropTable(table).Exec(ctx); err != nil {
				return err
			}
		}
		return nil
	})
}
