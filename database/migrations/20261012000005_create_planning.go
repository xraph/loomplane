package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Planning keeps three things. A manifest revision is one saved version of
// a project's loomplane.yaml; saving adds one and never edits one, so a
// plan can name exactly what it was made from. A plan is pinned to the
// analysis and manifest revision it was made from, with its result whole,
// as JSON. The partial unique index allows one open plan per environment,
// so of two started at once, one fails instead of both waiting for a
// decision. A binding records what runs one service or resource in one
// environment; deployments write them and the next plan reads them.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_manifests").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_projects", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("revision", migrate.Integer, migrate.NotNull()).
				Column("body", migrate.Text, migrate.NotNull()).
				Column("digest", migrate.Text, migrate.NotNull()).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Unique("project_id", "revision").Named("loomplane_manifests_project_revision").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_plans").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_projects", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("environment_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_environments", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("environment", migrate.Text, migrate.NotNull()).
				Column("status", migrate.Text, migrate.NotNull()).
				Column("revision", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("analysis_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_analyses", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("commit_sha", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("manifest_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_manifests", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("manifest_revision", migrate.Integer, migrate.NotNull()).
				Column("result", migrate.JSON).
				Column("executable", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("error", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("planned_at", migrate.Timestamp).
				Column("expires_at", migrate.Timestamp).
				Column("decided_by", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("decided_at", migrate.Timestamp).
				Column("decision_note", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_plans_one_open").IfNotExists().
				On("loomplane_plans").
				Columns("environment_id").
				Unique().
				Where("status IN ('planning', 'ready')").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_plans_project_created").IfNotExists().
				On("loomplane_plans").
				Columns("org_id", "project_id").
				ColumnDesc("created_at").
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateTable("loomplane_bindings").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_projects", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("environment_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_environments", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("subject", migrate.Text, migrate.NotNull()).
				Column("name", migrate.Text, migrate.NotNull()).
				Column("provider", migrate.Text, migrate.NotNull()).
				Column("region", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("engine", migrate.Text, migrate.NotNull()).
				Column("external_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("config", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).
				Column("plan_id", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Unique("environment_id", "subject", "name").Named("loomplane_bindings_subject").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewDropTable("loomplane_bindings").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_plans").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewDropTable("loomplane_manifests").IfExists().Exec(ctx)
		},
	)
}
