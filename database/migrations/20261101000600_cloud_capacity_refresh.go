package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewCreateTable("loomplane_cloud_capacity_refreshes").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("cloud_id", migrate.Text, migrate.NotNull()).
				Column("issued_generation", migrate.BigInt, migrate.NotNull(), migrate.Check("issued_generation>0")).
				Column("saved_generation", migrate.BigInt, migrate.NotNull(), migrate.Default(0),
					migrate.Check("saved_generation>=0 AND saved_generation<=issued_generation")).
				PrimaryKey("org_id", "cloud_id").
				ForeignKey("org_id", "cloud_id").References("loomplane_cloud_profiles", "org_id", "id").OnDelete(migrate.Cascade).
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropTable("loomplane_cloud_capacity_refreshes").IfExists().Exec(ctx)
		},
	)
}
