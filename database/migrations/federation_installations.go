package migrations

var federationInstallationsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_federation_installations (
 org_id TEXT NOT NULL, project_id TEXT NOT NULL, environment_id TEXT NOT NULL, destination_id TEXT NOT NULL,
 generation BIGINT NOT NULL CHECK(generation>0), profile JSONB NOT NULL,
 recorded_by TEXT NOT NULL CHECK(recorded_by<>''), checked_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY(org_id,destination_id),
 FOREIGN KEY(org_id,project_id,environment_id,destination_id)
 REFERENCES loomplane_federation_destinations(org_id,project_id,environment_id,id) ON DELETE RESTRICT
 )`,
	`CREATE OR REPLACE FUNCTION loomplane_federation_installation_reference_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN
 IF TG_OP='DELETE' THEN
 DELETE FROM loomplane_federation_destination_refs WHERE org_id=OLD.org_id AND destination_id=OLD.destination_id AND kind='agent' AND owner_id='installation:'||OLD.destination_id;
 ELSE
 INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
 VALUES(NEW.org_id,NEW.project_id,NEW.environment_id,NEW.destination_id,'agent','installation:'||NEW.destination_id) ON CONFLICT DO NOTHING;
 END IF;
 RETURN NULL;
 END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_federation_installation_reference AFTER INSERT OR UPDATE OR DELETE ON loomplane_federation_installations FOR EACH ROW EXECUTE FUNCTION loomplane_federation_installation_reference_trigger()`,
	`INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
 SELECT org_id,project_id,environment_id,destination_id,'agent','installation:'||destination_id FROM loomplane_federation_installations ON CONFLICT DO NOTHING`,
}
var federationInstallationsDown = []string{
	`DELETE FROM loomplane_federation_destination_refs WHERE kind='agent' AND owner_id='installation:'||destination_id AND (org_id,destination_id) IN (SELECT org_id,destination_id FROM loomplane_federation_installations)`,
	`DROP TABLE IF EXISTS loomplane_federation_installations`,
	`DROP FUNCTION IF EXISTS loomplane_federation_installation_reference_trigger()`,
}
