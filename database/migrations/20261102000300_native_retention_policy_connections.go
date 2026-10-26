package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_native_policy_connection_uses() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
	source_connection TEXT;
BEGIN
	IF TG_OP='DELETE' THEN
		DELETE FROM loomplane_connection_uses WHERE kind='native_retention_policy' AND owner_id=OLD.id;
	ELSE
		DELETE FROM loomplane_connection_uses WHERE kind='native_retention_policy' AND owner_id=NEW.id;
		IF NEW.enabled THEN
			SELECT connection_id INTO source_connection FROM loomplane_native_sources WHERE org_id=NEW.org_id AND id=NEW.source_id;
			INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id)
			VALUES(NEW.org_id,source_connection,'native_retention_policy',NEW.id);
		END IF;
	END IF;
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_native_policy_connection_uses: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_native_policy_connection_uses
	AFTER INSERT OR UPDATE OR DELETE ON loomplane_native_retention_policies
	FOR EACH ROW EXECUTE FUNCTION loomplane_native_policy_connection_uses()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_native_policy_connection_uses: %w", err)
			}

			_, err = db.Exec(ctx, `INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id)
SELECT p.org_id,s.connection_id,'native_retention_policy',p.id
FROM loomplane_native_retention_policies p
JOIN loomplane_native_sources s ON s.org_id=p.org_id AND s.id=p.source_id
JOIN loomplane_cloud_connections c ON c.org_id=s.org_id AND c.id=s.connection_id
WHERE p.enabled
ON CONFLICT DO NOTHING`)
			if err != nil {
				return fmt.Errorf("backfill native retention policy connection uses: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DO $native_policy_connections_down$
BEGIN
	IF to_regclass('loomplane_native_retention_policies') IS NOT NULL THEN
		LOCK TABLE loomplane_native_retention_policies IN ACCESS EXCLUSIVE MODE;
		IF EXISTS(SELECT 1 FROM loomplane_native_retention_policies WHERE enabled) THEN
			RAISE EXCEPTION 'enabled native retention policies retain their source accounts' USING ERRCODE='55000';
		END IF;

		DROP TRIGGER IF EXISTS loomplane_native_policy_connection_uses ON loomplane_native_retention_policies;
	END IF;

	DELETE FROM loomplane_connection_uses WHERE kind='native_retention_policy';
	DROP FUNCTION IF EXISTS loomplane_native_policy_connection_uses();
END $native_policy_connections_down$`)
			if err != nil {
				return fmt.Errorf("drop native retention policy connection uses: %w", err)
			}
			return nil
		},
	)
}
