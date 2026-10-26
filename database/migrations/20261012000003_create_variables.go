package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// A variable set on one layer: organization-wide (no project), a project,
// or one of its services; for every environment or for one. A project's
// variable for one environment references it, so deleting either deletes
// the variable. An organization-wide one names its environment instead,
// since it applies to that environment in every project. A secret's value
// isn't here at all: it's in Vault, encrypted.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_variables").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text,
					migrate.References("loomplane_projects", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("environment_id", migrate.Text,
					migrate.References("loomplane_environments", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("environment", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("service", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("key", migrate.Text, migrate.NotNull()).
				Column("value", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("secret", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("updated_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Check("project_id IS NOT NULL OR service = ''").Named("loomplane_variables_service_in_project").
				Check("project_id IS NOT NULL OR environment_id IS NULL").Named("loomplane_variables_environment_in_project").
				Check("NOT secret OR value = ''").Named("loomplane_variables_secret_not_stored").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_variables_layer_key").IfNotExists().
				On("loomplane_variables").
				Columns("org_id").
				ColumnExpr("COALESCE(project_id, '')").
				Columns("service", "environment", "key").
				Unique().
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateIndex("loomplane_variables_project").IfNotExists().
				On("loomplane_variables").
				Columns("org_id", "project_id").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropTable("loomplane_variables").IfExists().Exec(ctx)
		},
	)
}
