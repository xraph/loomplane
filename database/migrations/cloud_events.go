package migrations

// Cloud events share the existing transactional event log. Sync storage has
// private snapshots; project only the fields a status subscriber may receive.
var cloudEventsUp = []string{
	`CREATE OR REPLACE FUNCTION loomplane_environment_cloud_event_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$ DECLARE doc JSONB; BEGIN
 IF TG_OP='DELETE' THEN doc:=to_jsonb(OLD); ELSE doc:=to_jsonb(NEW); END IF;
 PERFORM loomplane_append_event(doc->>'org_id',doc->>'project_id','environment_cloud.updated',doc->>'environment_id',doc); RETURN NULL; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_state_event AFTER INSERT OR UPDATE OR DELETE ON loomplane_cloud_assignments FOR EACH ROW EXECUTE FUNCTION loomplane_environment_cloud_event_trigger()`,
	`DO $$ DECLARE table_name TEXT; entity TEXT; BEGIN
 FOR table_name,entity IN SELECT * FROM (VALUES ('loomplane_cloud_profiles','cloud'),('loomplane_cloud_targets','cloud_target')) names(table_name,entity)
 LOOP EXECUTE format('CREATE OR REPLACE TRIGGER loomplane_state_event AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION loomplane_state_event_trigger(%L)',table_name,entity); END LOOP; END $$`,
	`CREATE OR REPLACE FUNCTION loomplane_cloud_sync_event_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$ DECLARE payload JSONB; BEGIN
 payload:=jsonb_build_object('id',NEW.id,'org_id',NEW.org_id,'project_id',NEW.project_id,'cloud_id',NEW.cloud_id,'target_id',NEW.target_id,'status',NEW.status,'attempt',NEW.body->'attempt','environments',NEW.body->'environments','reverts',NEW.body->'reverts','updated_at',NEW.body->'updated_at');
 PERFORM loomplane_append_event(NEW.org_id,NEW.project_id,'cloud_sync.updated',NEW.id,payload); RETURN NULL; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_cloud_sync_event AFTER INSERT OR UPDATE ON loomplane_cloud_syncs FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_sync_event_trigger()`,
}
var cloudEventsDown = []string{
	`DROP TRIGGER IF EXISTS loomplane_cloud_sync_event ON loomplane_cloud_syncs`,
	`DROP FUNCTION IF EXISTS loomplane_cloud_sync_event_trigger()`,
	`DROP TRIGGER IF EXISTS loomplane_state_event ON loomplane_cloud_assignments`,
	`DROP FUNCTION IF EXISTS loomplane_environment_cloud_event_trigger()`,
	`DROP TRIGGER IF EXISTS loomplane_state_event ON loomplane_cloud_targets`,
	`DROP TRIGGER IF EXISTS loomplane_state_event ON loomplane_cloud_profiles`,
}
