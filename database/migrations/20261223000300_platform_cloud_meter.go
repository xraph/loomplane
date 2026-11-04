package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// What the platform Cloud meter keeps between samples, so a restarted
// sampler neither loses nor repeats usage.
//
// loomplane_platform_meter_compute holds the minutes one placement generation
// ran in one hour, with the size it ran at and the last minute counted. The
// hour's events go to the meter once the hour has closed, and emitted says
// they have.
//
// loomplane_platform_meter_egress holds the bytes one placement sent in one
// hour, summed from the deltas of the host's counters.
//
// Neither table references placements: usage outlives the placement row.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_platform_meter_compute").IfNotExists().
				Column("placement_id", migrate.Text, migrate.NotNull()).
				Column("generation", migrate.BigInt, migrate.NotNull()).
				Column("hour", migrate.Timestamp, migrate.NotNull()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("vcpus", migrate.Integer, migrate.NotNull()).
				Column("memory_mib", migrate.Integer, migrate.NotNull()).
				Column("minutes", migrate.Integer, migrate.NotNull(), migrate.Default(0), migrate.Check("minutes>=0")).
				Column("last_minute", migrate.Timestamp, migrate.NotNull()).
				Column("emitted", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				PrimaryKey("placement_id", "generation", "hour").
				Exec(ctx)
			if err != nil {
				return err
			}
			err = db.NewCreateIndex("loomplane_platform_meter_compute_open").IfNotExists().
				On("loomplane_platform_meter_compute").
				Columns("hour").
				Where("NOT emitted").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_platform_meter_egress").IfNotExists().
				Column("placement_id", migrate.Text, migrate.NotNull()).
				Column("hour", migrate.Timestamp, migrate.NotNull()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("bytes", migrate.BigInt, migrate.NotNull(), migrate.Default(0), migrate.Check("bytes>=0")).
				Column("emitted", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				PrimaryKey("placement_id", "hour").
				Exec(ctx)
			if err != nil {
				return err
			}
			return db.NewCreateIndex("loomplane_platform_meter_egress_open").IfNotExists().
				On("loomplane_platform_meter_egress").
				Columns("hour").
				Where("NOT emitted").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			if err := db.NewDropTable("loomplane_platform_meter_egress").IfExists().Exec(ctx); err != nil {
				return err
			}
			return db.NewDropTable("loomplane_platform_meter_compute").IfExists().Exec(ctx)
		},
	)
}
