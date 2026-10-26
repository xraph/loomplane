package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// A Fabriq key lock is a lease on one service's keys, so a release, a
// rotation and a revocation never interleave their calls to Fabriq. It
// holds no connection: holder names the process holding it, which renews
// expires_at while it works and deletes its own row when it is done. A
// holder that dies stops renewing, and the next writer takes the lease
// over once it expires.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewCreateTable("loomplane_fabriq_key_locks").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("resource", migrate.Text, migrate.NotNull()).
				Column("service", migrate.Text, migrate.NotNull()).
				Column("holder", migrate.Text, migrate.NotNull()).
				Column("expires_at", migrate.Timestamp, migrate.NotNull()).
				PrimaryKey("org_id", "environment_id", "resource", "service").
				Exec(ctx)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return db.NewDropTable("loomplane_fabriq_key_locks").IfExists().Exec(ctx)
		},
	)
}
