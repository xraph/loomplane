package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// External endpoints become workspace services: a database, cache or
// bucket registered by its connection string, described, labelled with the
// environment it serves, shared with every project or only some, checked
// with what its credentials may do, and given an optional admin credential.
//
// origin tells the ones a person registered from the ones a provider
// resource's Connect made for itself, named resource-<64 hex>-<revision>.
// Every endpoint stays shared with every project (restricted is false), as
// they all were before.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewAlterTable("loomplane_catalog_endpoints").
				AddColumn("origin", migrate.Text, migrate.NotNull(), migrate.Default("registered"),
					migrate.Check("origin IN ('registered','provider')")).IfNotExists().
				AddColumn("description", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				AddColumn("environment_label", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				AddColumn("restricted", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).IfNotExists().
				AddColumn("admin_set", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).IfNotExists().
				AddColumn("updated_at", migrate.Timestamp).IfNotExists().
				AddColumn("last_check", migrate.JSON).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}
			if _, err = db.Exec(ctx, `UPDATE loomplane_catalog_endpoints SET origin = 'provider'
				WHERE name ~ '^resource-[a-f0-9]{64}-[0-9]+$'`); err != nil {
				return err
			}
			return db.NewCreateTable("loomplane_catalog_endpoint_grants").IfNotExists().
				Column("endpoint_id", migrate.Text, migrate.NotNull(),
					migrate.References("loomplane_catalog_endpoints", "id"), migrate.OnDelete(migrate.Cascade)).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				PrimaryKey("endpoint_id", "project_id").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			if err := db.NewDropTable("loomplane_catalog_endpoint_grants").IfExists().Exec(ctx); err != nil {
				return err
			}
			return db.NewAlterTable("loomplane_catalog_endpoints").
				DropColumn("origin").IfExists().
				DropColumn("description").IfExists().
				DropColumn("environment_label").IfExists().
				DropColumn("restricted").IfExists().
				DropColumn("admin_set").IfExists().
				DropColumn("updated_at").IfExists().
				DropColumn("last_check").IfExists().
				Exec(ctx)
		},
	)
}
