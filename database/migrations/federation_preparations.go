package migrations

var federationPreparationsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_federation_preparations (
 org_id TEXT NOT NULL,project_id TEXT NOT NULL,environment_id TEXT NOT NULL,id TEXT NOT NULL,
 destination_id TEXT NOT NULL,destination_generation BIGINT NOT NULL CHECK(destination_generation>0),identity_revision TEXT NOT NULL,
 intent JSONB NOT NULL,state TEXT NOT NULL CHECK(state IN ('reserved','account_pending','account_created','pod_pending','prepared')),
 generation BIGINT NOT NULL CHECK(generation>0),account_uid TEXT NOT NULL DEFAULT '',pod_uid TEXT NOT NULL DEFAULT '',
 recorded_by TEXT NOT NULL CHECK(recorded_by<>''),created_at TIMESTAMPTZ NOT NULL,updated_at TIMESTAMPTZ NOT NULL,
 CHECK((state IN ('reserved','account_pending') AND account_uid='' AND pod_uid='') OR (state IN ('account_created','pod_pending') AND account_uid<>'' AND pod_uid='') OR (state='prepared' AND account_uid<>'' AND pod_uid<>'')),
 PRIMARY KEY(org_id,id),UNIQUE(org_id,destination_id),
 FOREIGN KEY(org_id,project_id,environment_id,destination_id) REFERENCES loomplane_federation_destinations(org_id,project_id,environment_id,id) ON DELETE RESTRICT
 )`,
	`CREATE OR REPLACE FUNCTION loomplane_federation_preparation_reference_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN
 IF TG_OP='DELETE' THEN
 DELETE FROM loomplane_federation_destination_refs WHERE org_id=OLD.org_id AND destination_id=OLD.destination_id AND kind='agent' AND owner_id='preparation:'||OLD.id;
 ELSE
 INSERT INTO loomplane_federation_destination_refs(org_id,project_id,environment_id,destination_id,kind,owner_id)
 VALUES(NEW.org_id,NEW.project_id,NEW.environment_id,NEW.destination_id,'agent','preparation:'||NEW.id) ON CONFLICT DO NOTHING;
 END IF; RETURN NULL;
 END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_federation_preparation_reference AFTER INSERT OR UPDATE OR DELETE ON loomplane_federation_preparations FOR EACH ROW EXECUTE FUNCTION loomplane_federation_preparation_reference_trigger()`,
}

// Ownership records must be retired before a downgrade can remove their schema.
var federationPreparationsDown = []string{`DO $preparation_down$ BEGIN
 IF to_regclass('loomplane_federation_preparations') IS NOT NULL THEN
 LOCK TABLE loomplane_federation_preparations IN ACCESS EXCLUSIVE MODE;
 IF EXISTS(SELECT 1 FROM loomplane_federation_preparations) THEN
 RAISE EXCEPTION 'preparation ownership is still retained' USING ERRCODE='55000';
 END IF;
 DROP TABLE loomplane_federation_preparations;
 END IF;
 DROP FUNCTION IF EXISTS loomplane_federation_preparation_reference_trigger();
 END $preparation_down$`}
