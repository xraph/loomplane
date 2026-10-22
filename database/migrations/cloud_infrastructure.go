package migrations

var cloudInfrastructureUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_cloud_servers (
 id TEXT PRIMARY KEY, org_id TEXT NOT NULL, cloud_id TEXT NOT NULL,
 connection_id TEXT NOT NULL, runtime_connection_id TEXT NOT NULL DEFAULT '',
 provider TEXT NOT NULL, account_id TEXT NOT NULL, resource_id TEXT NOT NULL DEFAULT '',
 ownership TEXT NOT NULL CHECK(ownership IN ('managed','adopted')),
 revision BIGINT NOT NULL CHECK(revision>0), status TEXT NOT NULL, body JSONB NOT NULL)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_cloud_server_resource ON loomplane_cloud_servers(org_id,provider,account_id,resource_id) WHERE resource_id<>'' AND status NOT IN ('deleted','disconnected')`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_cloud_server_runtime ON loomplane_cloud_servers(org_id,runtime_connection_id) WHERE runtime_connection_id<>'' AND status NOT IN ('deleted','disconnected')`,
	`CREATE TABLE IF NOT EXISTS loomplane_infrastructure_operations (
 id TEXT PRIMARY KEY, org_id TEXT NOT NULL, cloud_id TEXT NOT NULL, server_id TEXT NOT NULL,
 status TEXT NOT NULL, body JSONB NOT NULL, principal JSONB NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_infrastructure_active_server ON loomplane_infrastructure_operations(org_id,server_id) WHERE status NOT IN ('preview','succeeded','cancelled')`,
	`CREATE OR REPLACE FUNCTION loomplane_infrastructure_intent_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF (NEW.body - ARRAY['state_revision','status','message','attempt','attempted','actions','resource_id','public_ipv4','host_key','enrolled','bootstrap_expires_at','updated_at']) IS DISTINCT FROM
 (OLD.body - ARRAY['state_revision','status','message','attempt','attempted','actions','resource_id','public_ipv4','host_key','enrolled','bootstrap_expires_at','updated_at']) OR NEW.principal IS DISTINCT FROM OLD.principal
 THEN RAISE EXCEPTION 'infrastructure intent is immutable' USING ERRCODE='23514'; END IF;
 IF OLD.body->>'attempted'='true' AND NEW.body->>'attempted'<>'true' THEN RAISE EXCEPTION 'infrastructure attempt cannot be cleared' USING ERRCODE='23514'; END IF;
 IF coalesce(OLD.body->>'resource_id','')<>'' AND NEW.body->>'resource_id' IS DISTINCT FROM OLD.body->>'resource_id' THEN RAISE EXCEPTION 'infrastructure identity is immutable' USING ERRCODE='23514'; END IF;
 IF coalesce(OLD.body->>'host_key','')<>'' AND NEW.body->>'host_key' IS DISTINCT FROM OLD.body->>'host_key' THEN RAISE EXCEPTION 'enrolled host key is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW; END $$`,
	`DROP TRIGGER IF EXISTS loomplane_infrastructure_intent ON loomplane_infrastructure_operations`,
	`CREATE TRIGGER loomplane_infrastructure_intent BEFORE UPDATE ON loomplane_infrastructure_operations FOR EACH ROW EXECUTE FUNCTION loomplane_infrastructure_intent_guard()`,
	`CREATE OR REPLACE FUNCTION loomplane_infrastructure_connection_uses() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF TG_TABLE_NAME='loomplane_cloud_servers' THEN
 DELETE FROM loomplane_connection_uses WHERE kind='cloud_server' AND owner_id=NEW.id;
 IF NEW.status NOT IN ('deleted','disconnected') THEN
 INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.connection_id,'cloud_server',NEW.id) ON CONFLICT DO NOTHING;
 IF NEW.runtime_connection_id<>'' THEN INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.runtime_connection_id,'cloud_server',NEW.id) ON CONFLICT DO NOTHING; END IF;
 END IF;
 ELSE
 DELETE FROM loomplane_connection_uses WHERE kind='infrastructure_operation' AND owner_id=NEW.id;
 IF NEW.status NOT IN ('preview','succeeded','cancelled') THEN INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.body->>'connection_id','infrastructure_operation',NEW.id); END IF;
 END IF; RETURN NEW; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_server_connection_uses AFTER INSERT OR UPDATE ON loomplane_cloud_servers FOR EACH ROW EXECUTE FUNCTION loomplane_infrastructure_connection_uses()`,
	`CREATE OR REPLACE TRIGGER loomplane_operation_connection_uses AFTER INSERT OR UPDATE ON loomplane_infrastructure_operations FOR EACH ROW EXECUTE FUNCTION loomplane_infrastructure_connection_uses()`,
	`CREATE OR REPLACE FUNCTION loomplane_retiring_server_use() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.kind IN ('cloud_server','infrastructure_operation') THEN RETURN NEW; END IF;
 PERFORM 1 FROM loomplane_cloud_connections WHERE org_id=NEW.org_id AND id=NEW.connection_id FOR UPDATE;
 IF EXISTS(SELECT 1 FROM loomplane_cloud_servers WHERE org_id=NEW.org_id AND runtime_connection_id=NEW.connection_id AND status<>'ready') THEN RAISE EXCEPTION 'server is retiring' USING ERRCODE='23503'; END IF;
 RETURN NEW; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_retiring_server_use BEFORE INSERT ON loomplane_connection_uses FOR EACH ROW EXECUTE FUNCTION loomplane_retiring_server_use()`,

	`CREATE OR REPLACE FUNCTION loomplane_infrastructure_cloud_scope() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 PERFORM 1 FROM loomplane_cloud_profiles WHERE org_id=NEW.org_id AND id=NEW.cloud_id FOR KEY SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'Cloud scope missing' USING ERRCODE='23503'; END IF; RETURN NEW; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_infrastructure_cloud_scope BEFORE INSERT ON loomplane_infrastructure_operations FOR EACH ROW EXECUTE FUNCTION loomplane_infrastructure_cloud_scope()`,
	`CREATE OR REPLACE TRIGGER loomplane_server_cloud_scope BEFORE INSERT ON loomplane_cloud_servers FOR EACH ROW EXECUTE FUNCTION loomplane_infrastructure_cloud_scope()`,
	`CREATE OR REPLACE FUNCTION loomplane_infrastructure_cloud_delete() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF EXISTS(SELECT 1 FROM loomplane_cloud_servers WHERE org_id=OLD.org_id AND cloud_id=OLD.id AND status NOT IN ('deleted','disconnected'))
 OR EXISTS(SELECT 1 FROM loomplane_infrastructure_operations WHERE org_id=OLD.org_id AND cloud_id=OLD.id AND status NOT IN ('preview','succeeded','cancelled'))
 THEN RAISE EXCEPTION 'Cloud still owns infrastructure' USING ERRCODE='23503'; END IF;
 RETURN OLD; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_infrastructure_cloud_delete BEFORE DELETE ON loomplane_cloud_profiles FOR EACH ROW EXECUTE FUNCTION loomplane_infrastructure_cloud_delete()`,
}
var cloudInfrastructureDown = []string{`DROP TRIGGER IF EXISTS loomplane_infrastructure_cloud_delete ON loomplane_cloud_profiles`, `DROP FUNCTION IF EXISTS loomplane_infrastructure_cloud_delete()`, `DROP TRIGGER IF EXISTS loomplane_retiring_server_use ON loomplane_connection_uses`, `DROP FUNCTION IF EXISTS loomplane_retiring_server_use()`, `DROP TABLE IF EXISTS loomplane_infrastructure_operations`, `DROP TABLE IF EXISTS loomplane_cloud_servers`, `DROP FUNCTION IF EXISTS loomplane_infrastructure_intent_guard()`, `DROP FUNCTION IF EXISTS loomplane_infrastructure_connection_uses()`, `DROP FUNCTION IF EXISTS loomplane_infrastructure_cloud_scope()`}
