package migrations

var workloadAttemptSafetyUp = []string{
	`ALTER TABLE loomplane_workload_attempts ADD COLUMN effects_started BOOLEAN NOT NULL DEFAULT true`,
	`INSERT INTO loomplane_workload_attempts(org_id,project_id,environment_id,service,plan_id,deployment_id,generation,legacy_unfenced)
 SELECT DISTINCT ON(d.org_id,d.environment_id,a->>'name') d.org_id,d.project_id,d.environment_id,a->>'name',d.plan_id,d.id,0,true
 FROM loomplane_deployments d JOIN loomplane_plans p ON p.org_id=d.org_id AND p.id=d.plan_id
 CROSS JOIN LATERAL jsonb_array_elements(p.result->'actions') a
 WHERE NOT EXISTS(SELECT 1 FROM loomplane_pool_execution x WHERE x.org_id=d.org_id AND x.plan_id=d.plan_id AND x.service=a->>'name')
 AND NOT EXISTS(SELECT 1 FROM loomplane_workload_attempts known WHERE known.org_id=d.org_id AND known.environment_id=d.environment_id AND known.service=a->>'name' AND known.plan_id=d.plan_id AND known.deployment_id=d.id AND known.generation>0 AND NOT known.legacy_unfenced)
 AND d.status IN ('running','failed') AND a->>'provider'='server' AND a->>'op'<>'keep'
 AND NOT (d.status='failed' AND d.finished_at IS NOT NULL AND EXISTS(
 SELECT 1 FROM jsonb_array_elements(d.steps) step WHERE step->>'action_id'=a->>'id'
 AND step->>'status'='succeeded' AND step->>'finished_at' IS NOT NULL
 AND coalesce(step->'replaced','null'::jsonb)='null'::jsonb AND coalesce(step->'warnings','[]'::jsonb)='[]'::jsonb)
 AND EXISTS(
 SELECT 1 FROM loomplane_bindings b JOIN loomplane_deployments successor
 ON successor.org_id=b.org_id AND successor.project_id=b.project_id AND successor.environment_id=b.environment_id AND successor.plan_id=b.plan_id
 WHERE b.org_id=d.org_id AND b.project_id=d.project_id AND b.environment_id=d.environment_id
 AND b.subject='service' AND b.name=a->>'name' AND b.provider='server' AND b.external_id<>'' AND b.replaced='[]'::jsonb
 AND successor.status='succeeded' AND successor.started_at>d.finished_at))
 ORDER BY d.org_id,d.environment_id,a->>'name',d.started_at DESC,d.id DESC
 ON CONFLICT(org_id,environment_id,service) DO UPDATE SET legacy_unfenced=true,settled=false,
 generation=loomplane_workload_attempts.generation+1`,
}
