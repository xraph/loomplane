package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, clusterMaintenanceAdmissionSchema)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM loomplane_cluster_operations WHERE body->>'kind' IN ('cluster-upgrade','node-pool-create','node-pool-update','node-pool-delete','cluster-configure') AND status NOT IN ('preview','succeeded','failed','cancelled')) THEN RAISE EXCEPTION 'cluster maintenance must finish before downgrade'; END IF;
 END $$;
 DROP TRIGGER IF EXISTS loomplane_cluster_deployment_admission ON loomplane_deployments;
 DROP FUNCTION IF EXISTS loomplane_cluster_deployment_admission();
 DROP FUNCTION IF EXISTS loomplane_plan_uses_cluster(text,text,text);`)
		return err
	})
}

const clusterMaintenanceAdmissionSchema = `
CREATE OR REPLACE FUNCTION loomplane_plan_uses_cluster(org text, plan text, cluster text) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(
 SELECT 1 FROM loomplane_plans p
 JOIN loomplane_runtime_attachments a ON a.org_id=p.org_id AND a.project_id=p.project_id
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(p.result->'actions')='array' THEN p.result->'actions' ELSE '[]'::jsonb END) action
 WHERE p.org_id=org AND p.id=plan AND a.cluster_id=cluster AND a.deleted_at IS NULL
 AND (coalesce(nullif(action->>'connection_id',''),p.result->'connections'->>(action->>'provider'))=a.runtime_connection_id
 OR action->'service'->'cloud_placement'->>'connection_id'=a.runtime_connection_id
 OR action->'resource'->'cloud_placement'->>'connection_id'=a.runtime_connection_id
 OR EXISTS(SELECT 1 FROM loomplane_bindings b WHERE b.org_id=p.org_id AND b.project_id=p.project_id AND b.environment_id=p.environment_id AND b.subject=action->>'subject' AND b.name=action->>'name' AND b.connection_id=a.runtime_connection_id)))
$$;

CREATE OR REPLACE FUNCTION loomplane_cluster_deployment_admission() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status NOT IN ('succeeded','failed','cancelled') THEN
  PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
  IF EXISTS(
   SELECT 1 FROM loomplane_cluster_operations o
   JOIN loomplane_runtime_attachments a ON a.org_id=o.org_id AND a.cluster_id=o.body->'input'->>'resource_id'
   WHERE o.org_id=NEW.org_id AND a.project_id=NEW.project_id AND a.deleted_at IS NULL
   AND loomplane_plan_uses_cluster(NEW.org_id,NEW.plan_id,a.cluster_id)
   AND o.status IN ('accepted','running','waiting_provider','needs_attention')
   AND (o.body->>'kind' IN ('cluster-upgrade','node-pool-delete') OR coalesce(o.body->'input'->'maintenance'->>'drain_digest','')<>''))
  THEN RAISE EXCEPTION 'cluster maintenance blocks deployment admission' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_cluster_deployment_admission BEFORE INSERT OR UPDATE OF status ON loomplane_deployments FOR EACH ROW EXECUTE FUNCTION loomplane_cluster_deployment_admission();`
