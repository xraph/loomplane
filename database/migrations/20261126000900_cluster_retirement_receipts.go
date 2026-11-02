package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, clusterRetirementSchema)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_cluster_retirement_receipts) THEN RAISE EXCEPTION 'retirement receipts must be retained'; END IF; END $$;
 DROP TRIGGER IF EXISTS loomplane_cluster_retiring_use ON loomplane_connection_uses;
 DROP TRIGGER IF EXISTS loomplane_cluster_retiring_attachment ON loomplane_runtime_attachments;
 DROP FUNCTION IF EXISTS loomplane_cluster_retiring_use();
 DROP FUNCTION IF EXISTS loomplane_cluster_retiring_attachment();
 DROP TABLE IF EXISTS loomplane_cluster_retirement_receipts;`)
		return err
	})
}

const clusterRetirementSchema = `
CREATE TABLE loomplane_cluster_retirement_receipts (
 org_id text NOT NULL, cloud_id text NOT NULL, operation_id text NOT NULL, resource_id text NOT NULL,
 schema_version integer NOT NULL DEFAULT 1, revision bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), body jsonb NOT NULL,
 PRIMARY KEY(org_id,operation_id),
 FOREIGN KEY(org_id,operation_id) REFERENCES loomplane_cluster_operations(org_id,id),
 FOREIGN KEY(org_id,resource_id) REFERENCES loomplane_managed_resources(org_id,id)
);
CREATE FUNCTION loomplane_cluster_retiring_use() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
 IF EXISTS(SELECT 1 FROM loomplane_runtime_attachments a WHERE a.org_id=NEW.org_id AND a.runtime_connection_id=NEW.connection_id AND
 (a.deleted_at IS NOT NULL OR EXISTS(SELECT 1 FROM loomplane_cluster_operations o WHERE o.org_id=a.org_id AND o.body->'input'->>'resource_id'=a.cluster_id AND o.body->>'kind' IN ('retire-cluster','detach-runtime') AND o.status IN ('accepted','running','waiting_provider','needs_attention')))) THEN
 RAISE EXCEPTION 'cluster retirement blocks connection use' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_cluster_retiring_use BEFORE INSERT OR UPDATE ON loomplane_connection_uses FOR EACH ROW EXECUTE FUNCTION loomplane_cluster_retiring_use();
CREATE FUNCTION loomplane_cluster_retiring_attachment() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
 IF NEW.deleted_at IS NULL AND EXISTS(SELECT 1 FROM loomplane_cluster_operations o WHERE o.org_id=NEW.org_id AND o.body->'input'->>'resource_id'=NEW.cluster_id AND o.body->>'kind' IN ('retire-cluster','detach-runtime') AND o.status IN ('accepted','running','waiting_provider','needs_attention')) THEN
 RAISE EXCEPTION 'cluster retirement blocks attachment publication' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_cluster_retiring_attachment BEFORE INSERT OR UPDATE ON loomplane_runtime_attachments FOR EACH ROW EXECUTE FUNCTION loomplane_cluster_retiring_attachment();
`
