package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// The Octopus gateway Loomplane runs for an environment, and the manual
// routes people add to it. generation counts every change to what the
// gateway should serve; applied_generation is the one the syncer last
// pushed to Octopus, and state is what that sync saw. Tokens never live
// here, only in Vault.
//
// A sync holds the gateway through a lease on its row: sync_lease_token
// names the holder and sync_lease_until is when the lease runs out, NULL
// when nobody holds it. A holder that crashed leaves the lease to expire.
//
// A route belongs to its gateway: deleting the gateway deletes its routes.
// A gateway serves one route per hostname and path.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_octopus_gateways").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("resource", migrate.Text, migrate.NotNull()).
				Column("provider", migrate.Text, migrate.NotNull()).
				Column("namespace", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("farp_hostnames", migrate.ArrayOf(migrate.Text), migrate.NotNull(), migrate.Default("{}")).
				Column("generation", migrate.BigInt, migrate.NotNull(), migrate.Default(1)).
				Column("applied_generation", migrate.BigInt, migrate.NotNull(), migrate.Default(0)).
				Column("state", migrate.JSON, migrate.NotNull(), migrate.Default("{}")).
				Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Default(1)).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Column("sync_lease_token", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("sync_lease_until", migrate.Timestamp).
				PrimaryKey("org_id", "environment_id", "resource").
				Exec(ctx)
			if err != nil {
				return err
			}
			// A table an earlier run of this migration made has no lease yet.
			err = db.NewAlterTable("loomplane_octopus_gateways").
				AddColumn("sync_lease_token", migrate.Text, migrate.NotNull(), migrate.Default("")).IfNotExists().
				AddColumn("sync_lease_until", migrate.Timestamp).IfNotExists().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_octopus_routes").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("resource", migrate.Text, migrate.NotNull()).
				Column("hostname", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("path", migrate.Text, migrate.NotNull()).
				Column("methods", migrate.ArrayOf(migrate.Text), migrate.NotNull(), migrate.Default("{}")).
				Column("service", migrate.Text, migrate.NotNull()).
				Column("port", migrate.Integer, migrate.NotNull(),
					migrate.Check("port BETWEEN 1 AND 65535")).
				Column("strip_prefix", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("note", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("revision", migrate.BigInt, migrate.NotNull(), migrate.Default(1)).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				ForeignKey("org_id", "environment_id", "resource").
				References("loomplane_octopus_gateways", "org_id", "environment_id", "resource").
				OnDelete(migrate.Cascade).
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateIndex("loomplane_octopus_route_path").IfNotExists().
				On("loomplane_octopus_routes").
				Columns("org_id", "environment_id", "resource", "hostname", "path").
				Unique().
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			// Routes first: they reference the gateways.
			if err := db.NewDropTable("loomplane_octopus_routes").IfExists().Exec(ctx); err != nil {
				return err
			}
			return db.NewDropTable("loomplane_octopus_gateways").IfExists().Exec(ctx)
		},
	)
}
