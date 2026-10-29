package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, managedClustersSchema)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM loomplane_managed_resources WHERE deleted_at IS NULL) OR EXISTS(SELECT 1 FROM loomplane_runtime_attachments WHERE deleted_at IS NULL) OR EXISTS(SELECT 1 FROM loomplane_cluster_operations WHERE status NOT IN ('succeeded','failed','cancelled','preview')) THEN
 RAISE EXCEPTION 'managed cluster records must be retired before downgrade'; END IF;
 END $$;
 DROP TRIGGER IF EXISTS loomplane_managed_target_guard ON loomplane_cloud_targets;
 ALTER TABLE loomplane_cloud_targets DROP COLUMN IF EXISTS managed_resource_id, DROP COLUMN IF EXISTS location_id;
 DROP TABLE IF EXISTS loomplane_cluster_operation_steps,loomplane_cluster_operations,loomplane_runtime_attachments,loomplane_resource_relations,loomplane_managed_resources;
 DROP FUNCTION IF EXISTS loomplane_resource_relation_guard(),loomplane_managed_resource_guard(),loomplane_runtime_attachment_guard(),loomplane_managed_target_guard();`)
		return err
	})
}

const managedClustersSchema = `
CREATE TABLE IF NOT EXISTS loomplane_managed_resources (
 id text PRIMARY KEY, org_id text NOT NULL, cloud_id text,
 kind text NOT NULL CHECK(kind IN ('account','cluster','node_pool','attachment','target','location')),
 provider text NOT NULL, account_id text NOT NULL CHECK(account_id<>''), connection_id text,
 external_id text NOT NULL CHECK(external_id<>''), incarnation text NOT NULL CHECK(incarnation<>''),
 ownership text NOT NULL CHECK(ownership IN ('discovered','external','managed')),
 status text NOT NULL, revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 deleted_at timestamptz, body jsonb NOT NULL,
 UNIQUE(org_id,id), UNIQUE(org_id,cloud_id,id),
 UNIQUE(org_id,provider,account_id,kind,external_id),
 FOREIGN KEY(org_id,cloud_id) REFERENCES loomplane_cloud_profiles(org_id,id),
 FOREIGN KEY(org_id,connection_id) REFERENCES loomplane_cloud_connections(org_id,id),
 CHECK((ownership='discovered' AND cloud_id IS NULL) OR (ownership IN ('external','managed') AND cloud_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS loomplane_managed_resources_cloud_page ON loomplane_managed_resources(org_id,cloud_id,id);
CREATE TABLE IF NOT EXISTS loomplane_resource_relations (
 org_id text NOT NULL, source_id text NOT NULL, destination_id text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('owns','provisions','uses','membership')), revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 PRIMARY KEY(org_id,source_id,destination_id,kind), CHECK(source_id<>destination_id),
 FOREIGN KEY(org_id,source_id) REFERENCES loomplane_managed_resources(org_id,id),
 FOREIGN KEY(org_id,destination_id) REFERENCES loomplane_managed_resources(org_id,id)
);
CREATE OR REPLACE FUNCTION loomplane_resource_relation_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE source_kind text; destination_kind text; source_cloud text; destination_cloud text;
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
 SELECT kind,cloud_id INTO source_kind,source_cloud FROM loomplane_managed_resources WHERE org_id=NEW.org_id AND id=NEW.source_id;
 SELECT kind,cloud_id INTO destination_kind,destination_cloud FROM loomplane_managed_resources WHERE org_id=NEW.org_id AND id=NEW.destination_id;
 IF NOT ((source_kind='account' AND destination_kind='cluster') OR (source_kind='cluster' AND destination_kind IN ('node_pool','attachment')) OR (source_kind='attachment' AND destination_kind='target') OR (source_kind='location' AND destination_kind IN ('cluster','node_pool','attachment','target'))) THEN
 RAISE EXCEPTION 'unsupported resource relationship' USING ERRCODE='23514'; END IF;
 IF source_cloud IS NOT NULL AND destination_cloud IS DISTINCT FROM source_cloud THEN
 RAISE EXCEPTION 'relationship crosses owning Cloud' USING ERRCODE='23514'; END IF;
 IF EXISTS(WITH RECURSIVE descendants(id) AS (SELECT NEW.destination_id UNION SELECT r.destination_id FROM loomplane_resource_relations r JOIN descendants d ON r.source_id=d.id WHERE r.org_id=NEW.org_id) SELECT 1 FROM descendants WHERE id=NEW.source_id) THEN
 RAISE EXCEPTION 'resource relationship cycle' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS loomplane_resource_relation_guard ON loomplane_resource_relations;
CREATE TRIGGER loomplane_resource_relation_guard BEFORE INSERT OR UPDATE ON loomplane_resource_relations FOR EACH ROW EXECUTE FUNCTION loomplane_resource_relation_guard();
CREATE TABLE IF NOT EXISTS loomplane_runtime_attachments (
 id text PRIMARY KEY, org_id text NOT NULL, cloud_id text NOT NULL, cluster_id text NOT NULL, project_id text NOT NULL,
 namespace text NOT NULL CHECK(namespace<>''), namespace_uid text NOT NULL CHECK(namespace_uid<>''),
 service_account_uid text NOT NULL CHECK(service_account_uid<>''), runtime_connection_id text NOT NULL,
 endpoint_fingerprint text NOT NULL, trust_fingerprint text NOT NULL,
 revision bigint NOT NULL CHECK(revision>0), deleted_at timestamptz, body jsonb NOT NULL,
 UNIQUE(org_id,id), UNIQUE(org_id,cluster_id,namespace_uid),
 FOREIGN KEY(org_id,cloud_id,cluster_id) REFERENCES loomplane_managed_resources(org_id,cloud_id,id),
 FOREIGN KEY(org_id,project_id) REFERENCES loomplane_projects(org_id,id),
 FOREIGN KEY(org_id,runtime_connection_id) REFERENCES loomplane_cloud_connections(org_id,id)
);
CREATE TABLE IF NOT EXISTS loomplane_cluster_operations (
 id text PRIMARY KEY, org_id text NOT NULL, cloud_id text NOT NULL, resource_id text,
 status text NOT NULL CHECK(status IN ('preview','accepted','running','waiting_provider','needs_attention','succeeded','failed','cancelled')),
 revision bigint NOT NULL CHECK(revision>0), digest text NOT NULL, fencing_epoch bigint NOT NULL DEFAULT 0 CHECK(fencing_epoch>=0),
 body jsonb NOT NULL, principal jsonb NOT NULL, updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(org_id,id), FOREIGN KEY(org_id,cloud_id) REFERENCES loomplane_cloud_profiles(org_id,id),
 FOREIGN KEY(org_id,cloud_id,resource_id) REFERENCES loomplane_managed_resources(org_id,cloud_id,id)
);
CREATE INDEX IF NOT EXISTS loomplane_cluster_operations_pending ON loomplane_cluster_operations(updated_at,id) WHERE status IN ('accepted','running','waiting_provider');
CREATE INDEX IF NOT EXISTS loomplane_cluster_operations_cloud ON loomplane_cluster_operations(org_id,cloud_id,id);
CREATE TABLE IF NOT EXISTS loomplane_cluster_operation_steps (
 org_id text NOT NULL, operation_id text NOT NULL, name text NOT NULL,
 state text NOT NULL CHECK(state IN ('pending','attempted','observed','completed')), request_digest text NOT NULL CHECK(request_digest<>''), body jsonb NOT NULL,
 PRIMARY KEY(org_id,operation_id,name), FOREIGN KEY(org_id,operation_id) REFERENCES loomplane_cluster_operations(org_id,id)
);
ALTER TABLE loomplane_cloud_targets ADD COLUMN IF NOT EXISTS managed_resource_id text, ADD COLUMN IF NOT EXISTS location_id text;
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='loomplane_target_managed_resource') THEN
 ALTER TABLE loomplane_cloud_targets ADD CONSTRAINT loomplane_target_managed_resource FOREIGN KEY(org_id,cloud_id,managed_resource_id) REFERENCES loomplane_managed_resources(org_id,cloud_id,id);
 ALTER TABLE loomplane_cloud_targets ADD CONSTRAINT loomplane_target_location FOREIGN KEY(org_id,cloud_id,location_id) REFERENCES loomplane_managed_resources(org_id,cloud_id,id);
 END IF;
END $$;
CREATE OR REPLACE FUNCTION loomplane_managed_resource_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.id<>OLD.id OR NEW.org_id<>OLD.org_id OR NEW.provider<>OLD.provider OR NEW.account_id<>OLD.account_id OR NEW.kind<>OLD.kind OR NEW.external_id<>OLD.external_id OR NEW.incarnation<>OLD.incarnation OR (OLD.cloud_id IS NOT NULL AND NEW.cloud_id IS DISTINCT FROM OLD.cloud_id) OR OLD.deleted_at IS NOT NULL THEN
 RAISE EXCEPTION 'managed resource identity is immutable' USING ERRCODE='23514'; END IF;
 IF NEW.revision<>OLD.revision+1 THEN RAISE EXCEPTION 'managed resource revision must advance' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS loomplane_managed_resource_guard ON loomplane_managed_resources;
CREATE TRIGGER loomplane_managed_resource_guard BEFORE UPDATE ON loomplane_managed_resources FOR EACH ROW EXECUTE FUNCTION loomplane_managed_resource_guard();
CREATE OR REPLACE FUNCTION loomplane_runtime_attachment_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
 IF NOT EXISTS(SELECT 1 FROM loomplane_managed_resources WHERE org_id=NEW.org_id AND cloud_id=NEW.cloud_id AND id=NEW.cluster_id AND kind='cluster' AND deleted_at IS NULL) THEN
 RAISE EXCEPTION 'attachment requires an owned active cluster' USING ERRCODE='23514'; END IF;
 IF TG_OP='UPDATE' AND (NEW.org_id<>OLD.org_id OR NEW.cloud_id<>OLD.cloud_id OR NEW.cluster_id<>OLD.cluster_id OR NEW.project_id<>OLD.project_id OR NEW.namespace<>OLD.namespace OR NEW.namespace_uid<>OLD.namespace_uid OR NEW.service_account_uid<>OLD.service_account_uid OR NEW.runtime_connection_id<>OLD.runtime_connection_id OR OLD.deleted_at IS NOT NULL OR NEW.revision<>OLD.revision+1) THEN
 RAISE EXCEPTION 'attachment identity is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS loomplane_runtime_attachment_guard ON loomplane_runtime_attachments;
CREATE TRIGGER loomplane_runtime_attachment_guard BEFORE INSERT OR UPDATE ON loomplane_runtime_attachments FOR EACH ROW EXECUTE FUNCTION loomplane_runtime_attachment_guard();
CREATE OR REPLACE FUNCTION loomplane_managed_target_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.managed_resource_id IS NOT NULL THEN
 PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
 IF NOT EXISTS(SELECT 1 FROM loomplane_runtime_attachments a JOIN loomplane_managed_resources r ON r.org_id=a.org_id AND r.id=a.cluster_id WHERE a.org_id=NEW.org_id AND a.cloud_id=NEW.cloud_id AND a.cluster_id=NEW.managed_resource_id AND a.project_id=NEW.project_id AND a.runtime_connection_id=NEW.connection_id AND a.deleted_at IS NULL AND r.deleted_at IS NULL AND r.kind='cluster') THEN
 RAISE EXCEPTION 'target requires matching active runtime attachment' USING ERRCODE='23514'; END IF;
 END IF;
 IF NEW.location_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM loomplane_managed_resources WHERE org_id=NEW.org_id AND cloud_id=NEW.cloud_id AND id=NEW.location_id AND kind='location' AND deleted_at IS NULL) THEN
 RAISE EXCEPTION 'target requires an active Cloud location' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS loomplane_managed_target_guard ON loomplane_cloud_targets;
CREATE TRIGGER loomplane_managed_target_guard BEFORE INSERT OR UPDATE ON loomplane_cloud_targets FOR EACH ROW EXECUTE FUNCTION loomplane_managed_target_guard();`
