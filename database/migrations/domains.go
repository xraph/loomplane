package migrations

// A registration holds its connection from approval on, unless it ended
// without a domain. Only one registration of a name can be active at once.
var domainRegistrationsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_domain_registrations (
 id TEXT PRIMARY KEY, org_id TEXT NOT NULL, connection_id TEXT NOT NULL, name TEXT NOT NULL, status TEXT NOT NULL,
 revision BIGINT NOT NULL, body JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp())`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_domain_registration_active ON loomplane_domain_registrations(org_id,name) WHERE status IN ('approved','registering','zone_pending','succeeded')`,
	`CREATE OR REPLACE FUNCTION loomplane_domain_registration_uses() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN DELETE FROM loomplane_connection_uses WHERE kind='domain_registration' AND owner_id=OLD.id; RETURN OLD; END IF;
 DELETE FROM loomplane_connection_uses WHERE kind='domain_registration' AND owner_id=NEW.id;
 IF NEW.status NOT IN ('preview','expired','price_changed','failed','unconfirmed') THEN
 INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.connection_id,'domain_registration',NEW.id) ON CONFLICT DO NOTHING;
 END IF; RETURN NEW; END $$`,
	`CREATE OR REPLACE TRIGGER loomplane_domain_registration_uses AFTER INSERT OR UPDATE OR DELETE ON loomplane_domain_registrations FOR EACH ROW EXECUTE FUNCTION loomplane_domain_registration_uses()`,
}

var domainRegistrationsDown = []string{
	`DROP TRIGGER IF EXISTS loomplane_domain_registration_uses ON loomplane_domain_registrations`,
	`DROP FUNCTION IF EXISTS loomplane_domain_registration_uses()`,
	`DELETE FROM loomplane_connection_uses WHERE kind='domain_registration'`,
	`DROP TABLE IF EXISTS loomplane_domain_registrations`,
}

// Outbox rows were all deployment notices until domain notices joined them.
var notificationKindsUp = []string{
	`ALTER TABLE loomplane_notification_outbox ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'deployment'`,
}

// Routes and their operations record which backend serves them. A Kubernetes
// route holds a ready Kubernetes connection where a server route holds a
// ready Cloud server. Every other rule is the cloud_domains one, unchanged.
var kubernetesRoutesUp = []string{
	`ALTER TABLE loomplane_domain_routes ADD COLUMN IF NOT EXISTS backend TEXT NOT NULL DEFAULT 'server'`,
	`ALTER TABLE loomplane_route_operations ADD COLUMN IF NOT EXISTS backend TEXT NOT NULL DEFAULT 'server'`,
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
 IF NEW.backend='kubernetes' THEN
 PERFORM 1 FROM loomplane_cloud_connections WHERE org_id=NEW.org_id AND id=NEW.connection_id AND cloud='kubernetes' AND status='ready' FOR KEY SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'route connection unavailable' USING ERRCODE='23503'; END IF;
 ELSE
 PERFORM 1 FROM loomplane_cloud_servers WHERE org_id=NEW.org_id AND cloud_id=NEW.cloud_id AND id=NEW.server_id AND status='ready' FOR KEY SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'route server unavailable' USING ERRCODE='23503'; END IF;
 END IF;
 DELETE FROM loomplane_connection_uses WHERE kind='domain_route' AND owner_id=NEW.id;
 INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.connection_id,'domain_route',NEW.id) ON CONFLICT DO NOTHING;
 RETURN NEW; END $$`,
}
