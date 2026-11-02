package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// A project's exclusions keep services the analysis finds out of its graph,
// plans and deploys, on every branch (internal/projects/exclusions.go).
// They're a project setting rather than a manifest entry, so excluding a
// test fixture never changes the repository. Every project starts with
// none.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_projects").
				AddColumn("exclusions", migrate.JSON, migrate.NotNull(), migrate.Default("[]")).IfNotExists().
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_projects").
				DropColumn("exclusions").IfExists().
				Exec(ctx)
		},
	)
}
