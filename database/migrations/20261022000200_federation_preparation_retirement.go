package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, atomicFederationDDL(
				`ALTER TABLE loomplane_federation_preparations ADD COLUMN IF NOT EXISTS cleanup_from TEXT NOT NULL DEFAULT ''`,
				`ALTER TABLE loomplane_federation_preparations
	DROP CONSTRAINT IF EXISTS loomplane_federation_preparations_state_check,
	DROP CONSTRAINT IF EXISTS loomplane_federation_preparations_check,
	DROP CONSTRAINT IF EXISTS federation_preparation_cleanup_check,
	DROP CONSTRAINT IF EXISTS loomplane_federation_preparations_org_id_destination_id_key`,
				`ALTER TABLE loomplane_federation_preparations
	ADD CONSTRAINT loomplane_federation_preparations_state_check
		CHECK(state IN ('reserved','account_pending','account_created','pod_pending','prepared','retiring','retired')),
	ADD CONSTRAINT federation_preparation_cleanup_check CHECK(
		(cleanup_from='' AND (
			(state IN ('reserved','account_pending') AND account_uid='' AND pod_uid='') OR
			(state IN ('account_created','pod_pending') AND account_uid<>'' AND pod_uid='') OR
			(state='prepared' AND account_uid<>'' AND pod_uid<>''))) OR
		(state IN ('retiring','retired') AND (
			(cleanup_from='reserved' AND account_uid='' AND pod_uid='') OR
			(cleanup_from='account_pending' AND pod_uid='') OR
			(cleanup_from='account_created' AND account_uid<>'' AND pod_uid='') OR
			(cleanup_from='pod_pending' AND account_uid<>'') OR
			(cleanup_from='prepared' AND account_uid<>'' AND pod_uid<>'')))
	)`,
				`CREATE UNIQUE INDEX IF NOT EXISTS federation_preparation_one_active
	ON loomplane_federation_preparations(org_id,destination_id)
	WHERE state<>'retired'`,
				preparationDestinationFK("CASCADE"),
				`CREATE OR REPLACE FUNCTION loomplane_federation_preparation_reference_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP='DELETE' THEN
		DELETE FROM loomplane_federation_destination_refs
		WHERE org_id=OLD.org_id AND destination_id=OLD.destination_id AND kind='agent' AND owner_id='preparation:'||OLD.id;
	ELSIF NEW.state='retired' THEN
		DELETE FROM loomplane_federation_destination_refs
		WHERE org_id=NEW.org_id AND destination_id=NEW.destination_id AND kind='agent' AND owner_id='preparation:'||NEW.id;
	ELSE
		INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
		VALUES(NEW.org_id,NEW.project_id,NEW.environment_id,NEW.destination_id,'agent','preparation:'||NEW.id)
		ON CONFLICT DO NOTHING;
	END IF;
	RETURN NULL;
END $$`,
				`INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
	SELECT org_id,project_id,environment_id,destination_id,'agent','preparation:'||id
	FROM loomplane_federation_preparations
	WHERE state<>'retired'
	ON CONFLICT DO NOTHING`,
			))
			if err != nil {
				return fmt.Errorf("retire federation preparation guards: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, atomicFederationDDL(
				`LOCK TABLE loomplane_federation_preparations IN ACCESS EXCLUSIVE MODE`,
				`IF EXISTS(SELECT 1 FROM loomplane_federation_preparations WHERE state IN ('retiring','retired')) THEN
	RAISE EXCEPTION 'preparation retirement history is still retained' USING ERRCODE='55000';
END IF`,
				`ALTER TABLE loomplane_federation_preparations
	DROP CONSTRAINT IF EXISTS loomplane_federation_preparations_state_check,
	DROP CONSTRAINT IF EXISTS federation_preparation_cleanup_check`,
				`DROP INDEX IF EXISTS federation_preparation_one_active`,
				`ALTER TABLE loomplane_federation_preparations
	DROP COLUMN IF EXISTS cleanup_from,
	ADD CONSTRAINT loomplane_federation_preparations_state_check
		CHECK(state IN ('reserved','account_pending','account_created','pod_pending','prepared')),
	ADD CONSTRAINT loomplane_federation_preparations_check CHECK(
		(state IN ('reserved','account_pending') AND account_uid='' AND pod_uid='') OR
		(state IN ('account_created','pod_pending') AND account_uid<>'' AND pod_uid='') OR
		(state='prepared' AND account_uid<>'' AND pod_uid<>'')),
	ADD CONSTRAINT loomplane_federation_preparations_org_id_destination_id_key UNIQUE(org_id,destination_id)`,
				preparationDestinationFK("RESTRICT"),
			))
			if err != nil {
				return fmt.Errorf("restore federation preparation guards: %w", err)
			}
			return nil
		},
	)
}

// PostgreSQL may truncate the generated composite foreign-key name. Discover it
// by relation identity so upgrades also work with an existing installation.
func preparationDestinationFK(action string) string {
	return `DECLARE
	fk RECORD;
BEGIN
	FOR fk IN
		SELECT conname FROM pg_constraint
		WHERE conrelid='loomplane_federation_preparations'::regclass
			AND confrelid='loomplane_federation_destinations'::regclass
			AND contype='f'
	LOOP
		EXECUTE format('ALTER TABLE loomplane_federation_preparations DROP CONSTRAINT %I',fk.conname);
	END LOOP;

	ALTER TABLE loomplane_federation_preparations
		ADD CONSTRAINT federation_preparation_destination_fk
		FOREIGN KEY(org_id,project_id,environment_id,destination_id)
		REFERENCES loomplane_federation_destinations(org_id,project_id,environment_id,id)
		ON DELETE ` + action + `;
END`
}
