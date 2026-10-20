package migrations

var federationAccessUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_federation_policy_versions (
 org_id TEXT PRIMARY KEY, generation BIGINT NOT NULL DEFAULT 1 CHECK(generation>0))`,
	`CREATE TABLE IF NOT EXISTS loomplane_federation_exports (
 org_id TEXT NOT NULL, project_id TEXT NOT NULL, environment_id TEXT NOT NULL, id TEXT NOT NULL,
 generation BIGINT NOT NULL CHECK(generation>0), state JSONB NOT NULL,
 PRIMARY KEY(org_id,id), UNIQUE(org_id,project_id,environment_id,id),
 FOREIGN KEY(org_id,project_id,environment_id) REFERENCES loomplane_environments(org_id,project_id,id) ON DELETE RESTRICT)`,
	`CREATE TABLE IF NOT EXISTS loomplane_federation_export_destinations (
 org_id TEXT NOT NULL, project_id TEXT NOT NULL, environment_id TEXT NOT NULL, export_id TEXT NOT NULL, destination_id TEXT NOT NULL,
 PRIMARY KEY(org_id,export_id,destination_id),
 FOREIGN KEY(org_id,project_id,environment_id,export_id) REFERENCES loomplane_federation_exports(org_id,project_id,environment_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,project_id,environment_id,destination_id) REFERENCES loomplane_federation_destinations(org_id,project_id,environment_id,id) ON DELETE RESTRICT)`,
	`CREATE TABLE IF NOT EXISTS loomplane_federation_grants (
 org_id TEXT NOT NULL, project_id TEXT NOT NULL, environment_id TEXT NOT NULL, id TEXT NOT NULL, export_id TEXT NOT NULL,
 source_project_id TEXT NOT NULL, source_environment_id TEXT NOT NULL, source_destination_id TEXT NOT NULL,
 generation BIGINT NOT NULL CHECK(generation>0), state JSONB NOT NULL,
 PRIMARY KEY(org_id,id),
 FOREIGN KEY(org_id,project_id,environment_id,export_id) REFERENCES loomplane_federation_exports(org_id,project_id,environment_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,source_project_id,source_environment_id,source_destination_id) REFERENCES loomplane_federation_destinations(org_id,project_id,environment_id,id) ON DELETE RESTRICT)`,
	`CREATE TABLE IF NOT EXISTS loomplane_federation_grant_sources (
 org_id TEXT NOT NULL, grant_id TEXT NOT NULL, project_id TEXT NOT NULL, environment_id TEXT NOT NULL, destination_id TEXT NOT NULL,
 PRIMARY KEY(org_id,grant_id,project_id,environment_id,destination_id),
 FOREIGN KEY(org_id,grant_id) REFERENCES loomplane_federation_grants(org_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,project_id,environment_id,destination_id) REFERENCES loomplane_federation_destinations(org_id,project_id,environment_id,id) ON DELETE RESTRICT)`,
	`CREATE INDEX IF NOT EXISTS loomplane_federation_grants_source ON loomplane_federation_grants(org_id,source_project_id,source_environment_id)`,
	`CREATE TABLE IF NOT EXISTS loomplane_federation_policy_history (
 org_id TEXT NOT NULL, generation BIGINT NOT NULL, kind TEXT NOT NULL, entity_id TEXT NOT NULL, actor_id TEXT NOT NULL,
 state JSONB NOT NULL, occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(org_id,generation))`,
	`CREATE OR REPLACE FUNCTION loomplane_federation_policy_change(org TEXT,kind TEXT,entity TEXT,actor TEXT,body JSONB) RETURNS BIGINT LANGUAGE plpgsql AS $$
 DECLARE revision BIGINT;
 BEGIN
 UPDATE loomplane_federation_policy_versions SET generation=generation+1 WHERE org_id=org RETURNING generation INTO revision;
 IF revision IS NULL THEN RAISE EXCEPTION 'policy version is missing'; END IF;
 INSERT INTO loomplane_federation_policy_history(org_id,generation,kind,entity_id,actor_id,state) VALUES(org,revision,kind,entity,actor,body);
 PERFORM loomplane_append_event(org,'','federation.policy.changed',entity,jsonb_build_object('generation',revision));
 RETURN revision;
 END $$`,
	`CREATE OR REPLACE FUNCTION loomplane_federation_quarantine_policy() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN
 INSERT INTO loomplane_federation_policy_versions(org_id) VALUES(NEW.org_id) ON CONFLICT DO NOTHING;
 PERFORM loomplane_federation_policy_change(NEW.org_id,'destination.quarantine',NEW.id,'system',jsonb_build_object('destination_id',NEW.id,'quarantined',NEW.quarantined));
 RETURN NULL;
 END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_federation_quarantine_policy AFTER UPDATE OF quarantined ON loomplane_federation_destinations
 FOR EACH ROW WHEN (OLD.quarantined IS DISTINCT FROM NEW.quarantined) EXECUTE FUNCTION loomplane_federation_quarantine_policy()`,
}
var federationAccessDown = []string{
	`DROP TRIGGER IF EXISTS loomplane_federation_quarantine_policy ON loomplane_federation_destinations`,
	`DROP FUNCTION IF EXISTS loomplane_federation_quarantine_policy()`,
	`DROP FUNCTION IF EXISTS loomplane_federation_policy_change(TEXT,TEXT,TEXT,TEXT,JSONB)`,
	`DROP TABLE IF EXISTS loomplane_federation_grant_sources`,
	`DROP TABLE IF EXISTS loomplane_federation_grants`,
	`DROP TABLE IF EXISTS loomplane_federation_export_destinations`,
	`DROP TABLE IF EXISTS loomplane_federation_exports`,
	`DROP TABLE IF EXISTS loomplane_federation_policy_history`,
	`DROP TABLE IF EXISTS loomplane_federation_policy_versions`,
}
