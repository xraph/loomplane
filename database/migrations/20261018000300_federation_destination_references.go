package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Reference insertion and destination removal serialize through the composite
// foreign key. Ownership records must be explicitly released after revocation
// or cleanup; deleting a display alias cannot cascade away runtime authority.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateIndex("loomplane_federation_destinations_scope_id").IfNotExists().
				On("loomplane_federation_destinations").
				Columns("org_id", "project_id", "environment_id", "id").
				Unique().
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateTable("loomplane_federation_destination_refs").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("destination_id", migrate.Text, migrate.NotNull()).
				Column("kind", migrate.Text, migrate.NotNull(),
					migrate.Check("kind IN ('agent','export','placement','network','policy','trust','cleanup')")).
				Column("owner_id", migrate.Text, migrate.NotNull(), migrate.Check("owner_id<>''")).
				PrimaryKey("org_id", "project_id", "environment_id", "destination_id", "kind", "owner_id").
				ForeignKey("org_id", "project_id", "environment_id", "destination_id").
				References("loomplane_federation_destinations", "org_id", "project_id", "environment_id", "id").
				OnDelete(migrate.Restrict).
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewDropTable("loomplane_federation_destination_refs").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewDropIndex("loomplane_federation_destinations_scope_id").IfExists().Exec(ctx)
		},
	)
}
