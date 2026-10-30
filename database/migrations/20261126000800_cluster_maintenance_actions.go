package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, clusterMaintenanceActionRepair)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DROP TRIGGER IF EXISTS loomplane_cluster_deployment_admission ON loomplane_deployments;`+clusterMaintenanceAdmissionSchema)
		return err
	})
}

const clusterMaintenanceActionRepair = `CREATE OR REPLACE FUNCTION loomplane_plan_uses_cluster(org text, plan text, cluster text) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(
 SELECT 1 FROM loomplane_plans p
 JOIN loomplane_runtime_attachments a ON a.org_id=p.org_id AND a.project_id=p.project_id
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(p.result->'actions')='array' THEN p.result->'actions' ELSE '[]'::jsonb END) action
 WHERE p.org_id=org AND p.id=plan AND a.cluster_id=cluster AND a.deleted_at IS NULL
 AND (action->>'op' IS DISTINCT FROM 'keep' OR action->>'adopt_cloud_placement'='true')
 AND (coalesce(nullif(action->>'connection_id',''),p.result->'connections'->>(action->>'provider'))=a.runtime_connection_id
 OR action->'service'->'cloud_placement'->>'connection_id'=a.runtime_connection_id
 OR action->'resource'->'cloud_placement'->>'connection_id'=a.runtime_connection_id
 OR EXISTS(SELECT 1 FROM loomplane_bindings b WHERE b.org_id=p.org_id AND b.project_id=p.project_id AND b.environment_id=p.environment_id AND b.subject=action->>'subject' AND b.name=action->>'name' AND b.connection_id=a.runtime_connection_id)))
$$;
`
