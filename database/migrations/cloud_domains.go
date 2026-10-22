package migrations

var cloudDomainsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_dns_zones (
 id TEXT PRIMARY KEY, org_id TEXT NOT NULL, name TEXT NOT NULL, connection_id TEXT NOT NULL DEFAULT '',
 revision BIGINT NOT NULL, body JSONB NOT NULL, UNIQUE(org_id,name))`,
	`CREATE TABLE IF NOT EXISTS loomplane_domain_routes (
 id TEXT PRIMARY KEY, org_id TEXT NOT NULL, project_id TEXT NOT NULL, environment_id TEXT NOT NULL,
 cloud_id TEXT NOT NULL, server_id TEXT NOT NULL, connection_id TEXT NOT NULL, zone_id TEXT NOT NULL,
 hostname TEXT NOT NULL, status TEXT NOT NULL, revision BIGINT NOT NULL, body JSONB NOT NULL)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_hostname_claim ON loomplane_domain_routes(hostname) WHERE status<>'removed'`,
	`CREATE TABLE IF NOT EXISTS loomplane_route_operations (
 id TEXT PRIMARY KEY, org_id TEXT NOT NULL, project_id TEXT NOT NULL, route_id TEXT NOT NULL, server_id TEXT NOT NULL,
 cloud_id TEXT NOT NULL, status TEXT NOT NULL, revision BIGINT NOT NULL, generation BIGINT NOT NULL DEFAULT 0,
 body JSONB NOT NULL, principal JSONB NOT NULL, checkpoint JSONB NOT NULL DEFAULT '{}')`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_active_route_operation ON loomplane_route_operations(route_id) WHERE status NOT IN ('preview','succeeded','cancelled')`,
	`CREATE TABLE IF NOT EXISTS loomplane_ingress_configs (
 org_id TEXT NOT NULL, server_id TEXT NOT NULL, revision BIGINT NOT NULL DEFAULT 0, generation BIGINT NOT NULL DEFAULT 0,
 body JSONB NOT NULL DEFAULT '{}', PRIMARY KEY(org_id,server_id))`,
	`CREATE TABLE IF NOT EXISTS loomplane_route_handoffs(id TEXT PRIMARY KEY,org_id TEXT NOT NULL,route_id TEXT NOT NULL,source_connection_id TEXT NOT NULL,destination_connection_id TEXT NOT NULL,released BOOLEAN NOT NULL DEFAULT false)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_active_route_handoff ON loomplane_route_handoffs(route_id) WHERE NOT released`,
	`CREATE OR REPLACE FUNCTION loomplane_route_operation_uses() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 DELETE FROM loomplane_connection_uses WHERE kind='route_operation' AND owner_id=NEW.id;
 IF NEW.status NOT IN ('preview','succeeded','cancelled') THEN
 INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.body->'route'->'binding'->>'connection_id','route_operation',NEW.id) ON CONFLICT DO NOTHING;
 END IF;
 IF TG_OP='INSERT' AND NEW.body->>'kind'='prepare' THEN
 INSERT INTO loomplane_route_handoffs(id,org_id,route_id,source_connection_id,destination_connection_id) VALUES(NEW.id,NEW.org_id,NEW.route_id,NEW.body->'source'->'binding'->>'connection_id',NEW.body->'route'->'binding'->>'connection_id');
 INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.body->'source'->'binding'->>'connection_id','route_handoff',NEW.id),(NEW.org_id,NEW.body->'route'->'binding'->>'connection_id','route_handoff',NEW.id) ON CONFLICT DO NOTHING;
 END IF; RETURN NEW; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_route_operation_uses AFTER INSERT OR UPDATE ON loomplane_route_operations FOR EACH ROW EXECUTE FUNCTION loomplane_route_operation_uses()`,
	`CREATE OR REPLACE FUNCTION loomplane_route_intent_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.principal IS DISTINCT FROM OLD.principal
 OR (NEW.body - ARRAY['route','status','message','attempt','revision','generation','dns_attempted','receipt']) IS DISTINCT FROM (OLD.body - ARRAY['route','status','message','attempt','revision','generation','dns_attempted','receipt'])
 OR ((NEW.body->'route') - ARRAY['status','operation_id','receipt','verification','revision','updated_at']) IS DISTINCT FROM ((OLD.body->'route') - ARRAY['status','operation_id','receipt','verification','revision','updated_at'])
 THEN RAISE EXCEPTION 'route intent is immutable' USING ERRCODE='23514'; END IF;
 IF OLD.body->>'dns_attempted'='true' AND NEW.body->>'dns_attempted'<>'true' THEN RAISE EXCEPTION 'DNS attempt cannot be cleared' USING ERRCODE='23514'; END IF;
 RETURN NEW; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_route_intent BEFORE UPDATE ON loomplane_route_operations FOR EACH ROW EXECUTE FUNCTION loomplane_route_intent_guard()`,
	`CREATE OR REPLACE FUNCTION loomplane_domain_scope() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF TG_TABLE_NAME='loomplane_dns_zones' THEN
 IF NEW.connection_id<>'' THEN
 INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.connection_id,'dns_zone',NEW.id) ON CONFLICT DO NOTHING;
 END IF; RETURN NEW; END IF;
 IF NEW.status='removed' THEN
 DELETE FROM loomplane_connection_uses WHERE kind='domain_route' AND owner_id=NEW.id; RETURN NEW; END IF;
 PERFORM 1 FROM loomplane_environments WHERE org_id=NEW.org_id AND project_id=NEW.project_id AND id=NEW.environment_id FOR KEY SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'route scope missing' USING ERRCODE='23503'; END IF;
 PERFORM 1 FROM loomplane_dns_zones WHERE org_id=NEW.org_id AND id=NEW.zone_id FOR KEY SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'route zone missing' USING ERRCODE='23503'; END IF;
 PERFORM 1 FROM loomplane_cloud_servers WHERE org_id=NEW.org_id AND cloud_id=NEW.cloud_id AND id=NEW.server_id AND status='ready' FOR KEY SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'route server unavailable' USING ERRCODE='23503'; END IF;
 DELETE FROM loomplane_connection_uses WHERE kind='domain_route' AND owner_id=NEW.id;
 INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.connection_id,'domain_route',NEW.id) ON CONFLICT DO NOTHING;
 RETURN NEW; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_domain_route_scope BEFORE INSERT OR UPDATE ON loomplane_domain_routes FOR EACH ROW EXECUTE FUNCTION loomplane_domain_scope()`,
	`CREATE OR REPLACE TRIGGER loomplane_dns_zone_scope AFTER INSERT OR UPDATE ON loomplane_dns_zones FOR EACH ROW EXECUTE FUNCTION loomplane_domain_scope()`,
	`CREATE OR REPLACE FUNCTION loomplane_domain_delete_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF TG_TABLE_NAME='loomplane_cloud_profiles' THEN
 IF EXISTS(SELECT 1 FROM loomplane_domain_routes WHERE org_id=OLD.org_id AND cloud_id=OLD.id AND status<>'removed')
 OR EXISTS(SELECT 1 FROM loomplane_route_operations WHERE org_id=OLD.org_id AND cloud_id=OLD.id AND status NOT IN ('preview','succeeded','cancelled'))
 THEN RAISE EXCEPTION 'Cloud has domain claims or operations' USING ERRCODE='23503'; END IF;
 ELSIF TG_TABLE_NAME='loomplane_projects' THEN
 IF EXISTS(SELECT 1 FROM loomplane_domain_routes WHERE org_id=OLD.org_id AND project_id=OLD.id AND status<>'removed') THEN RAISE EXCEPTION 'remove project routes first' USING ERRCODE='23503'; END IF;
 ELSE
 IF EXISTS(SELECT 1 FROM loomplane_domain_routes WHERE org_id=OLD.org_id AND zone_id=OLD.id AND status<>'removed') THEN RAISE EXCEPTION 'zone still has claims' USING ERRCODE='23503'; END IF;
 DELETE FROM loomplane_connection_uses WHERE kind='dns_zone' AND owner_id=OLD.id;
 END IF; RETURN OLD; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_domain_cloud_delete BEFORE DELETE ON loomplane_cloud_profiles FOR EACH ROW EXECUTE FUNCTION loomplane_domain_delete_guard()`,
	`CREATE OR REPLACE TRIGGER loomplane_domain_project_delete BEFORE DELETE ON loomplane_projects FOR EACH ROW EXECUTE FUNCTION loomplane_domain_delete_guard()`,
	`CREATE OR REPLACE TRIGGER loomplane_domain_zone_delete BEFORE DELETE ON loomplane_dns_zones FOR EACH ROW EXECUTE FUNCTION loomplane_domain_delete_guard()`,
}

var cloudDomainsDown = []string{
	`DROP TRIGGER IF EXISTS loomplane_domain_cloud_delete ON loomplane_cloud_profiles`,
	`DROP TRIGGER IF EXISTS loomplane_domain_project_delete ON loomplane_projects`,
	`DROP TABLE IF EXISTS loomplane_route_handoffs`, `DROP TABLE IF EXISTS loomplane_route_operations`, `DROP TABLE IF EXISTS loomplane_ingress_configs`,
	`DROP TABLE IF EXISTS loomplane_domain_routes`, `DROP TABLE IF EXISTS loomplane_dns_zones`,
	`DROP FUNCTION IF EXISTS loomplane_domain_scope()`, `DROP FUNCTION IF EXISTS loomplane_domain_delete_guard()`,
	`DROP FUNCTION IF EXISTS loomplane_route_intent_guard()`, `DROP FUNCTION IF EXISTS loomplane_route_operation_uses()`,
}
