package migrations

// A zone's DNS challenge connection is held like its own connection, by
// that zone alone, and can't be cleared while a wildcard route uses it.
// The insert names no conflict target, so a second zone taking the same
// connection fails on the index. Every route rule is the kubernetes_routes
// one, unchanged.
var zoneChallengeUsesUp = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_dns_challenge_connection ON loomplane_connection_uses(connection_id) WHERE kind='dns_challenge'`,
	`CREATE OR REPLACE FUNCTION loomplane_domain_scope() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF TG_TABLE_NAME='loomplane_dns_zones' THEN
 IF NEW.connection_id<>'' THEN
 INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.connection_id,'dns_zone',NEW.id) ON CONFLICT DO NOTHING;
 END IF;
 DELETE FROM loomplane_connection_uses WHERE kind='dns_challenge' AND owner_id=NEW.id;
 IF coalesce(NEW.body->>'challenge_connection_id','')<>'' THEN
 INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.body->>'challenge_connection_id','dns_challenge',NEW.id);
 ELSIF EXISTS(SELECT 1 FROM loomplane_domain_routes WHERE org_id=NEW.org_id AND zone_id=NEW.id AND status<>'removed' AND left(hostname,2)='*.') THEN
 RAISE EXCEPTION 'zone still has wildcard routes' USING ERRCODE='23503';
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
	`CREATE OR REPLACE FUNCTION loomplane_domain_delete_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF TG_TABLE_NAME='loomplane_cloud_profiles' THEN
 IF EXISTS(SELECT 1 FROM loomplane_domain_routes WHERE org_id=OLD.org_id AND cloud_id=OLD.id AND status<>'removed')
 OR EXISTS(SELECT 1 FROM loomplane_route_operations WHERE org_id=OLD.org_id AND cloud_id=OLD.id AND status NOT IN ('preview','succeeded','cancelled'))
 THEN RAISE EXCEPTION 'Cloud has domain claims or operations' USING ERRCODE='23503'; END IF;
 ELSIF TG_TABLE_NAME='loomplane_projects' THEN
 IF EXISTS(SELECT 1 FROM loomplane_domain_routes WHERE org_id=OLD.org_id AND project_id=OLD.id AND status<>'removed') THEN RAISE EXCEPTION 'remove project routes first' USING ERRCODE='23503'; END IF;
 ELSE
 IF EXISTS(SELECT 1 FROM loomplane_domain_routes WHERE org_id=OLD.org_id AND zone_id=OLD.id AND status<>'removed') THEN RAISE EXCEPTION 'zone still has claims' USING ERRCODE='23503'; END IF;
 DELETE FROM loomplane_connection_uses WHERE kind IN ('dns_zone','dns_challenge') AND owner_id=OLD.id;
 END IF; RETURN OLD; END $$`,
}

func init() {
	App.MustRegister(statements("20261105000400", "zone_challenge_uses", zoneChallengeUsesUp, nil))
}
