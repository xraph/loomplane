package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, cloudLocationsSchema)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_cloud_locations) THEN RAISE EXCEPTION 'retire Cloud locations before downgrade'; END IF; END $$;
 DROP TRIGGER loomplane_cloud_location_target_guard ON loomplane_cloud_targets;
 DROP FUNCTION loomplane_cloud_location_target_guard();
 DROP TRIGGER loomplane_cloud_location_membership_guard ON loomplane_resource_relations;
 DROP FUNCTION loomplane_cloud_location_membership_guard();
 DROP TABLE loomplane_cloud_locations;
 DROP FUNCTION loomplane_cloud_location_guard();`)
		return err
	})
}

const cloudLocationsSchema = `
CREATE TABLE loomplane_cloud_locations (
 id text PRIMARY KEY,org_id text NOT NULL,cloud_id text NOT NULL,name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 64),
 provider text NOT NULL CHECK(provider<>''),account_id text NOT NULL CHECK(account_id<>''),connection_id text NOT NULL,
 region text NOT NULL CHECK(region<>''),revision bigint NOT NULL CHECK(revision>0),body jsonb NOT NULL,
 UNIQUE(org_id,cloud_id,id),
 FOREIGN KEY(org_id,cloud_id,id) REFERENCES loomplane_managed_resources(org_id,cloud_id,id),
 FOREIGN KEY(org_id,connection_id) REFERENCES loomplane_cloud_connections(org_id,id)
);
CREATE UNIQUE INDEX loomplane_cloud_location_alias ON loomplane_cloud_locations(org_id,cloud_id,lower(btrim(name)));
CREATE FUNCTION loomplane_cloud_location_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||coalesce(NEW.org_id,OLD.org_id),0));
 IF TG_OP='DELETE' THEN
 IF EXISTS(SELECT 1 FROM loomplane_cloud_targets WHERE org_id=OLD.org_id AND location_id=OLD.id)
 OR EXISTS(SELECT 1 FROM loomplane_resource_relations WHERE org_id=OLD.org_id AND (source_id=OLD.id OR destination_id=OLD.id))
 THEN RAISE EXCEPTION 'location still has attached resources or targets' USING ERRCODE='23503'; END IF;
 RETURN OLD;
 END IF;
 IF NOT EXISTS(SELECT 1 FROM loomplane_managed_resources WHERE org_id=NEW.org_id AND cloud_id=NEW.cloud_id AND id=NEW.id AND kind='location' AND provider=NEW.provider AND account_id=NEW.account_id AND deleted_at IS NULL) THEN
 RAISE EXCEPTION 'location requires matching active resource identity' USING ERRCODE='23514'; END IF;
 IF TG_OP='UPDATE' AND (NEW.id<>OLD.id OR NEW.org_id<>OLD.org_id OR NEW.cloud_id<>OLD.cloud_id OR NEW.provider<>OLD.provider OR NEW.account_id<>OLD.account_id OR NEW.region<>OLD.region OR NEW.revision<>OLD.revision+1) THEN
 RAISE EXCEPTION 'location placement is immutable; review a migration instead' USING ERRCODE='23514'; END IF;
 IF NEW.body->>'id' IS DISTINCT FROM NEW.id OR NEW.body->>'org_id' IS DISTINCT FROM NEW.org_id OR NEW.body->>'cloud_id' IS DISTINCT FROM NEW.cloud_id OR NEW.body->>'name' IS DISTINCT FROM NEW.name OR NEW.body->>'provider' IS DISTINCT FROM NEW.provider OR NEW.body->>'account_id' IS DISTINCT FROM NEW.account_id OR NEW.body->>'connection_id' IS DISTINCT FROM NEW.connection_id OR NEW.body->>'region' IS DISTINCT FROM NEW.region OR (NEW.body->>'revision')::bigint IS DISTINCT FROM NEW.revision THEN
 RAISE EXCEPTION 'location body differs from its identity' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_cloud_location_guard BEFORE INSERT OR UPDATE OR DELETE ON loomplane_cloud_locations FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_location_guard();
CREATE FUNCTION loomplane_cloud_location_target_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE location loomplane_cloud_locations;resource loomplane_managed_resources;
BEGIN
 IF NEW.location_id IS NULL THEN RETURN NEW; END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
 SELECT * INTO location FROM loomplane_cloud_locations WHERE org_id=NEW.org_id AND cloud_id=NEW.cloud_id AND id=NEW.location_id;
 IF NOT FOUND THEN
 IF TG_OP='UPDATE' AND NEW.location_id IS NOT DISTINCT FROM OLD.location_id THEN RETURN NEW; END IF;
 RAISE EXCEPTION 'target requires classified Cloud location' USING ERRCODE='23514'; END IF;
 IF NEW.region<>location.region THEN RAISE EXCEPTION 'target and location regions differ' USING ERRCODE='23514'; END IF;
 IF NEW.managed_resource_id IS NOT NULL THEN
 SELECT * INTO resource FROM loomplane_managed_resources WHERE org_id=NEW.org_id AND cloud_id=NEW.cloud_id AND id=NEW.managed_resource_id;
 IF NOT FOUND OR resource.provider<>location.provider OR resource.account_id<>location.account_id OR coalesce(resource.body->'observation'->'ref'->>'region','')<>location.region THEN
 RAISE EXCEPTION 'target resource and location placement differ' USING ERRCODE='23514'; END IF;
 ELSE
 IF NEW.provider<>location.provider OR NEW.connection_id IS DISTINCT FROM location.connection_id OR NEW.region<>location.region THEN
 RAISE EXCEPTION 'target and location placement differ' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_cloud_location_target_guard BEFORE INSERT OR UPDATE ON loomplane_cloud_targets FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_location_target_guard();
CREATE FUNCTION loomplane_cloud_location_membership_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE location loomplane_cloud_locations;resource loomplane_managed_resources;
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
 SELECT * INTO location FROM loomplane_cloud_locations WHERE org_id=NEW.org_id AND id=NEW.source_id;
 IF NOT FOUND THEN RETURN NEW; END IF;
 SELECT * INTO resource FROM loomplane_managed_resources WHERE org_id=NEW.org_id AND id=NEW.destination_id;
 IF NOT FOUND OR resource.provider<>location.provider OR resource.account_id<>location.account_id OR coalesce(resource.body->'observation'->'ref'->>'region','')<>location.region THEN
 RAISE EXCEPTION 'resource and location placement differ' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_cloud_location_membership_guard BEFORE INSERT OR UPDATE ON loomplane_resource_relations FOR EACH ROW EXECUTE FUNCTION loomplane_cloud_location_membership_guard();
`
