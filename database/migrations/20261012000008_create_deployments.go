package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// A deployment is one run of an approved plan, at most one per plan, with
// a step per action of the plan. source is the key of the commit's
// tarball in artifact storage.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_deployments").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_projects", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("environment_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_environments", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("environment", migrate.Text, migrate.NotNull()).
				Column("plan_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_plans", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("commit_sha", migrate.Text, migrate.NotNull()).
				Column("status", migrate.Text, migrate.NotNull()).
				Column("source", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("error", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("steps", migrate.JSON, migrate.NotNull(), migrate.Default("[]")).
				Column("started_by", migrate.Text, migrate.NotNull()).
				Column("started_at", migrate.Timestamp, migrate.NotNull()).
				Column("finished_at", migrate.Timestamp).
				Unique("plan_id").Named("loomplane_deployments_plan").
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateIndex("loomplane_deployments_project_started").IfNotExists().
				On("loomplane_deployments").
				Columns("org_id", "project_id").
				ColumnDesc("started_at").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropTable("loomplane_deployments").IfExists().Exec(ctx)
		},
	)
}
