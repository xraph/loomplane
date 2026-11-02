package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// Keyless connections. loomplane_oidc_keys holds the public half of each
// key Loomplane signs its own tokens with; the private halves are only in
// Vault. A retired key stays a day so tokens it signed still verify. A
// reservation holds a connection ID for a day while you set up the
// cloud's trust, since the trust names the ID before the connection
// exists. And every webhook endpoint that hears connection.sign_in_needed
// also hears connection.keyless_refused. As with the sign-in backfill,
// Relay's tables come after this group on a new database, so a missing
// table is skipped, and running it again changes nothing.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_oidc_keys").IfNotExists().
				Column("kid", migrate.Text, migrate.PrimaryKey()).
				Column("public_jwk", migrate.JSON, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("retired_at", migrate.Timestamp).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateTable("loomplane_keyless_reservations").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("user_id", migrate.Text, migrate.NotNull()).
				Column("cloud", migrate.Text, migrate.NotNull()).
				Column("expires_at", migrate.Timestamp, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_keyless_reservations_expires_at").IfNotExists().
				On("loomplane_keyless_reservations").
				Columns("expires_at").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `DO $$
BEGIN
	IF to_regclass('relay_endpoints') IS NOT NULL THEN
		UPDATE relay_endpoints
		   SET event_types = array_append(event_types, 'connection.keyless_refused'), updated_at = clock_timestamp()
		 WHERE 'connection.sign_in_needed' = ANY(event_types)
		   AND NOT ('connection.keyless_refused' = ANY(event_types));
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("add connection.keyless_refused to relay_endpoints: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $$
BEGIN
	IF to_regclass('relay_endpoints') IS NOT NULL THEN
		UPDATE relay_endpoints
		   SET event_types = array_remove(event_types, 'connection.keyless_refused'), updated_at = clock_timestamp()
		 WHERE 'connection.keyless_refused' = ANY(event_types);
	END IF;
END $$`)
			if err != nil {
				return fmt.Errorf("remove connection.keyless_refused from relay_endpoints: %w", err)
			}

			err = db.NewDropIndex("loomplane_keyless_reservations_expires_at").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewDropTable("loomplane_keyless_reservations").IfExists().Exec(ctx)
			if err != nil {
				return err
			}

			return db.NewDropTable("loomplane_oidc_keys").IfExists().Exec(ctx)
		},
	)
}
