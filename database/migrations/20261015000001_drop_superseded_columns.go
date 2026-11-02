package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// Sources and plan locksets replaced a project's repository_url, a plan's
// analysis_id and commit_sha, and a deployment's source, and the
// migrations that brought them in copied every value across. Nothing reads
// the old columns, so they go. Going down puts them back and fills them
// from what replaced them: a project's oldest live source, a plan's only
// source when it has just one, and a deployment's first source's key.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_projects").
				DropColumn("repository_url").IfExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_plans").
				DropColumn("analysis_id").IfExists().
				DropColumn("commit_sha").IfExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewAlterTable("loomplane_deployments").
				DropColumn("source").IfExists().
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_projects").
				AddColumn("repository_url", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_plans").
				AddColumn("analysis_id", migrate.Text,
					migrate.References("loomplane_analyses", "id"), migrate.OnDelete(migrate.Cascade)).IfNotExists().
				AddColumn("commit_sha", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_deployments").
				AddColumn("source", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `UPDATE loomplane_projects p
				SET repository_url = 'https://github.com/' || s.repository
				FROM (
					SELECT DISTINCT ON (project_id) project_id, repository
					FROM loomplane_project_sources
					WHERE removed_at IS NULL
					ORDER BY project_id, created_at, id
				) s
				WHERE s.project_id = p.id AND p.repository_url = ''`)
			if err != nil {
				return fmt.Errorf("restore project repository urls: %w", err)
			}

			_, err = db.Exec(ctx, `UPDATE loomplane_plans p
				SET analysis_id = ps.analysis_id, commit_sha = ps.commit_sha
				FROM loomplane_plan_sources ps
				WHERE ps.plan_id = p.id AND p.analysis_id IS NULL
					AND (SELECT count(*) FROM loomplane_plan_sources x WHERE x.plan_id = p.id) = 1`)
			if err != nil {
				return fmt.Errorf("restore plan analyses: %w", err)
			}

			_, err = db.Exec(ctx, `UPDATE loomplane_deployments
				SET source = sources->0->>'key'
				WHERE source = '' AND jsonb_typeof(sources) = 'array' AND sources->0->>'key' IS NOT NULL`)
			if err != nil {
				return fmt.Errorf("restore deployment sources: %w", err)
			}
			return nil
		},
	)
}
