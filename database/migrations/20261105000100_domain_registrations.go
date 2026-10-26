package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A registration holds its connection from approval on, unless it ended
// without a domain. Only one registration of a name can be active at once.
var domainRegistrationsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_domain_registrations (
 id TEXT PRIMARY KEY, org_id TEXT NOT NULL, connection_id TEXT NOT NULL, name TEXT NOT NULL, status TEXT NOT NULL,
 revision BIGINT NOT NULL, body JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp())`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_domain_registration_active ON loomplane_domain_registrations(org_id,name) WHERE status IN ('approved','registering','zone_pending','succeeded')`,
	`CREATE OR REPLACE FUNCTION loomplane_domain_registration_uses() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN DELETE FROM loomplane_connection_uses WHERE kind='domain_registration' AND owner_id=OLD.id; RETURN OLD; END IF;
 DELETE FROM loomplane_connection_uses WHERE kind='domain_registration' AND owner_id=NEW.id;
 IF NEW.status NOT IN ('preview','expired','price_changed','failed','unconfirmed') THEN
 INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.connection_id,'domain_registration',NEW.id) ON CONFLICT DO NOTHING;
 END IF; RETURN NEW; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_domain_registration_uses AFTER INSERT OR UPDATE OR DELETE ON loomplane_domain_registrations FOR EACH ROW EXECUTE FUNCTION loomplane_domain_registration_uses()`,
}

var domainRegistrationsDown = []string{
	`DROP TRIGGER IF EXISTS loomplane_domain_registration_uses ON loomplane_domain_registrations`,
	`DROP FUNCTION IF EXISTS loomplane_domain_registration_uses()`,
	`DELETE FROM loomplane_connection_uses WHERE kind='domain_registration'`,
	`DROP TABLE IF EXISTS loomplane_domain_registrations`,
}

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			for i, q := range domainRegistrationsUp {
				if _, err := db.Exec(ctx, q); err != nil {
					return fmt.Errorf("domain_registrations statement %d: %w", i+1, err)
				}
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			for i, q := range domainRegistrationsDown {
				if _, err := db.Exec(ctx, q); err != nil {
					return fmt.Errorf("domain_registrations statement %d: %w", i+1, err)
				}
			}
			return nil
		},
	)
}
