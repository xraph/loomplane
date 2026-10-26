package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// The Fabriq API keys Loomplane issues, one per service that needs a
// hosted Fabriq. id is Fabriq's key id; the plaintext is in Vault, never
// here. A service has at most one active key (the one its running release
// holds) and one pending key (a rotation waiting for its next release);
// retiring keys stay valid in Fabriq until that release has written the
// service's binding.
//
// A rebuild re-creates a FalkorDB or Elasticsearch projection from
// Postgres. A resource has at most one queued or running rebuild of each
// projection; attempt names the claim running it.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_fabriq_keys").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("resource", migrate.Text, migrate.NotNull()).
				Column("service", migrate.Text, migrate.NotNull()).
				Column("tenant", migrate.Text, migrate.NotNull()).
				Column("prefix", migrate.Text, migrate.NotNull()).
				Column("state", migrate.Text, migrate.NotNull(),
					migrate.Check("state IN ('pending','active','retiring','revoked')")).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				Column("revoked_at", migrate.Timestamp).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_fabriq_keys_active").IfNotExists().
				On("loomplane_fabriq_keys").
				Columns("org_id", "environment_id", "resource", "service").
				Where("state = 'active'").
				Unique().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_fabriq_keys_pending").IfNotExists().
				On("loomplane_fabriq_keys").
				Columns("org_id", "environment_id", "resource", "service").
				Where("state = 'pending'").
				Unique().
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_fabriq_rebuilds").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("resource", migrate.Text, migrate.NotNull()).
				Column("projection", migrate.Text, migrate.NotNull(),
					migrate.Check("projection IN ('graph','search')")).
				Column("request_id", migrate.Text, migrate.NotNull()).
				Column("status", migrate.Text, migrate.NotNull(),
					migrate.Check("status IN ('queued','running','succeeded','failed')")).
				Column("attempt", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("output", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("created_by", migrate.Text, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("finished_at", migrate.Timestamp).
				Unique("org_id", "request_id").
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_fabriq_rebuilds_active").IfNotExists().
				On("loomplane_fabriq_rebuilds").
				Columns("org_id", "environment_id", "resource", "projection").
				Where("status IN ('queued','running')").
				Unique().
				Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewCreateIndex("loomplane_fabriq_rebuilds_resource").IfNotExists().
				On("loomplane_fabriq_rebuilds").
				Columns("org_id", "environment_id", "resource").
				ColumnDesc("created_at").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			if err := db.NewDropTable("loomplane_fabriq_rebuilds").IfExists().Exec(ctx); err != nil {
				return err
			}
			return db.NewDropTable("loomplane_fabriq_keys").IfExists().Exec(ctx)
		},
	)
}
