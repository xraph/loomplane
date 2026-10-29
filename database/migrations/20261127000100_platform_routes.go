package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// A platform route (an app platform app's domain) rests on a ready cloud
// connection and no server, and the DNS records its operation writes are
// kept as receipts on the operation and on its route. Everything else in
// both functions is as the migrations before left it.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			return platformRoutesFunctions(ctx, db, platformRoutesScope, platformRoutesGuard)
		},
		func(ctx context.Context, db *migrate.DB) error {
			return platformRoutesFunctions(ctx, db, previousDomainScope, previousRouteIntentGuard)
		},
	)
}

func platformRoutesFunctions(ctx context.Context, db *migrate.DB, scope, guard string) error {
	if _, err := db.Exec(ctx, scope); err != nil {
		return fmt.Errorf("create function loomplane_domain_scope: %w", err)
	}
	if _, err := db.Exec(ctx, guard); err != nil {
		return fmt.Errorf("create function loomplane_route_intent_guard: %w", err)
	}
	return nil
}

const platformRoutesScope = `CREATE OR REPLACE FUNCTION loomplane_domain_scope() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF TG_TABLE_NAME='loomplane_dns_zones' THEN
		IF NEW.connection_id<>'' THEN
			INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.connection_id,'dns_zone',NEW.id) ON CONFLICT DO NOTHING;
		END IF;
		DELETE FROM loomplane_connection_uses WHERE kind='dns_challenge' AND owner_id=NEW.id;
		IF coalesce(NEW.body->>'challenge_connection_id','')<>'' THEN
			INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.body->>'challenge_connection_id','dns_challenge',NEW.id);
		ELSIF EXISTS(SELECT 1 FROM loomplane_domain_routes WHERE org_id=NEW.org_id AND zone_id=NEW.id AND status<>'removed' AND left(hostname,2)='*.') THEN
			RAISE EXCEPTION 'zone still has wildcard routes' USING ERRCODE='23503';
		END IF;
		RETURN NEW;
	END IF;

	IF NEW.status='removed' THEN
		DELETE FROM loomplane_connection_uses WHERE kind='domain_route' AND owner_id=NEW.id;
		RETURN NEW;
	END IF;

	PERFORM 1 FROM loomplane_environments WHERE org_id=NEW.org_id AND project_id=NEW.project_id AND id=NEW.environment_id FOR KEY SHARE;
	IF NOT FOUND THEN RAISE EXCEPTION 'route scope missing' USING ERRCODE='23503'; END IF;
	PERFORM 1 FROM loomplane_dns_zones WHERE org_id=NEW.org_id AND id=NEW.zone_id FOR KEY SHARE;
	IF NOT FOUND THEN RAISE EXCEPTION 'route zone missing' USING ERRCODE='23503'; END IF;
	IF NEW.backend='platform' THEN
		PERFORM 1 FROM loomplane_cloud_connections WHERE org_id=NEW.org_id AND id=NEW.connection_id AND status='ready' FOR KEY SHARE;
		IF NOT FOUND THEN RAISE EXCEPTION 'route connection unavailable' USING ERRCODE='23503'; END IF;
	ELSIF NEW.backend='kubernetes' THEN
		PERFORM 1 FROM loomplane_cloud_connections WHERE org_id=NEW.org_id AND id=NEW.connection_id AND cloud='kubernetes' AND status='ready' FOR KEY SHARE;
		IF NOT FOUND THEN RAISE EXCEPTION 'route connection unavailable' USING ERRCODE='23503'; END IF;
	ELSE
		PERFORM 1 FROM loomplane_cloud_servers WHERE org_id=NEW.org_id AND cloud_id=NEW.cloud_id AND id=NEW.server_id AND status='ready' FOR KEY SHARE;
		IF NOT FOUND THEN RAISE EXCEPTION 'route server unavailable' USING ERRCODE='23503'; END IF;
	END IF;

	DELETE FROM loomplane_connection_uses WHERE kind='domain_route' AND owner_id=NEW.id;
	INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.connection_id,'domain_route',NEW.id) ON CONFLICT DO NOTHING;
	RETURN NEW;
END $$`

const platformRoutesGuard = `CREATE OR REPLACE FUNCTION loomplane_route_intent_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.principal IS DISTINCT FROM OLD.principal
		OR (NEW.body - ARRAY['route','status','message','attempt','revision','generation','dns_attempted','receipt','receipts']) IS DISTINCT FROM (OLD.body - ARRAY['route','status','message','attempt','revision','generation','dns_attempted','receipt','receipts'])
		OR ((NEW.body->'route') - ARRAY['status','operation_id','receipt','receipts','verification','revision','updated_at']) IS DISTINCT FROM ((OLD.body->'route') - ARRAY['status','operation_id','receipt','receipts','verification','revision','updated_at'])
	THEN
		RAISE EXCEPTION 'route intent is immutable' USING ERRCODE='23514';
	END IF;
	IF OLD.body->>'dns_attempted'='true' AND NEW.body->>'dns_attempted'<>'true' THEN
		RAISE EXCEPTION 'DNS attempt cannot be cleared' USING ERRCODE='23514';
	END IF;
	RETURN NEW;
END $$`

const previousDomainScope = `CREATE OR REPLACE FUNCTION loomplane_domain_scope() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF TG_TABLE_NAME='loomplane_dns_zones' THEN
		IF NEW.connection_id<>'' THEN
			INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.connection_id,'dns_zone',NEW.id) ON CONFLICT DO NOTHING;
		END IF;
		DELETE FROM loomplane_connection_uses WHERE kind='dns_challenge' AND owner_id=NEW.id;
		IF coalesce(NEW.body->>'challenge_connection_id','')<>'' THEN
			INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.body->>'challenge_connection_id','dns_challenge',NEW.id);
		ELSIF EXISTS(SELECT 1 FROM loomplane_domain_routes WHERE org_id=NEW.org_id AND zone_id=NEW.id AND status<>'removed' AND left(hostname,2)='*.') THEN
			RAISE EXCEPTION 'zone still has wildcard routes' USING ERRCODE='23503';
		END IF;
		RETURN NEW;
	END IF;

	IF NEW.status='removed' THEN
		DELETE FROM loomplane_connection_uses WHERE kind='domain_route' AND owner_id=NEW.id;
		RETURN NEW;
	END IF;

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
	RETURN NEW;
END $$`

const previousRouteIntentGuard = `CREATE OR REPLACE FUNCTION loomplane_route_intent_guard() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.principal IS DISTINCT FROM OLD.principal
		OR (NEW.body - ARRAY['route','status','message','attempt','revision','generation','dns_attempted','receipt']) IS DISTINCT FROM (OLD.body - ARRAY['route','status','message','attempt','revision','generation','dns_attempted','receipt'])
		OR ((NEW.body->'route') - ARRAY['status','operation_id','receipt','verification','revision','updated_at']) IS DISTINCT FROM ((OLD.body->'route') - ARRAY['status','operation_id','receipt','verification','revision','updated_at'])
	THEN
		RAISE EXCEPTION 'route intent is immutable' USING ERRCODE='23514';
	END IF;
	IF OLD.body->>'dns_attempted'='true' AND NEW.body->>'dns_attempted'<>'true' THEN
		RAISE EXCEPTION 'DNS attempt cannot be cleared' USING ERRCODE='23514';
	END IF;
	RETURN NEW;
END $$`
