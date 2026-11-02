package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// Cloud events share the existing transactional event log. Sync storage has
// private snapshots; project only the fields a status subscriber may receive.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_environment_cloud_event_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
	doc JSONB;
BEGIN
	IF TG_OP='DELETE' THEN doc:=to_jsonb(OLD); ELSE doc:=to_jsonb(NEW); END IF;

	PERFORM loomplane_append_event(doc->>'org_id',doc->>'project_id','environment_cloud.updated',doc->>'environment_id',doc);
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_environment_cloud_event_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_state_event
AFTER INSERT OR UPDATE OR DELETE ON loomplane_cloud_assignments
FOR EACH ROW EXECUTE FUNCTION loomplane_environment_cloud_event_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_state_event on loomplane_cloud_assignments: %w", err)
			}

			_, err = db.Exec(ctx, `DO $$
DECLARE
	table_name TEXT;
	entity TEXT;
BEGIN
	FOR table_name,entity IN SELECT * FROM (VALUES ('loomplane_cloud_profiles','cloud'),('loomplane_cloud_targets','cloud_target')) names(table_name,entity)
	LOOP
		EXECUTE format('CREATE OR REPLACE TRIGGER loomplane_state_event AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION loomplane_state_event_trigger(%L)',table_name,entity);
	END LOOP;
END $$`)
			if err != nil {
				return fmt.Errorf("create loomplane_state_event triggers on clouds and targets: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_cloud_sync_event_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
	payload JSONB;
BEGIN
	payload:=jsonb_build_object('id',NEW.id,'org_id',NEW.org_id,'project_id',NEW.project_id,'cloud_id',NEW.cloud_id,'target_id',NEW.target_id,'status',NEW.status,'attempt',NEW.body->'attempt','environments',NEW.body->'environments','reverts',NEW.body->'reverts','updated_at',NEW.body->'updated_at');

	PERFORM loomplane_append_event(NEW.org_id,NEW.project_id,'cloud_sync.updated',NEW.id,payload);
	RETURN NULL;
END $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_cloud_sync_event_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE TRIGGER loomplane_cloud_sync_event
AFTER INSERT OR UPDATE ON loomplane_cloud_syncs
FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_sync_event_trigger()`)
			if err != nil {
				return fmt.Errorf("create trigger loomplane_cloud_sync_event: %w", err)
			}
			return nil
		},
		func(ctx context.Context, db *migrate.DB) error {
			_, err := db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_cloud_sync_event ON loomplane_cloud_syncs`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_cloud_sync_event: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_cloud_sync_event_trigger()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_cloud_sync_event_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_state_event ON loomplane_cloud_assignments`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_state_event on loomplane_cloud_assignments: %w", err)
			}

			_, err = db.Exec(ctx, `DROP FUNCTION IF EXISTS loomplane_environment_cloud_event_trigger()`)
			if err != nil {
				return fmt.Errorf("drop function loomplane_environment_cloud_event_trigger: %w", err)
			}

			_, err = db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_state_event ON loomplane_cloud_targets`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_state_event on loomplane_cloud_targets: %w", err)
			}

			_, err = db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_state_event ON loomplane_cloud_profiles`)
			if err != nil {
				return fmt.Errorf("drop trigger loomplane_state_event on loomplane_cloud_profiles: %w", err)
			}
			return nil
		},
	)
}
