package migrations

import "github.com/xraph/grove/migrate"

// WorkloadRegistrationsMigration retains application issuance and cleanup intent.
// Unknown creation outcomes keep their parents even after a successful delete.
func WorkloadRegistrationsMigration() *migrate.Migration {
	return statements("20261025000400", "workload_registrations", []string{`DO $workloads_up$ BEGIN
 CREATE TABLE IF NOT EXISTS loomplane_workload_registrations (
 id TEXT PRIMARY KEY,org_id TEXT NOT NULL,project_id TEXT NOT NULL,environment_id TEXT NOT NULL,
 destination_id TEXT NOT NULL,destination_generation BIGINT NOT NULL,identity_revision TEXT NOT NULL,
 connection_id TEXT NOT NULL,instance_id TEXT NOT NULL,runtime_ref TEXT NOT NULL,binding_id TEXT NOT NULL,
 service_id TEXT NOT NULL,plan_id TEXT NOT NULL,image TEXT NOT NULL,entry_id TEXT NOT NULL UNIQUE,
 phase TEXT NOT NULL CHECK(phase IN ('pending','issuing','ready','revoking','draining')),
 settled BOOLEAN NOT NULL DEFAULT true,body JSONB NOT NULL,
 revoke_after TIMESTAMPTZ NOT NULL,release_after TIMESTAMPTZ,next_attempt TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 CHECK(phase<>'draining' OR (settled AND release_after IS NOT NULL)),
 FOREIGN KEY(org_id,project_id,environment_id,destination_id) REFERENCES loomplane_federation_destinations(org_id,project_id,environment_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,connection_id,instance_id,runtime_ref) REFERENCES loomplane_workload_controllers(org_id,connection_id,instance_id,runtime_ref) ON DELETE RESTRICT
 );
 CREATE INDEX IF NOT EXISTS loomplane_workload_registration_cleanup ON loomplane_workload_registrations(org_id,next_attempt);
 CREATE OR REPLACE FUNCTION loomplane_workload_registration_reference_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $reference$
 BEGIN
 IF TG_OP='DELETE' THEN
 DELETE FROM loomplane_federation_destination_refs WHERE org_id=OLD.org_id AND destination_id=OLD.destination_id AND kind='trust' AND owner_id='workload:'||OLD.id;
 ELSE
 INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
 VALUES(NEW.org_id,NEW.project_id,NEW.environment_id,NEW.destination_id,'trust','workload:'||NEW.id) ON CONFLICT DO NOTHING;
 END IF;
 RETURN NULL;
 END $reference$;
 CREATE OR REPLACE TRIGGER loomplane_workload_registration_reference AFTER INSERT OR DELETE ON loomplane_workload_registrations FOR EACH ROW EXECUTE FUNCTION loomplane_workload_registration_reference_trigger();
 INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
 SELECT org_id,project_id,environment_id,destination_id,'trust','workload:'||id FROM loomplane_workload_registrations ON CONFLICT DO NOTHING;
 END $workloads_up$`}, []string{`DO $workloads_down$ BEGIN
 IF to_regclass('loomplane_workload_registrations') IS NOT NULL THEN
 LOCK TABLE loomplane_workload_registrations IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM loomplane_workload_registrations) THEN RAISE EXCEPTION 'workload registration cleanup is incomplete' USING ERRCODE='55000'; END IF;
 DROP TABLE loomplane_workload_registrations;
 END IF;
 DROP FUNCTION IF EXISTS loomplane_workload_registration_reference_trigger();
 END $workloads_down$`})
}
