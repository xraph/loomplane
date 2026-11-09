package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, clusterRetirementAdmissionSchema)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM loomplane_runtime_attachments WHERE deleted_at IS NOT NULL) OR EXISTS(SELECT 1 FROM loomplane_cluster_operations WHERE body->>'kind' IN ('retire-cluster','detach-runtime') AND status IN ('accepted','running','waiting_provider','needs_attention')) THEN RAISE EXCEPTION 'retirement admission history must be retained'; END IF;
 END $$;
 DROP TRIGGER IF EXISTS loomplane_cluster_deployment_admission ON loomplane_deployments;
 DROP FUNCTION IF EXISTS loomplane_plan_uses_runtime(text,text,text,text);`+clusterMaintenanceAdmissionSchema+clusterMaintenanceActionRepair)
		return err
	})
}

const clusterRetirementAdmissionSchema = `
CREATE OR REPLACE FUNCTION loomplane_plan_uses_runtime(org text,plan text,project text,connection text) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(
 SELECT 1 FROM loomplane_plans p
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(p.result->'actions')='array' THEN p.result->'actions' ELSE '[]'::jsonb END) action
 WHERE p.org_id=org AND p.id=plan AND p.project_id=project
 AND (action->>'op' IS DISTINCT FROM 'keep' OR action->>'adopt_cloud_placement'='true')
 AND (coalesce(nullif(action->>'connection_id',''),p.result->'connections'->>(action->>'provider'))=connection
 OR action->'service'->'cloud_placement'->>'connection_id'=connection
 OR action->'resource'->'cloud_placement'->>'connection_id'=connection
 OR EXISTS(SELECT 1 FROM loomplane_bindings b WHERE b.org_id=p.org_id AND b.project_id=p.project_id AND b.environment_id=p.environment_id AND b.subject=action->>'subject' AND b.name=action->>'name' AND b.connection_id=connection)))
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
  IF EXISTS(
   SELECT 1 FROM loomplane_runtime_attachments a
   WHERE a.org_id=NEW.org_id AND a.project_id=NEW.project_id
   AND loomplane_plan_uses_runtime(NEW.org_id,NEW.plan_id,NEW.project_id,a.runtime_connection_id)
   AND (a.deleted_at IS NOT NULL OR EXISTS(
    SELECT 1 FROM loomplane_cluster_operations o WHERE o.org_id=a.org_id AND o.body->'input'->>'resource_id'=a.cluster_id
    AND o.body->>'kind' IN ('retire-cluster','detach-runtime') AND o.status IN ('accepted','running','waiting_provider','needs_attention'))))
  THEN RAISE EXCEPTION 'cluster retirement blocks deployment admission' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
`
