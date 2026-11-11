package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if err := db.NewCreateTable("loomplane_fleet_metric_schedule").
			Column("org_id", migrate.Text, migrate.NotNull()).Column("fleet_id", migrate.Text, migrate.NotNull()).Column("instance_id", migrate.Text, migrate.NotNull()).
			Column("next_at", migrate.Timestamp, migrate.NotNull()).Column("lease_id", migrate.Text, migrate.NotNull(), migrate.Default("")).Column("lease_until", migrate.Timestamp).
			PrimaryKey("org_id", "instance_id").ForeignKey("org_id", "fleet_id", "instance_id").References("loomplane_fleet_instances", "org_id", "fleet_id", "id").OnDelete(migrate.Restrict).Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateIndex("idx_fleet_metric_due").On("loomplane_fleet_metric_schedule").Columns("next_at", "org_id", "instance_id").Exec(ctx); err != nil {
			return err
		}
		if err := db.NewCreateTable("loomplane_fleet_metric_history").
			Column("org_id", migrate.Text, migrate.NotNull()).Column("fleet_id", migrate.Text, migrate.NotNull()).Column("instance_id", migrate.Text, migrate.NotNull()).
			Column("hour", migrate.Timestamp, migrate.NotNull()).Column("captured_at", migrate.Timestamp, migrate.NotNull()).
			Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Check("generation>0")).Column("ownership_epoch", migrate.BigInt, migrate.NotNull(), migrate.Check("ownership_epoch>0")).
			Column("body", migrate.JSON, migrate.NotNull(), migrate.Check("octet_length(body::text)<=1048576")).
			PrimaryKey("org_id", "instance_id", "hour").ForeignKey("org_id", "fleet_id", "instance_id").References("loomplane_fleet_instances", "org_id", "fleet_id", "id").OnDelete(migrate.Restrict).Exec(ctx); err != nil {
			return err
		}
		return db.NewCreateIndex("idx_fleet_metric_retention").On("loomplane_fleet_metric_history").Columns("hour", "org_id", "instance_id").Exec(ctx)
	}, func(ctx context.Context, db *migrate.DB) error {
		if err := db.NewDropTable("loomplane_fleet_metric_history").Exec(ctx); err != nil {
			return err
		}
		return db.NewDropTable("loomplane_fleet_metric_schedule").Exec(ctx)
	})
}
