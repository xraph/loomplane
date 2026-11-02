package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// Retain both assignments during recovery, even after the original review expires.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_cloud_sync_delete_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
	active BOOLEAN;
BEGIN
	SELECT EXISTS(SELECT 1 FROM loomplane_cloud_syncs WHERE org_id=OLD.org_id AND project_id=OLD.project_id AND status NOT IN ('succeeded','reverted')
			AND (cloud_id=OLD.id OR target_id=OLD.id OR snapshot->'profile_revisions' ? OLD.id OR snapshot->'target_revisions' ? OLD.id))
		OR EXISTS(SELECT 1 FROM loomplane_cloud_previews WHERE org_id=OLD.org_id AND project_id=OLD.project_id AND created_at>now()-interval '24 hours'
			AND (body->'input'->>'cloud_id'=OLD.id OR body->'input'->>'target_id'=OLD.id OR body->'before_assignment'->>'cloud_id'=OLD.id OR snapshot->'profile_revisions' ? OLD.id OR snapshot->'target_revisions' ? OLD.id))
		INTO active;
	IF active THEN
		RAISE EXCEPTION 'Cloud configuration has retained migration references' USING ERRCODE='23503';
	END IF;

	RETURN OLD;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_cloud_sync_delete_guard: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, cloudSyncDeleteGuardSQL)
			if err != nil {
				return fmt.Errorf("create function loomplane_cloud_sync_delete_guard: %w", err)
			}
			return nil
		},
	)
}
