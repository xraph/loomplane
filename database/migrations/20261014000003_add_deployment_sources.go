package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A deployment fetches one tarball per repository it moves. sources holds
// each one's alias, repository, commit and artifact key. source, the single
// key deployments used before, stays unread; the cleanup migration drops it
// with the other old columns.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_deployments").
				AddColumn("sources", migrate.JSON, migrate.NotNull(), migrate.Default("[]")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `UPDATE loomplane_deployments
				SET sources = jsonb_build_array(jsonb_build_object(
					'alias', '', 'repository', '', 'commit_sha', commit_sha, 'key', source))
				WHERE sources = '[]'::jsonb AND source <> ''`)
			if err != nil {
				return fmt.Errorf("backfill deployment sources: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewAlterTable("loomplane_deployments").
				DropColumn("sources").IfExists().
				Exec(ctx)
		},
	)
}
