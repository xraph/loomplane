package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_cloud_capacity_policies").
				AddColumn("authority", migrate.JSON, migrate.NotNull(), migrate.Default("null")).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_cloud_expansions").
				AddColumn("revision", migrate.BigInt, migrate.NotNull(), migrate.Default(1), migrate.Check("revision>0")).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_cloud_expansions").
				AddColumn("approval_principal", migrate.JSON, migrate.NotNull(), migrate.Default("null")).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_expansion_operation").Unique().
				On("loomplane_cloud_expansions").
				Columns("org_id", "operation_id").
				Where("operation_id IS NOT NULL").
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateTable("loomplane_cloud_expansion_leases").
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("cloud_id", migrate.Text, migrate.NotNull()).
				Column("pool_id", migrate.Text, migrate.NotNull()).
				Column("owner", migrate.Text, migrate.NotNull()).
				Column("expires_at", migrate.Timestamp, migrate.NotNull()).
				PrimaryKey("org_id", "cloud_id", "pool_id").
				ForeignKey("org_id", "cloud_id", "pool_id").References("loomplane_server_pools", "org_id", "cloud_id", "id").OnDelete(migrate.Cascade).
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewDropTable("loomplane_cloud_expansion_leases").Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropIndex("loomplane_expansion_operation").Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_cloud_expansions").
				DropColumn("approval_principal").
				DropColumn("revision").
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewAlterTable("loomplane_cloud_capacity_policies").
				DropColumn("authority").
				Exec(ctx)
		},
	)
}
