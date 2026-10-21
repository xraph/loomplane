package migrations

var federationLaunchAttemptsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_federation_launch_attempts (
 org_id TEXT NOT NULL, project_id TEXT NOT NULL, environment_id TEXT NOT NULL,
 id TEXT NOT NULL, destination_id TEXT NOT NULL, destination_generation BIGINT NOT NULL CHECK(destination_generation>0),
 identity_revision TEXT NOT NULL, enrollment_id TEXT NOT NULL, intent JSONB NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','delivered')), generation BIGINT NOT NULL CHECK(generation>0),
 config_map_uid TEXT NOT NULL DEFAULT '', recorded_by TEXT NOT NULL CHECK(recorded_by<>''),
 created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL,
 CHECK((state='pending' AND config_map_uid='') OR (state='delivered' AND config_map_uid<>'')),
 PRIMARY KEY(org_id,id), UNIQUE(org_id,enrollment_id),
 FOREIGN KEY(org_id,project_id,environment_id,destination_id)
 REFERENCES loomplane_federation_destinations(org_id,project_id,environment_id,id) ON DELETE CASCADE
 )`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_federation_launch_one_pending ON loomplane_federation_launch_attempts(org_id,destination_id) WHERE state='pending'`,
	`CREATE INDEX IF NOT EXISTS loomplane_federation_launch_pending ON loomplane_federation_launch_attempts(org_id,destination_id,state)`,
	`CREATE OR REPLACE FUNCTION loomplane_federation_launch_reference_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN
 IF TG_OP='DELETE' THEN
 DELETE FROM loomplane_federation_destination_refs WHERE org_id=OLD.org_id AND destination_id=OLD.destination_id AND kind='cleanup' AND owner_id='launch:'||OLD.id;
 ELSIF NEW.state='pending' THEN
 INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
 VALUES(NEW.org_id,NEW.project_id,NEW.environment_id,NEW.destination_id,'cleanup','launch:'||NEW.id) ON CONFLICT DO NOTHING;
 ELSE
 DELETE FROM loomplane_federation_destination_refs WHERE org_id=NEW.org_id AND destination_id=NEW.destination_id AND kind='cleanup' AND owner_id='launch:'||NEW.id;
 END IF;
 RETURN NULL;
 END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_federation_launch_reference AFTER INSERT OR UPDATE OR DELETE ON loomplane_federation_launch_attempts FOR EACH ROW EXECUTE FUNCTION loomplane_federation_launch_reference_trigger()`,
	`INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
 SELECT org_id,project_id,environment_id,destination_id,'cleanup','launch:'||id FROM loomplane_federation_launch_attempts WHERE state='pending' ON CONFLICT DO NOTHING`,
}
var federationLaunchAttemptsDown = []string{
	`DELETE FROM loomplane_federation_destination_refs WHERE kind='cleanup' AND (org_id,owner_id) IN (SELECT org_id,'launch:'||id FROM loomplane_federation_launch_attempts)`,
	`DROP TABLE IF EXISTS loomplane_federation_launch_attempts`,
	`DROP FUNCTION IF EXISTS loomplane_federation_launch_reference_trigger()`,
}
