package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// An analysis is one run of repository discovery; a succeeded one holds a
// version of the project's application graph, whole, as JSON. attempts
// counts deliveries of its job, so the worker knows when a transient
// failure has become the result.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_analyses").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_projects", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("status", migrate.Text, migrate.NotNull()).
				Column("repository", migrate.Text, migrate.NotNull()).
				Column("ref", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("commit_sha", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("error", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("summary", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).
				Column("graph", migrate.JSON).
				Column("attempts", migrate.Integer, migrate.NotNull(), migrate.Default(0)).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("started_at", migrate.Timestamp).
				Column("finished_at", migrate.Timestamp).
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateIndex("loomplane_analyses_project_created").IfNotExists().
				On("loomplane_analyses").
				Columns("org_id", "project_id").
				ColumnDesc("created_at").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropTable("loomplane_analyses").IfExists().Exec(ctx)
		},
	)
}
