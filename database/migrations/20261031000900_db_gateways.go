package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Database gateways: one row per deployed gateway, with the public keys the
// control plane verifies and seals to. Routes are one per opted-in Postgres
// resource and keep their SCRAM salt for good, so a route's verifiers stay
// valid across snapshots. Snapshots hold only the latest version and digest.
// Instances are each running gateway's last report. A gateway owns its
// routes, snapshot and instances, and the environment owns the gateway.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateIndex("loomplane_environments_scope_id").Unique().IfNotExists().
				On("loomplane_environments").
				Columns("org_id", "project_id", "id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_db_gateways").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("name", migrate.Text, migrate.NotNull()).
				Column("provider", migrate.Text, migrate.NotNull()).
				Column("region", migrate.Text, migrate.NotNull()).
				Column("suffix", migrate.Text, migrate.NotNull()).
				Column("signing_public_key", migrate.Bytes, migrate.NotNull()).
				Column("sealing_public_key", migrate.Bytes, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				PrimaryKey("org_id", "id").
				Unique("org_id", "environment_id", "name").Named("loomplane_db_gateways_environment_name").
				Check("id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'").Named("loomplane_db_gateways_id").
				Check("octet_length(signing_public_key)=32").Named("loomplane_db_gateways_signing_key").
				Check("octet_length(sealing_public_key)=32").Named("loomplane_db_gateways_sealing_key").
				ForeignKey("org_id", "project_id", "environment_id").
				References("loomplane_environments", "org_id", "project_id", "id").OnDelete(migrate.Cascade).
				Exec(ctx)
			if err != nil {
				return err
			}

			// A gateway names itself by id alone in its signed requests, so an
			// id is unique across organizations, and looking one up is indexed.
			err = db.NewCreateIndex("loomplane_db_gateways_id_unique").Unique().IfNotExists().
				On("loomplane_db_gateways").
				Columns("id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_db_gateway_routes").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("gateway_id", migrate.Text, migrate.NotNull()).
				Column("id", migrate.Text, migrate.NotNull()).
				Column("resource", migrate.Text, migrate.NotNull()).
				Column("salt", migrate.Bytes, migrate.NotNull()).
				Column("state", migrate.Text, migrate.NotNull(), migrate.Default("active")).
				Column("updated_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				PrimaryKey("org_id", "gateway_id", "id").
				Unique("org_id", "gateway_id", "resource").Named("loomplane_db_gateway_routes_resource").
				Check("id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'").Named("loomplane_db_gateway_routes_id").
				Check("octet_length(salt)=16").Named("loomplane_db_gateway_routes_salt").
				Check("state IN ('active','paused')").Named("loomplane_db_gateway_routes_state").
				ForeignKey("org_id", "gateway_id").
				References("loomplane_db_gateways", "org_id", "id").OnDelete(migrate.Cascade).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_db_gateway_snapshots").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("gateway_id", migrate.Text, migrate.NotNull()).
				Column("version", migrate.BigInt, migrate.NotNull()).
				Column("digest", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("now()")).
				PrimaryKey("org_id", "gateway_id").
				Check("version>0").Named("loomplane_db_gateway_snapshots_version").
				ForeignKey("org_id", "gateway_id").
				References("loomplane_db_gateways", "org_id", "id").OnDelete(migrate.Cascade).
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateTable("loomplane_db_gateway_instances").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("gateway_id", migrate.Text, migrate.NotNull()).
				Column("instance_id", migrate.Text, migrate.NotNull()).
				Column("snapshot_version", migrate.BigInt, migrate.NotNull()).
				Column("draining", migrate.Boolean, migrate.NotNull(), migrate.Default(false)).
				Column("metrics", migrate.JSON, migrate.NotNull(), migrate.Default("[]")).
				Column("last_seen_at", migrate.Timestamp, migrate.NotNull()).
				PrimaryKey("org_id", "gateway_id", "instance_id").
				Check("snapshot_version>=0").Named("loomplane_db_gateway_instances_version").
				ForeignKey("org_id", "gateway_id").
				References("loomplane_db_gateways", "org_id", "id").OnDelete(migrate.Cascade).
				Exec(ctx)
		},
		// The environment index belongs to the migrations that first needed
		// it, so this one leaves it in place.
		func(ctx context.Context, db *migrate.DB) error {
			for _, table := range []string{
				"loomplane_db_gateway_instances",
				"loomplane_db_gateway_snapshots",
				"loomplane_db_gateway_routes",
				"loomplane_db_gateways",
			} {
				if err := db.NewDropTable(table).IfExists().Exec(ctx); err != nil {
					return err
				}
			}
			return nil
		},
	)
}
