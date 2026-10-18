package migrations

var federationEnrollmentUp = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_federation_destination_org_id ON loomplane_federation_destinations(org_id,id)`,
	`CREATE TABLE IF NOT EXISTS loomplane_federation_enrollment_epochs (
 org_id TEXT NOT NULL, destination_id TEXT NOT NULL, epoch BIGINT NOT NULL CHECK(epoch>0),
 PRIMARY KEY(org_id,destination_id),
 FOREIGN KEY(org_id,destination_id) REFERENCES loomplane_federation_destinations(org_id,id) ON DELETE CASCADE
 )`,
	`CREATE TABLE IF NOT EXISTS loomplane_federation_enrollments (
 id TEXT PRIMARY KEY, org_id TEXT NOT NULL, project_id TEXT NOT NULL, environment_id TEXT NOT NULL,
 destination_id TEXT NOT NULL, destination_generation BIGINT NOT NULL CHECK(destination_generation>0), identity_revision TEXT NOT NULL,
 epoch BIGINT NOT NULL CHECK(epoch>0), agent_id TEXT NOT NULL, entry_id TEXT NOT NULL,
 phase TEXT NOT NULL CHECK(phase IN ('pending','ready','consumed','revoking','revoked')),
 body JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL, expires_at TIMESTAMPTZ NOT NULL,
 consumed_at TIMESTAMPTZ, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CHECK(expires_at>created_at AND expires_at<=created_at+interval '5 minutes'),
 UNIQUE(org_id,destination_id,epoch), UNIQUE(entry_id), UNIQUE(org_id,agent_id),
 FOREIGN KEY(org_id,project_id,environment_id,destination_id) REFERENCES loomplane_federation_destinations(org_id,project_id,environment_id,id) ON DELETE RESTRICT
 )`,
	`CREATE INDEX IF NOT EXISTS loomplane_federation_enrollment_cleanup ON loomplane_federation_enrollments(org_id,phase,expires_at)`,
	`CREATE OR REPLACE FUNCTION loomplane_federation_enrollment_reference_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN
 IF TG_OP='DELETE' THEN
 DELETE FROM loomplane_federation_destination_refs WHERE org_id=OLD.org_id AND destination_id=OLD.destination_id AND kind='trust' AND owner_id=OLD.id;
 ELSIF NEW.phase='revoked' THEN
 DELETE FROM loomplane_federation_destination_refs WHERE org_id=NEW.org_id AND destination_id=NEW.destination_id AND kind='trust' AND owner_id=NEW.id;
 ELSE
 INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
 VALUES(NEW.org_id,NEW.project_id,NEW.environment_id,NEW.destination_id,'trust',NEW.id) ON CONFLICT DO NOTHING;
 END IF;
 RETURN NULL;
 END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_federation_enrollment_reference AFTER INSERT OR UPDATE OR DELETE ON loomplane_federation_enrollments FOR EACH ROW EXECUTE FUNCTION loomplane_federation_enrollment_reference_trigger()`,
	`INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
 SELECT org_id,project_id,environment_id,destination_id,'trust',id FROM loomplane_federation_enrollments WHERE phase<>'revoked' ON CONFLICT DO NOTHING`,
}
var federationEnrollmentDown = []string{
	`DELETE FROM loomplane_federation_destination_refs WHERE kind='trust' AND (org_id,owner_id) IN (SELECT org_id,id FROM loomplane_federation_enrollments)`,
	`DROP TABLE IF EXISTS loomplane_federation_enrollments`,
	`DROP TABLE IF EXISTS loomplane_federation_enrollment_epochs`,
	`DROP FUNCTION IF EXISTS loomplane_federation_enrollment_reference_trigger()`,
}
