package migrations

var cloudSyncUp = append([]string{
	`ALTER TABLE loomplane_cloud_assignments ALTER COLUMN cloud_id DROP NOT NULL`,
	`CREATE TABLE IF NOT EXISTS loomplane_applied_variables (
 org_id TEXT NOT NULL, deployment_id TEXT NOT NULL REFERENCES loomplane_deployments(id) ON DELETE CASCADE,
 action_id TEXT NOT NULL, secret_ref TEXT NOT NULL, PRIMARY KEY(org_id,deployment_id,action_id))`,
	`CREATE TABLE IF NOT EXISTS loomplane_cloud_previews (
 id TEXT PRIMARY KEY, org_id TEXT NOT NULL, project_id TEXT NOT NULL, revision TEXT NOT NULL,
 body JSONB NOT NULL, snapshot JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL,
 UNIQUE(org_id,project_id,id),
 FOREIGN KEY(org_id,project_id) REFERENCES loomplane_projects(org_id,id) ON DELETE CASCADE)`,
	`CREATE TABLE IF NOT EXISTS loomplane_cloud_preview_connections (
 org_id TEXT NOT NULL,preview_id TEXT NOT NULL REFERENCES loomplane_cloud_previews(id) ON DELETE CASCADE,
 connection_id TEXT NOT NULL, PRIMARY KEY(preview_id,connection_id),
 FOREIGN KEY(org_id,connection_id) REFERENCES loomplane_cloud_connections(org_id,id) ON DELETE RESTRICT)`,

	`CREATE TABLE IF NOT EXISTS loomplane_cloud_syncs (
 id TEXT PRIMARY KEY, org_id TEXT NOT NULL, project_id TEXT NOT NULL,
 preview_id TEXT NOT NULL, idempotency_key TEXT NOT NULL, status TEXT NOT NULL,
 target_id TEXT NOT NULL DEFAULT '', cloud_id TEXT NOT NULL DEFAULT '',
 body JSONB NOT NULL, snapshot JSONB NOT NULL, version BIGINT NOT NULL DEFAULT 1,
 created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL,
 UNIQUE(org_id,project_id,id), UNIQUE(org_id,project_id,idempotency_key), UNIQUE(preview_id),
 FOREIGN KEY(org_id,project_id,preview_id) REFERENCES loomplane_cloud_previews(org_id,project_id,id) ON DELETE RESTRICT)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_cloud_sync_active_target ON loomplane_cloud_syncs(org_id,project_id,target_id)
 WHERE target_id<>'' AND status NOT IN ('succeeded','reverted','reverting')`,
	`CREATE TABLE IF NOT EXISTS loomplane_environment_sync_guards (
 org_id TEXT NOT NULL, project_id TEXT NOT NULL, environment_id TEXT NOT NULL, sync_id TEXT NOT NULL,
 PRIMARY KEY(org_id,environment_id),
 FOREIGN KEY(org_id,project_id,environment_id) REFERENCES loomplane_environments(org_id,project_id,id) ON DELETE CASCADE,
 FOREIGN KEY(org_id,project_id,sync_id) REFERENCES loomplane_cloud_syncs(org_id,project_id,id) ON DELETE RESTRICT)`,
	`CREATE TABLE IF NOT EXISTS loomplane_cloud_sync_connections (
 org_id TEXT NOT NULL, sync_id TEXT NOT NULL REFERENCES loomplane_cloud_syncs(id) ON DELETE CASCADE,
 connection_id TEXT NOT NULL, PRIMARY KEY(sync_id,connection_id),
 FOREIGN KEY(org_id,connection_id) REFERENCES loomplane_cloud_connections(org_id,id) ON DELETE RESTRICT)`,
	// The same environment row orders approval, start, retry and sync acceptance.
	`CREATE OR REPLACE FUNCTION loomplane_deployment_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 DECLARE owner TEXT; plan_sync TEXT;
 BEGIN
 IF NEW.status<>'running' THEN RETURN NEW; END IF;
 PERFORM 1 FROM loomplane_environments WHERE org_id=NEW.org_id AND id=NEW.environment_id FOR UPDATE;
 SELECT sync_id INTO owner FROM loomplane_environment_sync_guards WHERE org_id=NEW.org_id AND environment_id=NEW.environment_id;
 SELECT coalesce(result->>'cloud_sync_id','') INTO plan_sync FROM loomplane_plans WHERE org_id=NEW.org_id AND id=NEW.plan_id;
 IF owner IS NOT NULL AND owner<>plan_sync THEN RAISE EXCEPTION 'environment waits for Cloud sync %',owner USING ERRCODE='55P03'; END IF;
 IF EXISTS(SELECT 1 FROM loomplane_deployments WHERE org_id=NEW.org_id AND environment_id=NEW.environment_id AND status='running' AND plan_id<>NEW.plan_id)
 THEN RAISE EXCEPTION 'environment has a running deployment' USING ERRCODE='55P03'; END IF;
 RETURN NEW;
 END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_deployment_sync_guard BEFORE INSERT OR UPDATE OF status ON loomplane_deployments FOR EACH ROW EXECUTE FUNCTION loomplane_deployment_guard()`,
	`CREATE OR REPLACE FUNCTION loomplane_approval_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 BEGIN
 IF NEW.status='approved' THEN
 PERFORM 1 FROM loomplane_environments WHERE org_id=NEW.org_id AND id=NEW.environment_id FOR UPDATE;
 END IF;
 RETURN NEW;
 END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_plan_approval_guard BEFORE INSERT OR UPDATE OF status ON loomplane_plans FOR EACH ROW EXECUTE FUNCTION loomplane_approval_guard()`,
	`CREATE OR REPLACE FUNCTION loomplane_cloud_sync_delete_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
 DECLARE active BOOLEAN;
 BEGIN
 SELECT EXISTS(SELECT 1 FROM loomplane_cloud_syncs WHERE org_id=OLD.org_id AND project_id=OLD.project_id AND status NOT IN ('succeeded','reverted') AND (cloud_id=OLD.id OR target_id=OLD.id))
 OR EXISTS(SELECT 1 FROM loomplane_cloud_previews WHERE org_id=OLD.org_id AND project_id=OLD.project_id AND created_at>now()-interval '24 hours' AND (body->'input'->>'cloud_id'=OLD.id OR body->'input'->>'target_id'=OLD.id OR body->'before_assignment'->>'cloud_id'=OLD.id)) INTO active;
 IF active THEN RAISE EXCEPTION 'Cloud configuration has retained migration references' USING ERRCODE='23503'; END IF;
 RETURN OLD;
 END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_cloud_sync_profile_delete BEFORE DELETE ON loomplane_cloud_profiles FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_sync_delete_guard()`,
	`CREATE OR REPLACE TRIGGER loomplane_cloud_sync_target_delete BEFORE DELETE ON loomplane_cloud_targets FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_sync_delete_guard()`,
}, cloudSyncCheckpointsUp...)

var cloudSyncDown = []string{
	`DROP TRIGGER IF EXISTS loomplane_cloud_sync_profile_delete ON loomplane_cloud_profiles`,
	`DROP TRIGGER IF EXISTS loomplane_cloud_sync_target_delete ON loomplane_cloud_targets`,
	`DROP FUNCTION IF EXISTS loomplane_cloud_sync_delete_guard()`,
	`DROP TABLE IF EXISTS loomplane_cloud_preview_connections`,
	`DROP TABLE IF EXISTS loomplane_cloud_sync_bindings`,
	`DROP TRIGGER IF EXISTS loomplane_plan_approval_guard ON loomplane_plans`,
	`DROP TRIGGER IF EXISTS loomplane_deployment_sync_guard ON loomplane_deployments`,
	`DROP FUNCTION IF EXISTS loomplane_approval_guard()`,
	`DROP FUNCTION IF EXISTS loomplane_deployment_guard()`,
	`DROP TABLE IF EXISTS loomplane_cloud_sync_connections`,
	`DROP TABLE IF EXISTS loomplane_environment_sync_guards`,
	`DROP TABLE IF EXISTS loomplane_cloud_syncs`,
	`DROP TABLE IF EXISTS loomplane_cloud_previews`,
	`DROP TABLE IF EXISTS loomplane_applied_variables`,
}

var cloudSyncCheckpointsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_cloud_sync_bindings (
 sync_id TEXT NOT NULL REFERENCES loomplane_cloud_syncs(id) ON DELETE CASCADE,
 org_id TEXT NOT NULL, environment_id TEXT NOT NULL, binding_id TEXT NOT NULL,
 after_value JSONB NOT NULL, PRIMARY KEY(sync_id,binding_id))`,
}
