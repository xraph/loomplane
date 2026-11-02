package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_backup_policies").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("environment", migrate.Text, migrate.NotNull()).
				Column("resource", migrate.Text, migrate.NotNull()).
				Column("every_hours", migrate.Integer, migrate.NotNull(), migrate.Check("every_hours BETWEEN 1 AND 168")).
				Column("enabled", migrate.Boolean, migrate.NotNull()).
				Column("next_at", migrate.Timestamp, migrate.NotNull()).
				Column("last_error", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Unique("org_id", "environment_id", "resource").
				ForeignKey("org_id", "project_id", "environment_id").
				References("loomplane_environments", "org_id", "project_id", "id").OnDelete(migrate.Cascade).
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateIndex("loomplane_backup_policies_due").IfNotExists().
				On("loomplane_backup_policies").
				Columns("next_at").
				Where("enabled=true").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropTable("loomplane_backup_policies").IfExists().Exec(ctx)
		},
	)
}
