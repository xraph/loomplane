package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A source is a repository, or a directory of one, that a project builds
// from; a project has any number. A removed source keeps its row, so its
// alias stays taken: plans and variables name sources by alias, and an
// alias must always mean the same repository. An analysis reads one
// source; its alias is copied onto it, which is safe because aliases never
// change.
//
// The backfill turns each project's repository_url into its first source,
// with the alias internal/discovery.AliasFor would pick, and qualifies the
// service IDs its variables name with that alias. A URL that isn't on
// GitHub gets no source: it could never be analyzed. The pattern is anchored
// to the hosts repositories.ParseGitHubURL accepts, so a look-alike such as
// mygithub.com or a gist doesn't slip through. repository_url stays,
// unread, so a rollback still finds it; a later migration drops it. Each
// UPDATE only touches a project with exactly one source, which keeps a
// re-run from guessing between two.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_project_sources").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_projects", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("alias", migrate.Text, migrate.NotNull()).
				Column("repository", migrate.Text, migrate.NotNull()).
				Column("root", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("ref", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Column("removed_at", migrate.Timestamp).
				Unique("project_id", "alias").Named("loomplane_project_sources_alias").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_project_sources_repository").IfNotExists().
				On("loomplane_project_sources").
				Columns("repository").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_analyses").
				AddColumn("source_id", migrate.Text,
					migrate.References("loomplane_project_sources", "id"), migrate.OnDelete(migrate.Cascade)).IfNotExists().
				AddColumn("alias", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_analyses_source_created").IfNotExists().
				On("loomplane_analyses").
				Columns("org_id", "source_id").
				ColumnDesc("created_at").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `WITH m AS (
					SELECT id, org_id, created_by, created_at,
						regexp_match(trim(repository_url),
							'^(?:(?:https?://)?(?:www\.)?github\.com(?::[0-9]+)?/|git@github\.com:)([A-Za-z0-9][A-Za-z0-9-]*)/([A-Za-z0-9._-]+)',
							'i') AS parts
					FROM loomplane_projects
					WHERE repository_url <> ''
				), r AS (
					SELECT id, org_id, created_by, created_at,
						parts[1] || '/' || regexp_replace(parts[2], '\.git$', '') AS repository,
						trim(both '-' FROM regexp_replace(lower(regexp_replace(parts[2], '\.git$', '')), '[^a-z0-9]+', '-', 'g')) AS base
					FROM m
					WHERE parts IS NOT NULL
				), a AS (
					SELECT *, rtrim(left(CASE WHEN base = '' THEN 'repo' WHEN base ~ '^[0-9]' THEN 'r-' || base ELSE base END, 32), '-') AS alias
					FROM r
				)
				INSERT INTO loomplane_project_sources (id, org_id, project_id, alias, repository, root, ref, created_by, created_at, updated_at)
				SELECT 'src_' || substr(md5('source:' || id), 1, 26), org_id, id,
					CASE WHEN alias IN ('go', 'rust', 'node', 'container') THEN alias || '-src' ELSE alias END,
					repository, '', '', created_by, created_at, created_at
				FROM a
				ON CONFLICT DO NOTHING`)
			if err != nil {
				return fmt.Errorf("backfill project sources: %w", err)
			}

			_, err = db.Exec(ctx, `UPDATE loomplane_analyses a
				SET source_id = s.id, alias = s.alias
				FROM loomplane_project_sources s
				WHERE a.source_id IS NULL AND s.project_id = a.project_id
					AND (SELECT count(*) FROM loomplane_project_sources x WHERE x.project_id = a.project_id) = 1`)
			if err != nil {
				return fmt.Errorf("backfill analysis sources: %w", err)
			}

			_, err = db.Exec(ctx, `UPDATE loomplane_variables v
				SET service = 'svc:' || s.alias || ':' || substr(v.service, 5)
				FROM loomplane_project_sources s
				WHERE v.project_id = s.project_id AND v.service LIKE 'svc:%'
					AND split_part(v.service, ':', 2) IN ('go', 'rust', 'node', 'container')
					AND (SELECT count(*) FROM loomplane_project_sources x WHERE x.project_id = v.project_id) = 1`)
			if err != nil {
				return fmt.Errorf("qualify variable services: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `UPDATE loomplane_variables v
				SET service = 'svc:' || substr(v.service, length('svc:' || s.alias || ':') + 1)
				FROM loomplane_project_sources s
				WHERE v.project_id = s.project_id AND v.service LIKE 'svc:' || s.alias || ':%'`)
			if err != nil {
				return fmt.Errorf("unqualify variable services: %w", err)
			}

			err = db.NewAlterTable("loomplane_analyses").
				DropColumn("alias").IfExists().
				DropColumn("source_id").IfExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewDropTable("loomplane_project_sources").IfExists().Exec(ctx)
		},
	)
}
