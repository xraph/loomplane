package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Workspace teams: named groups of members that carry a role per project or
// for the whole workspace, a project's responsible team, and the queue of
// people whose Warden assignments need recomputing.
//
// These tables are Loomplane's own. authsome's authsome_teams stays unused
// and Loomplane blocks its routes. A row in loomplane_team_members grants
// nothing without a current authsome_members row, so a person who leaves the
// workspace keeps no team rights even if a membership row survives.
//
// A grant with a NULL project_id applies to the whole workspace; at most one
// grant exists per team and project, and at most one workspace grant per
// team. The queue rows are keyed by person, so queueing twice keeps one row.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_teams").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("name", migrate.Text, migrate.NotNull(), migrate.Check("char_length(name) BETWEEN 1 AND 80")).
				Column("slug", migrate.Text, migrate.NotNull()).
				Column("description", migrate.Text, migrate.NotNull(), migrate.Default(""),
					migrate.Check("char_length(description) <= 500")).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Unique("org_id", "slug").Named("loomplane_teams_slug").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_team_members").IfNotExists().
				Column("team_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_teams", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("user_id", migrate.Text, migrate.NotNull()).
				Column("added_by", migrate.Text, migrate.NotNull()).
				Column("added_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				PrimaryKey("team_id", "user_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_team_members_user").IfNotExists().
				On("loomplane_team_members").
				Columns("org_id", "user_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_team_grants").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("team_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_teams", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("role", migrate.Text, migrate.NotNull(), migrate.Check("role IN ('viewer','developer','admin')")).
				Column("project_id", migrate.Text,
					migrate.References("loomplane_projects", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_team_grants_scope").IfNotExists().
				On("loomplane_team_grants").
				Columns("team_id").
				ColumnExpr("coalesce(project_id, '')").
				Unique().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_team_grants_project").IfNotExists().
				On("loomplane_team_grants").
				Columns("project_id").
				Where("project_id IS NOT NULL").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewAlterTable("loomplane_projects").
				AddColumn("responsible_team_id", migrate.Text,
					migrate.References("loomplane_teams", "id"), migrate.OnDelete(migrate.SetNull)).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateTable("loomplane_authz_sync").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("user_id", migrate.Text, migrate.NotNull()).
				Column("queued_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				PrimaryKey("org_id", "user_id").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_projects").
				DropColumn("responsible_team_id").IfExists().
				Exec(ctx)
			if err != nil {
				return err
			}
			for _, table := range []string{"loomplane_authz_sync", "loomplane_team_grants", "loomplane_team_members", "loomplane_teams"} {
				if err := db.NewDropTable(table).IfExists().Exec(ctx); err != nil {
					return err
				}
			}
			return nil
		},
	)
}
