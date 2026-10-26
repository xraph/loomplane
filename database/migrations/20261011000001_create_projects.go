package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Projects belong to an organization, an authsome organization ID. There's
// no foreign key to authsome's tables: they're authsome's, and Loomplane
// never writes them.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_projects").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("name", migrate.Text, migrate.NotNull()).
				Column("slug", migrate.Text, migrate.NotNull()).
				Column("description", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("repository_url", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("status", migrate.Text, migrate.NotNull()).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Unique("org_id", "slug").Named("loomplane_projects_org_slug").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_projects_org_created").IfNotExists().
				On("loomplane_projects").
				Columns("org_id").
				ColumnDesc("created_at").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_environments").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_projects", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("name", migrate.Text, migrate.NotNull()).
				Column("kind", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Unique("project_id", "name").Named("loomplane_environments_project_name").
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateIndex("loomplane_environments_org_project").IfNotExists().
				On("loomplane_environments").
				Columns("org_id", "project_id").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			if err := db.NewDropTable("loomplane_environments").IfExists().Exec(ctx); err != nil {
				return err
			}
			return db.NewDropTable("loomplane_projects").IfExists().Exec(ctx)
		},
	)
}
