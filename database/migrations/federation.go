package migrations

// Destinations retain parent resources until explicit federation cleanup. This
// prevents environment deletion from losing the credentials needed to revoke access.
var federationUp = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_environments_scope_id ON loomplane_environments(org_id,project_id,id)`,
	`CREATE TABLE IF NOT EXISTS loomplane_federation_destinations (
 id TEXT PRIMARY KEY, org_id TEXT NOT NULL, project_id TEXT NOT NULL, environment_id TEXT NOT NULL,
 name TEXT NOT NULL, connection_id TEXT NOT NULL, ownership TEXT NOT NULL DEFAULT 'imported' CHECK(ownership='imported'),
 identity JSONB NOT NULL, identity_revision TEXT NOT NULL CHECK(identity_revision<>''),
 generation BIGINT NOT NULL CHECK(generation>0), capabilities JSONB NOT NULL DEFAULT '{}',
 quarantined BOOLEAN NOT NULL DEFAULT false, created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL,
 UNIQUE(org_id,environment_id,name),
 FOREIGN KEY(org_id,project_id,environment_id) REFERENCES loomplane_environments(org_id,project_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,connection_id) REFERENCES loomplane_cloud_connections(org_id,id) ON DELETE RESTRICT
 )`,
	`CREATE INDEX IF NOT EXISTS loomplane_federation_physical ON loomplane_federation_destinations(org_id,identity_revision)`,
	`CREATE TABLE IF NOT EXISTS loomplane_federation_connection_refs (
 org_id TEXT NOT NULL, connection_id TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('cleanup','trust')), owner_id TEXT NOT NULL,
 PRIMARY KEY(kind,owner_id,connection_id),
 FOREIGN KEY(org_id,connection_id) REFERENCES loomplane_cloud_connections(org_id,id) ON DELETE RESTRICT
 )`,
	`CREATE OR REPLACE FUNCTION loomplane_federation_destination_uses_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN
 IF TG_OP='DELETE' THEN DELETE FROM loomplane_connection_uses WHERE kind='federation_destination' AND owner_id=OLD.id;
 ELSE
 DELETE FROM loomplane_connection_uses WHERE kind='federation_destination' AND owner_id=NEW.id;
 INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.connection_id,'federation_destination',NEW.id);
 END IF;
 RETURN NULL;
 END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_federation_destination_uses AFTER INSERT OR UPDATE OR DELETE ON loomplane_federation_destinations
 FOR EACH ROW EXECUTE FUNCTION loomplane_federation_destination_uses_trigger()`,
	`CREATE OR REPLACE FUNCTION loomplane_federation_reference_uses_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN
 IF TG_OP IN ('DELETE','UPDATE') THEN DELETE FROM loomplane_connection_uses WHERE kind='federation_'||OLD.kind AND owner_id=OLD.owner_id AND connection_id=OLD.connection_id; END IF;
 IF TG_OP IN ('INSERT','UPDATE') THEN INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.connection_id,'federation_'||NEW.kind,NEW.owner_id); END IF;
 RETURN NULL;
 END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_federation_reference_uses AFTER INSERT OR UPDATE OR DELETE ON loomplane_federation_connection_refs
 FOR EACH ROW EXECUTE FUNCTION loomplane_federation_reference_uses_trigger()`,
	`INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id)
 SELECT org_id,connection_id,'federation_destination',id FROM loomplane_federation_destinations ON CONFLICT DO NOTHING`,
	`INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id)
 SELECT org_id,connection_id,'federation_'||kind,owner_id FROM loomplane_federation_connection_refs ON CONFLICT DO NOTHING`,
}

var federationDown = []string{
	`DROP TABLE IF EXISTS loomplane_federation_connection_refs`,
	`DROP TABLE IF EXISTS loomplane_federation_destinations`,
	`DROP FUNCTION IF EXISTS loomplane_federation_reference_uses_trigger()`,
	`DROP FUNCTION IF EXISTS loomplane_federation_destination_uses_trigger()`,
	`DELETE FROM loomplane_connection_uses WHERE kind IN ('federation_destination','federation_cleanup','federation_trust')`,
	`DROP INDEX IF EXISTS loomplane_environments_scope_id`,
}
