package migrations

import "strings"

// Admission retains its original binding comparison. Execution also recognizes
// exact after-values written by this accepted compensation's sibling actions.
var cloudCompensationProgressUp = func() []string {
	identity, _, found := strings.Cut(sharedSyncAdmissionUp[0], "\n AND NOT EXISTS(SELECT 1 FROM loomplane_bindings")
	if !found {
		panic("accepted sync identity boundary missing")
	}
	identity = strings.Replace(identity, "loomplane_accepted_pool_sync", "loomplane_accepted_sync_identity", 1) + ") $$"
	out := []string{`CREATE INDEX IF NOT EXISTS loomplane_handoff_receipt_owners ON loomplane_route_operations(org_id,project_id,route_id,(body->>'handoff_id')) WHERE status<>'preview' AND body->>'kind' IN ('cutover','rollback')`, identity, `CREATE OR REPLACE FUNCTION loomplane_compensation_execution(p loomplane_plans) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT loomplane_accepted_sync_identity(p) AND EXISTS(
 SELECT 1 FROM loomplane_cloud_syncs s CROSS JOIN LATERAL jsonb_array_elements(s.snapshot->'environments') e
 WHERE s.org_id=p.org_id AND s.id=p.result->>'cloud_sync_id' AND coalesce(s.body->>'reverts','')<>'' AND e->>'environment_id'=p.environment_id
 AND NOT EXISTS(
 SELECT 1 FROM jsonb_array_elements(e->'bindings') original
 LEFT JOIN loomplane_bindings b ON b.org_id=p.org_id AND b.project_id=p.project_id AND b.environment_id=p.environment_id AND b.id=original->>'id'
 WHERE b.id IS NULL OR NOT (
 (to_jsonb(b)-'observed_at'-'drift_message'=original AND NOT EXISTS(SELECT 1 FROM loomplane_cloud_sync_bindings recorded WHERE recorded.org_id=p.org_id AND recorded.sync_id=s.id AND recorded.environment_id=p.environment_id AND recorded.binding_id=b.id))
 OR EXISTS(SELECT 1 FROM loomplane_cloud_sync_bindings checkpoint CROSS JOIN LATERAL jsonb_array_elements(p.result->'actions') a
 WHERE checkpoint.org_id=p.org_id AND checkpoint.sync_id=s.id AND checkpoint.environment_id=p.environment_id AND checkpoint.binding_id=b.id
 AND checkpoint.after_value=to_jsonb(b)-'observed_at'-'drift_message' AND b.plan_id=p.id
 AND a->>'subject'=b.subject AND a->>'name'=b.name AND original->>'subject'=b.subject AND original->>'name'=b.name
 AND a->'compensation'->'candidate'->>'id'=b.id AND a->'compensation'->'source'->>'id'=b.id
 AND (NOT coalesce((a->'compensation'->>'retained')::boolean,false) OR (
 loomplane_compensation_identity(to_jsonb(b))=loomplane_compensation_identity(a->'compensation'->'source')
 AND EXISTS(SELECT 1 FROM loomplane_workload_attempts w WHERE w.org_id=p.org_id AND w.project_id=p.project_id AND w.environment_id=p.environment_id AND w.service=b.name AND w.plan_id=p.id AND NOT w.legacy_unfenced)
 ))))
 )
 AND NOT EXISTS(SELECT 1 FROM loomplane_bindings b WHERE b.org_id=p.org_id AND b.project_id=p.project_id AND b.environment_id=p.environment_id AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(e->'bindings') original WHERE original->>'id'=b.id))
 ) $$`}
	for _, sql := range cloudCompensationUp {
		if strings.HasPrefix(sql, "CREATE OR REPLACE FUNCTION loomplane_retained_compensation(") {
			out = append(out, strings.Replace(sql, "loomplane_accepted_pool_sync(p)", "loomplane_compensation_execution(p)", 1))
		}
	}
	return out
}()
