package migrations

// Keep project_id as creation provenance for old clients and saved operations.
// Ownership and foreign keys now follow org_id and the immutable Cloud ID.
var sharedCloudsUp = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_cloud_profiles_org_id ON loomplane_cloud_profiles(org_id,id)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_cloud_targets_org_cloud_id ON loomplane_cloud_targets(org_id,cloud_id,id)`,
	`CREATE TABLE IF NOT EXISTS loomplane_cloud_project_grants (
 org_id TEXT NOT NULL, cloud_id TEXT NOT NULL, project_id TEXT NOT NULL,
 target_ids TEXT[] NOT NULL DEFAULT '{}', revision BIGINT NOT NULL DEFAULT 1 CHECK(revision>0),
 revoked BOOLEAN NOT NULL DEFAULT false, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,cloud_id,project_id),
 FOREIGN KEY(org_id,cloud_id) REFERENCES loomplane_cloud_profiles(org_id,id) ON DELETE CASCADE,
 FOREIGN KEY(org_id,project_id) REFERENCES loomplane_projects(org_id,id) ON DELETE CASCADE)`,
	`INSERT INTO loomplane_cloud_project_grants(org_id,cloud_id,project_id)
 SELECT c.org_id,c.id,c.project_id FROM loomplane_cloud_profiles c JOIN loomplane_projects p ON p.org_id=c.org_id AND p.id=c.project_id ON CONFLICT DO NOTHING`,
	`DO $$ DECLARE c RECORD; BEGIN
 FOR c IN SELECT conrelid::regclass AS tbl,conname FROM pg_constraint WHERE contype='f' AND
 ((conrelid='loomplane_cloud_profiles'::regclass AND confrelid='loomplane_projects'::regclass)
 OR (conrelid IN ('loomplane_cloud_targets'::regclass,'loomplane_cloud_assignments'::regclass) AND confrelid='loomplane_cloud_profiles'::regclass AND array_length(conkey,1)=3)
 OR (conrelid='loomplane_cloud_previews'::regclass AND confrelid='loomplane_projects'::regclass)
 OR (conrelid='loomplane_environment_sync_guards'::regclass AND confrelid='loomplane_cloud_syncs'::regclass AND array_length(conkey,1)=3))
 LOOP EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I',c.tbl,c.conname); END LOOP;
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='shared_cloud_target_owner') THEN
 ALTER TABLE loomplane_cloud_targets ADD CONSTRAINT shared_cloud_target_owner FOREIGN KEY(org_id,cloud_id) REFERENCES loomplane_cloud_profiles(org_id,id) ON DELETE CASCADE;
 ALTER TABLE loomplane_cloud_assignments ADD CONSTRAINT shared_cloud_assignment_owner FOREIGN KEY(org_id,cloud_id) REFERENCES loomplane_cloud_profiles(org_id,id) ON DELETE RESTRICT;
 END IF; END $$`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_cloud_syncs_org_id ON loomplane_cloud_syncs(org_id,id)`,
	`DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='shared_cloud_sync_guard_owner') THEN
 ALTER TABLE loomplane_environment_sync_guards ADD CONSTRAINT shared_cloud_sync_guard_owner FOREIGN KEY(org_id,sync_id) REFERENCES loomplane_cloud_syncs(org_id,id) ON DELETE RESTRICT;
 END IF; END $$`,
	`DO $$ BEGIN
 ALTER TABLE loomplane_cloud_profiles DISABLE TRIGGER loomplane_cloud_profile_revision;
 UPDATE loomplane_cloud_profiles c SET name=left(c.name,20)||' ('||left(c.project_id,10)||':'||right(c.id,26)||')'
 WHERE EXISTS(SELECT 1 FROM loomplane_cloud_profiles other WHERE other.org_id=c.org_id AND other.name=c.name AND other.id<>c.id);
 ALTER TABLE loomplane_cloud_profiles ENABLE TRIGGER loomplane_cloud_profile_revision;
 END $$`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_cloud_profiles_org_name ON loomplane_cloud_profiles(org_id,name)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_cloud_targets_org_name ON loomplane_cloud_targets(org_id,cloud_id,name)`,
	`DROP INDEX IF EXISTS loomplane_cloud_sync_active_target`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_cloud_sync_active_target ON loomplane_cloud_syncs(org_id,target_id) WHERE target_id<>'' AND status NOT IN ('succeeded','reverted','reverting')`,
	`CREATE OR REPLACE FUNCTION loomplane_cloud_target_delete_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 PERFORM 1 FROM loomplane_cloud_profiles WHERE org_id=OLD.org_id AND id=OLD.cloud_id FOR UPDATE;
 IF EXISTS(SELECT 1 FROM loomplane_cloud_assignments WHERE org_id=OLD.org_id AND cloud_id=OLD.cloud_id)
 THEN RAISE EXCEPTION 'target belongs to an assigned cloud' USING ERRCODE='23503'; END IF; RETURN OLD; END $$`,
	`CREATE OR REPLACE FUNCTION loomplane_cloud_sync_delete_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$ DECLARE active BOOLEAN; BEGIN
 SELECT EXISTS(SELECT 1 FROM loomplane_cloud_syncs WHERE org_id=OLD.org_id AND status NOT IN ('succeeded','reverted')
 AND (cloud_id=OLD.id OR target_id=OLD.id OR snapshot->'profile_revisions' ? OLD.id OR snapshot->'target_revisions' ? OLD.id))
 OR EXISTS(SELECT 1 FROM loomplane_cloud_previews WHERE org_id=OLD.org_id AND created_at>now()-interval '24 hours'
 AND (body->'input'->>'cloud_id'=OLD.id OR body->'input'->>'target_id'=OLD.id OR body->'before_assignment'->>'cloud_id'=OLD.id OR snapshot->'profile_revisions' ? OLD.id OR snapshot->'target_revisions' ? OLD.id)) INTO active;
 IF active THEN RAISE EXCEPTION 'Cloud configuration has retained migration references' USING ERRCODE='23503'; END IF; RETURN OLD; END $$`,
	`CREATE OR REPLACE FUNCTION loomplane_cloud_assignment_grant_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.cloud_id IS NULL THEN RETURN NEW; END IF;
 IF TG_OP='UPDATE' AND NEW.cloud_id IS NOT DISTINCT FROM OLD.cloud_id THEN RETURN NEW; END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
 PERFORM 1 FROM loomplane_cloud_project_grants WHERE org_id=NEW.org_id AND project_id=NEW.project_id AND cloud_id=NEW.cloud_id AND NOT revoked FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'Cloud is not granted to project' USING ERRCODE='23503'; END IF;
 IF EXISTS(SELECT 1 FROM loomplane_cloud_syncs WHERE org_id=NEW.org_id AND cloud_id=NEW.cloud_id AND target_id<>'' AND status NOT IN ('succeeded','reverted')) THEN
 RAISE EXCEPTION 'Cloud target has unfinished migration work' USING ERRCODE='55P03'; END IF;
 RETURN NEW; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_cloud_assignment_grant BEFORE INSERT OR UPDATE ON loomplane_cloud_assignments FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_assignment_grant_guard()`,
	`CREATE OR REPLACE FUNCTION loomplane_cloud_sync_event_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$ DECLARE payload JSONB; project TEXT; participants JSONB; BEGIN
 FOR project IN SELECT DISTINCT coalesce(nullif(value->>'project_id',''),NEW.project_id) FROM jsonb_array_elements(coalesce(nullif(NEW.body->'environments','null'::jsonb),'[]'::jsonb)) LOOP
 SELECT coalesce(jsonb_agg(value),'[]'::jsonb) INTO participants FROM jsonb_array_elements(NEW.body->'environments') WHERE coalesce(nullif(value->>'project_id',''),NEW.project_id)=project;
 payload:=jsonb_build_object('id',NEW.id,'org_id',NEW.org_id,'project_id',project,'cloud_id',NEW.cloud_id,'target_id',NEW.target_id,'status',NEW.status,'attempt',NEW.body->'attempt','environments',participants,'reverts',NEW.body->'reverts','updated_at',NEW.body->'updated_at');
 PERFORM loomplane_append_event(NEW.org_id,project,'cloud_sync.updated',NEW.id,payload);
 END LOOP; RETURN NULL; END $$`,
	`CREATE OR REPLACE FUNCTION loomplane_cloud_grant_revision_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.revision<>OLD.revision+1 OR NEW.org_id<>OLD.org_id OR NEW.project_id<>OLD.project_id OR NEW.cloud_id<>OLD.cloud_id THEN
 RAISE EXCEPTION 'grant identity is immutable and revision must increment by one' USING ERRCODE='23514'; END IF; RETURN NEW; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_cloud_grant_revision BEFORE UPDATE ON loomplane_cloud_project_grants FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_grant_revision_trigger()`,
	`CREATE OR REPLACE FUNCTION loomplane_cloud_revision_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.revision<>OLD.revision+1 THEN RAISE EXCEPTION 'cloud revision must increment by one' USING ERRCODE='23514'; END IF;
 IF NEW.org_id<>OLD.org_id OR NEW.project_id<>OLD.project_id OR (to_jsonb(NEW)->>'id') IS DISTINCT FROM (to_jsonb(OLD)->>'id') THEN RAISE EXCEPTION 'cloud identity is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW; END $$`,
}

// Shared ownership cannot be folded back into a single project without losing grants.
var sharedCloudsDown = []string{`DO $$ BEGIN RAISE EXCEPTION 'shared Clouds require an explicit ownership migration before downgrade'; END $$`}
