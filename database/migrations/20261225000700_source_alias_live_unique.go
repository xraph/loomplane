package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A project's source alias is unique among its live sources only. Removing a
// source keeps its row for the analyses and plans that point at it by id, and
// the same repository can be added again under the alias. The service, not
// the index, keeps the alias from moving to another repository.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			if _, err := db.Exec(ctx, `ALTER TABLE loomplane_project_sources DROP CONSTRAINT IF EXISTS loomplane_project_sources_alias`); err != nil {
				return fmt.Errorf("drop constraint loomplane_project_sources_alias: %w", err)
			}
			if _, err := db.Exec(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS loomplane_project_sources_alias
	ON loomplane_project_sources (project_id, alias) WHERE removed_at IS NULL`); err != nil {
				return fmt.Errorf("create index loomplane_project_sources_alias: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			if _, err := db.Exec(ctx, `DROP INDEX IF EXISTS loomplane_project_sources_alias`); err != nil {
				return fmt.Errorf("drop index loomplane_project_sources_alias: %w", err)
			}
			// Fails if an alias has been reused since, which is right: those
			// rows can't coexist under the old rule.
			if _, err := db.Exec(ctx, `ALTER TABLE loomplane_project_sources ADD CONSTRAINT loomplane_project_sources_alias UNIQUE (project_id, alias)`); err != nil {
				return fmt.Errorf("add constraint loomplane_project_sources_alias: %w", err)
			}
			return nil
		},
	)
}
