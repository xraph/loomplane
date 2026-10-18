package migrations

var federationCatalogUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_federation_observers (
 org_id TEXT NOT NULL, project_id TEXT NOT NULL, environment_id TEXT NOT NULL,
 agent_id TEXT NOT NULL, destination_id TEXT NOT NULL, destination_generation BIGINT NOT NULL CHECK(destination_generation>0),
 active BOOLEAN NOT NULL DEFAULT true, state JSONB NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,agent_id),
 FOREIGN KEY(org_id,project_id,environment_id,destination_id) REFERENCES loomplane_federation_destinations(org_id,project_id,environment_id,id) ON DELETE RESTRICT
 )`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_federation_active_observer ON loomplane_federation_observers(org_id,destination_id) WHERE active`,
	`CREATE OR REPLACE FUNCTION loomplane_federation_observer_reference_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN
 IF TG_OP='INSERT' THEN
 INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
 VALUES(NEW.org_id,NEW.project_id,NEW.environment_id,NEW.destination_id,'agent',NEW.agent_id);
 ELSIF TG_OP='DELETE' THEN
 DELETE FROM loomplane_federation_destination_refs WHERE org_id=OLD.org_id AND destination_id=OLD.destination_id AND kind='agent' AND owner_id=OLD.agent_id;
 END IF;
 RETURN NULL;
 END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_federation_observer_reference AFTER INSERT OR DELETE ON loomplane_federation_observers FOR EACH ROW EXECUTE FUNCTION loomplane_federation_observer_reference_trigger()`,
	`INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
 SELECT org_id,project_id,environment_id,destination_id,'agent',agent_id FROM loomplane_federation_observers ON CONFLICT DO NOTHING`,
	`CREATE TABLE IF NOT EXISTS loomplane_federation_catalog_views (
 id TEXT PRIMARY KEY, org_id TEXT NOT NULL, agent_id TEXT NOT NULL, body JSONB NOT NULL,
 issued_at TIMESTAMPTZ NOT NULL, expires_at TIMESTAMPTZ NOT NULL CHECK(expires_at>issued_at),
 FOREIGN KEY(org_id,agent_id) REFERENCES loomplane_federation_observers(org_id,agent_id) ON DELETE CASCADE
 )`,
	`CREATE INDEX IF NOT EXISTS loomplane_federation_catalog_view_expiry ON loomplane_federation_catalog_views(org_id,agent_id,expires_at)`,
}
var federationCatalogDown = []string{
	`DROP TABLE IF EXISTS loomplane_federation_catalog_views`,
	`DELETE FROM loomplane_federation_destination_refs WHERE kind='agent' AND (org_id,owner_id) IN (SELECT org_id,agent_id FROM loomplane_federation_observers)`,
	`DROP TABLE IF EXISTS loomplane_federation_observers`,
	`DROP FUNCTION IF EXISTS loomplane_federation_observer_reference_trigger()`,
}
