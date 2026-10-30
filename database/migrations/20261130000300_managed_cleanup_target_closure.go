package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN EXECUTE replace(pg_get_functiondef('loomplane_managed_cleanup_targets(text,text,text,text,text)'::regprocedure),'loomplane_managed_cleanup_targets','loomplane_managed_cleanup_targets_binding_only'); END $$;
CREATE OR REPLACE FUNCTION loomplane_managed_cleanup_targets(p_org text, p_project text, p_environment text, p_service text, p_operation text)
RETURNS jsonb LANGUAGE plpgsql VOLATILE AS $$
DECLARE targets jsonb;
BEGIN
 WITH bindings AS (
 SELECT b.id AS parent_id, b.org_id, b.project_id, b.environment_id, b.name, b.external_id AS current_instance, b.connection_id AS current_connection, 'current'::text AS origin, to_jsonb(b) AS identity
 FROM loomplane_bindings b WHERE b.org_id=p_org AND b.project_id=p_project AND b.environment_id=p_environment AND b.subject='service' AND b.name=p_service
 UNION
 SELECT b.id, b.org_id, b.project_id, b.environment_id, b.name, b.external_id, b.connection_id, 'retained', retained
 FROM loomplane_bindings b CROSS JOIN LATERAL jsonb_array_elements(b.replaced) retained
 WHERE b.org_id=p_org AND b.project_id=p_project AND b.environment_id=p_environment AND b.subject='service' AND b.name=p_service
 ), normalized AS (
 SELECT DISTINCT parent_id, org_id, project_id, environment_id, name, current_instance, current_connection, origin,
 jsonb_build_object('id',identity->>'id','org_id',identity->>'org_id','project_id',identity->>'project_id','environment_id',identity->>'environment_id','name',identity->>'name','external_id',identity->>'external_id','plan_id',identity->>'plan_id','connection_id',identity->>'connection_id','revision',identity->>'revision','updated_at',identity->>'updated_at') AS identity FROM bindings
 ), candidates AS (
 SELECT b.*, a.release_id,
 NOT EXISTS(SELECT 1 FROM normalized r WHERE r.org_id=a.org_id AND r.identity->>'external_id'=a.body->>'Instance' AND r.identity->>'connection_id'=a.body->>'Connection'
 AND (r.identity->>'org_id' IS DISTINCT FROM r.org_id OR r.identity->>'project_id' IS DISTINCT FROM r.project_id OR r.identity->>'environment_id' IS DISTINCT FROM r.environment_id OR r.identity->>'name' IS DISTINCT FROM r.name)) AS references_current,
 row_number() OVER (PARTITION BY a.org_id,a.release_id ORDER BY b.parent_id,b.origin,b.identity::text) AS rank,
 (SELECT jsonb_agg(jsonb_build_object('origin',r.origin,'parent_binding_id',r.parent_id,'binding',jsonb_build_object('id',r.identity->>'id','external_id',r.identity->>'external_id','plan_id',r.identity->>'plan_id','connection_id',r.identity->>'connection_id','revision',r.identity->>'revision','updated_at',r.identity->>'updated_at')) ORDER BY r.parent_id,r.origin,r.identity::text)
 FROM normalized r WHERE r.org_id=a.org_id AND r.identity->>'external_id'=a.body->>'Instance' AND r.identity->>'connection_id'=a.body->>'Connection') AS binding_refs
 FROM normalized b JOIN loomplane_managed_workload_authority a ON a.org_id=b.org_id AND a.body->>'Instance'=b.identity->>'external_id' AND a.body->>'Connection'=b.identity->>'connection_id'
 AND a.body->>'Project'=b.project_id AND a.body->>'Environment'=b.environment_id AND a.body->>'Service'=b.name
 )
 SELECT coalesce(jsonb_agg(jsonb_build_object(
 'schema_version',1,
 'kind',CASE WHEN (p_operation='update' AND (b.origin='current' OR a.body->>'Plan' IS DISTINCT FROM b.identity->>'plan_id')) OR (b.origin='retained' AND b.identity->>'external_id'=b.current_instance AND b.identity->>'connection_id'=b.current_connection) THEN 'retire_release_dependents' ELSE 'remove_release' END,
 'origin',b.origin,'parent_binding_id',b.parent_id,'binding_refs',b.binding_refs,
 'binding',jsonb_build_object('id',b.identity->>'id','external_id',b.identity->>'external_id','plan_id',b.identity->>'plan_id','connection_id',b.identity->>'connection_id','revision',b.identity->>'revision','updated_at',b.identity->>'updated_at'),
 'scope',a.body,'authority_epoch',a.epoch,'authority_revision',a.revision,'revoked_at',a.revoked_at,
 'attachment_id',i.body->'input'->>'attachment_id','attachment_revision',rt.revision,
 'trust_revision',rt.body->>'trust_revision','connection_generation',cn.generation,
 'accepted',coalesce((SELECT jsonb_agg(jsonb_build_object('id',w.id,'epoch',w.epoch,'request',w.request,'outcome',w.outcome) ORDER BY w.id)
 FROM loomplane_managed_workload_issuances w WHERE w.org_id=a.org_id AND w.release_id=a.release_id AND w.cleanup_grant_id IS NULL AND w.outcome->>'State'='accepted'),'[]'::jsonb),
 'unresolved',coalesce((SELECT jsonb_agg(w.id ORDER BY w.id) FROM loomplane_managed_workload_issuances w WHERE w.org_id=a.org_id AND w.release_id=a.release_id AND w.outcome IS NULL),'[]'::jsonb),
 'identity_current',coalesce(b.references_current AND a.body->>'Org'=a.org_id AND b.identity->>'org_id'=b.org_id AND b.identity->>'project_id'=b.project_id AND b.identity->>'environment_id'=b.environment_id AND b.identity->>'name'=b.name AND a.release_id=a.body->>'Release' AND a.epoch=(a.body->>'Epoch')::bigint AND i.body->'input'->>'connection_id'=a.body->>'Connection' AND i.body->'input'->>'action_id'=a.body->>'Action' AND cn.status='ready' AND cn.cloud='kubernetes' AND cn.details->>'namespace'=a.body->>'Namespace' AND cn.details->>'namespace_uid'=a.body->>'NamespaceUID' AND cn.details->>'cluster_ca'=a.body->>'ClusterCA'
 AND rt.deleted_at IS NULL AND rt.project_id=p_project AND rt.runtime_connection_id=cn.id AND rt.body->>'namespace_uid'=a.body->>'NamespaceUID' AND rt.body->>'trust_fingerprint'=a.body->>'ClusterCA' AND rt.body->>'trust_revision'=i.body->'input'->>'trust_revision',false)
 ) ORDER BY b.parent_id,b.origin,a.release_id,b.identity->>'id',b.identity->>'revision',b.identity->>'updated_at'),'[]'::jsonb) INTO targets
 FROM candidates b JOIN loomplane_managed_workload_authority a ON a.org_id=b.org_id AND a.release_id=b.release_id
 LEFT JOIN loomplane_cluster_workload_intents i ON i.org_id=a.org_id AND i.plan_id=a.body->>'Plan' AND i.service=b.name
 LEFT JOIN loomplane_runtime_attachments rt ON rt.org_id=a.org_id AND rt.id=i.body->'input'->>'attachment_id'
 LEFT JOIN loomplane_cloud_connections cn ON cn.org_id=a.org_id AND cn.id=b.identity->>'connection_id'
 WHERE b.rank=1;
 RETURN targets;
END $$;
`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_managed_cleanup_intents i CROSS JOIN LATERAL jsonb_array_elements(i.body) t WHERE t ? 'binding_refs') THEN RAISE EXCEPTION 'complete cleanup target history must be retained'; END IF; END $$;
DO $$ BEGIN EXECUTE replace(pg_get_functiondef('loomplane_managed_cleanup_targets_binding_only(text,text,text,text,text)'::regprocedure),'loomplane_managed_cleanup_targets_binding_only','loomplane_managed_cleanup_targets'); END $$;
DROP FUNCTION loomplane_managed_cleanup_targets_binding_only(text,text,text,text,text);`)
		return err
	})
}
