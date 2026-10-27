package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A plan pins one analysis of each source it was made from, and whether
// it moves that source or holds it at what's deployed. Each row copies the
// source's alias, repository and directory, so a plan recomposes exactly
// even after someone changes a source. An environment's sources are what
// a deployment last put there; only a successful deployment writes them.
// The backfill gives every older plan the one source it was made from,
// moving. loomplane_plans.analysis_id and commit_sha stay, unread, until a
// later migration drops them.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_plan_sources").IfNotExists().
				Column("plan_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_plans", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("source_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_project_sources", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("alias", migrate.Text, migrate.NotNull()).
				Column("repository", migrate.Text, migrate.NotNull()).
				Column("root", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("analysis_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_analyses", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("commit_sha", migrate.Text, migrate.NotNull()).
				Column("in_scope", migrate.Boolean, migrate.NotNull()).
				PrimaryKey("plan_id", "source_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_environment_sources").IfNotExists().
				Column("environment_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_environments", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("source_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_project_sources", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("analysis_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_analyses", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("commit_sha", migrate.Text, migrate.NotNull()).
				Column("plan_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_plans", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("deployed_at", migrate.Timestamp, migrate.NotNull()).
				PrimaryKey("environment_id", "source_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_plans").
				DropNotNull("analysis_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `INSERT INTO loomplane_plan_sources (plan_id, source_id, alias, repository, root, analysis_id, commit_sha, in_scope)
				SELECT p.id, a.source_id, a.alias, a.repository, '', p.analysis_id, p.commit_sha, TRUE
				FROM loomplane_plans p
				JOIN loomplane_analyses a ON a.id = p.analysis_id
				WHERE a.source_id IS NOT NULL
				ON CONFLICT DO NOTHING`)
			if err != nil {
				return fmt.Errorf("backfill plan sources: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewDropTable("loomplane_environment_sources").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewDropTable("loomplane_plan_sources").IfExists().Exec(ctx)
		},
	)
}
