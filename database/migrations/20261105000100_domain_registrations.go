package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A registration holds its connection from approval on, unless it ended
// without a domain. Only one registration of a name can be active at once.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_domain_registrations").IfNotExists().
				Column("id", migrate.Text, migrate.PrimaryKey()).
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("connection_id", migrate.Text, migrate.NotNull()).
				Column("name", migrate.Text, migrate.NotNull()).
				Column("status", migrate.Text, migrate.NotNull()).
				Column("revision", migrate.BigInt, migrate.NotNull()).
				Column("body", migrate.JSON, migrate.NotNull()).
				Column("created_at", migrate.Timestamp, migrate.NotNull(), migrate.DefaultExpr("clock_timestamp()")).
				Exec(ctx)
			if err != nil {
				return err
			}

			err = db.NewCreateIndex("loomplane_domain_registration_active").IfNotExists().Unique().
				On("loomplane_domain_registrations").
				Columns("org_id", "name").
				Where("status IN ('approved','registering','zone_pending','succeeded')").
				Exec(ctx)
			if err != nil {
				return err
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_domain_registration_uses() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP='DELETE' THEN
		DELETE FROM loomplane_connection_uses WHERE kind='domain_registration' AND owner_id=OLD.id;
		RETURN OLD;
	END IF;

	DELETE FROM loomplane_connection_uses WHERE kind='domain_registration' AND owner_id=NEW.id;
	IF NEW.status NOT IN ('preview','expired','price_changed','failed','unconfirmed') THEN
		INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.connection_id,'domain_registration',NEW.id) ON CONFLICT DO NOTHING;
	END IF;
	RETURN NEW;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_domain_registration_uses: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_domain_registration_uses
AFTER INSERT OR UPDATE OR DELETE ON loomplane_domain_registrations
FOR EACH ROW EXECUTE FUNCTION loomplane_domain_registration_uses()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_domain_registration_uses: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_domain_registration_uses ON loomplane_domain_registrations`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_domain_registration_uses: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_domain_registration_uses()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_domain_registration_uses: %w", err)
			}

			_, err = db.Exec(ctx, `DELETE FROM loomplane_connection_uses WHERE kind='domain_registration'`)
			if err != nil {
				return fmt.Errorf("delete domain registration connection uses: %w", err)
			}

			return db.NewDropTable("loomplane_domain_registrations").IfExists().Exec(ctx)
		},
	)
}
