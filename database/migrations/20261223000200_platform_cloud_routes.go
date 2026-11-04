package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// Routes to Loomplane Cloud services, and the hostnames the platform Cloud
// generates for them.
//
// loomplane_platform_hostnames keeps each service's generated name under the
// platform apex and the record last published for it: the host the service
// ran on, that host's public ingress address and the DNS provider's receipt,
// so a move rewrites the record and a removal deletes it.
//
// A custom domain on a Loomplane Cloud service is a Firecracker route on the
// platform's connection, "platform", which isn't an organization connection.
// It rests on the service's live placement instead, and holds no connection
// use, and is never handed off to or from another backend. Everything else
// in both functions is as the migrations before left it.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateTable("loomplane_platform_hostnames").IfNotExists().
				Column("org_id", migrate.Text, migrate.NotNull()).
				Column("environment_id", migrate.Text, migrate.NotNull()).
				Column("resource", migrate.Text, migrate.NotNull()).
				Column("project_id", migrate.Text, migrate.NotNull()).
				Column("hostname", migrate.Text, migrate.NotNull(), migrate.Unique()).
				Column("host_id", migrate.Text,
					migrate.References("loomplane_platform_hosts", "id"), migrate.OnDelete(migrate.SetNull)).
				Column("address", migrate.Text, migrate.NotNull(), migrate.Default("")).
				Column("receipt", migrate.JSON).
				Column("created_at", migrate.Timestamp, migrate.NotNull()).
				Column("updated_at", migrate.Timestamp, migrate.NotNull()).
				PrimaryKey("org_id", "environment_id", "resource").
				Exec(ctx)
			if err != nil {
				return err
			}
			return platformCloudRouteFunctions(ctx, db, platformCloudRoutesScope, platformCloudRouteOperationUses)
		},
		func(ctx context.Context, db *migrate.DB) error {
			if err := platformCloudRouteFunctions(ctx, db, platformRoutesScope, previousRouteOperationUses); err != nil {
				return err
			}
			return db.NewDropTable("loomplane_platform_hostnames").IfExists().Exec(ctx)
		},
	)
}

func platformCloudRouteFunctions(ctx context.Context, db *migrate.DB, scope, uses string) error {
	if _, err := db.Exec(ctx, scope); err != nil {
		return fmt.Errorf("create function loomplane_domain_scope: %w", err)
	}
	if _, err := db.Exec(ctx, uses); err != nil {
		return fmt.Errorf("create function loomplane_route_operation_uses: %w", err)
	}
	return nil
}

const platformCloudRoutesScope = `CREATE OR REPLACE FUNCTION loomplane_domain_scope() RETURNS TRIGGER LANGUAGE plpgsql AS $$
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
	IF NEW.backend='firecracker' AND NEW.connection_id='platform' THEN
		PERFORM 1 FROM loomplane_platform_placements WHERE org_id=NEW.org_id AND environment_id=NEW.environment_id AND resource=NEW.body->'input'->>'service' AND kind='service' AND state<>'released' FOR KEY SHARE;
		IF NOT FOUND THEN RAISE EXCEPTION 'route placement unavailable' USING ERRCODE='23503'; END IF;
		DELETE FROM loomplane_connection_uses WHERE kind='domain_route' AND owner_id=NEW.id;
		RETURN NEW;
	ELSIF NEW.backend='platform' THEN
		PERFORM 1 FROM loomplane_cloud_connections WHERE org_id=NEW.org_id AND id=NEW.connection_id AND status='ready' FOR KEY SHARE;
		IF NOT FOUND THEN RAISE EXCEPTION 'route connection unavailable' USING ERRCODE='23503'; END IF;
	ELSIF NEW.backend IN ('kubernetes','firecracker') THEN
		PERFORM 1 FROM loomplane_cloud_connections WHERE org_id=NEW.org_id AND id=NEW.connection_id AND cloud=NEW.backend AND status='ready' FOR KEY SHARE;
		IF NOT FOUND THEN RAISE EXCEPTION 'route connection unavailable' USING ERRCODE='23503'; END IF;
	ELSE
		PERFORM 1 FROM loomplane_cloud_servers WHERE org_id=NEW.org_id AND cloud_id=NEW.cloud_id AND id=NEW.server_id AND status='ready' FOR KEY SHARE;
		IF NOT FOUND THEN RAISE EXCEPTION 'route server unavailable' USING ERRCODE='23503'; END IF;
	END IF;

	DELETE FROM loomplane_connection_uses WHERE kind='domain_route' AND owner_id=NEW.id;
	INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.connection_id,'domain_route',NEW.id) ON CONFLICT DO NOTHING;
	RETURN NEW;
END $$`

const platformCloudRouteOperationUses = `CREATE OR REPLACE FUNCTION loomplane_route_operation_uses() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	DELETE FROM loomplane_connection_uses WHERE kind='route_operation' AND owner_id=NEW.id;
	IF NEW.status NOT IN ('preview','succeeded','cancelled') AND NEW.body->'route'->'binding'->>'connection_id'<>'platform' THEN
		INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.body->'route'->'binding'->>'connection_id','route_operation',NEW.id) ON CONFLICT DO NOTHING;
	END IF;

	IF TG_OP='INSERT' AND NEW.body->>'kind'='prepare' AND 'platform' IN (NEW.body->'route'->'binding'->>'connection_id',NEW.body->'source'->'binding'->>'connection_id') THEN
		RAISE EXCEPTION 'a Loomplane Cloud route is never handed off' USING ERRCODE='23514';
	END IF;
	IF TG_OP='INSERT' AND NEW.body->>'kind'='prepare' THEN
		INSERT INTO loomplane_route_handoffs(id,org_id,route_id,source_connection_id,destination_connection_id) VALUES(NEW.id,NEW.org_id,NEW.route_id,NEW.body->'source'->'binding'->>'connection_id',NEW.body->'route'->'binding'->>'connection_id');
		INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.body->'source'->'binding'->>'connection_id','route_handoff',NEW.id),(NEW.org_id,NEW.body->'route'->'binding'->>'connection_id','route_handoff',NEW.id) ON CONFLICT DO NOTHING;
	END IF;
	RETURN NEW;
END $$`

const previousRouteOperationUses = `CREATE OR REPLACE FUNCTION loomplane_route_operation_uses() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
	DELETE FROM loomplane_connection_uses WHERE kind='route_operation' AND owner_id=NEW.id;
	IF NEW.status NOT IN ('preview','succeeded','cancelled') THEN
		INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.body->'route'->'binding'->>'connection_id','route_operation',NEW.id) ON CONFLICT DO NOTHING;
	END IF;

	IF TG_OP='INSERT' AND NEW.body->>'kind'='prepare' THEN
		INSERT INTO loomplane_route_handoffs(id,org_id,route_id,source_connection_id,destination_connection_id) VALUES(NEW.id,NEW.org_id,NEW.route_id,NEW.body->'source'->'binding'->>'connection_id',NEW.body->'route'->'binding'->>'connection_id');
		INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES(NEW.org_id,NEW.body->'source'->'binding'->>'connection_id','route_handoff',NEW.id),(NEW.org_id,NEW.body->'route'->'binding'->>'connection_id','route_handoff',NEW.id) ON CONFLICT DO NOTHING;
	END IF;
	RETURN NEW;
END $$`
