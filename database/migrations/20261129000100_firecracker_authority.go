package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if err := db.NewCreateTable("loomplane_firecracker_resources").Column("resource_key", migrate.Text, migrate.PrimaryKey()).Column("scope", migrate.JSON, migrate.NotNull()).Column("generation", migrate.BigInt, migrate.NotNull()).Exec(ctx); err != nil {
			return err
		}
		return db.NewCreateTable("loomplane_firecracker_operations").Column("id", migrate.Text, migrate.PrimaryKey()).Column("resource_key", migrate.Text, migrate.NotNull(), migrate.References("loomplane_firecracker_resources", "resource_key")).Column("operation", migrate.Text, migrate.NotNull()).Column("generation", migrate.BigInt, migrate.NotNull()).Column("digest", migrate.Text, migrate.NotNull()).Column("expires_at", migrate.Timestamp, migrate.NotNull()).Exec(ctx)
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_firecracker_resources) THEN RAISE EXCEPTION 'Firecracker ownership history must be retained'; END IF; END $$`)
		if err != nil {
			return err
		}
		if err := db.NewDropTable("loomplane_firecracker_operations").Exec(ctx); err != nil {
			return err
		}
		return db.NewDropTable("loomplane_firecracker_resources").Exec(ctx)
	})
}
