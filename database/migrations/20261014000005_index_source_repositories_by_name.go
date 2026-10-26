package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// A push webhook finds the sources of a repository across organizations.
// GitHub names it in its own case and a source keeps the case it was
// added with, so the lookup compares lowercased, and the index does too.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateIndex("loomplane_project_sources_repository_lower").IfNotExists().
				On("loomplane_project_sources").
				ColumnExpr("lower(repository)").
				Where("removed_at IS NULL").
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewDropIndex("loomplane_project_sources_repository").IfExists().Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateIndex("loomplane_project_sources_repository").IfNotExists().
				On("loomplane_project_sources").
				Columns("repository").
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewDropIndex("loomplane_project_sources_repository_lower").IfExists().Exec(ctx)
		},
	)
}
