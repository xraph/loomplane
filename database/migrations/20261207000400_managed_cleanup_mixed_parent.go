package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `ALTER FUNCTION loomplane_managed_cleanup_targets(text,text,text,text,text) RENAME TO loomplane_managed_cleanup_targets_prior_parent;
CREATE FUNCTION loomplane_managed_cleanup_targets(p_org text,p_project text,p_environment text,p_service text,p_operation text)
RETURNS jsonb LANGUAGE sql STABLE AS $$
 WITH review AS (SELECT loomplane_managed_cleanup_targets_prior_parent(p_org,p_project,p_environment,p_service,p_operation) AS targets),
 elements AS (SELECT value, ordinality FROM review CROSS JOIN LATERAL jsonb_array_elements(targets) WITH ORDINALITY),
 parent AS (SELECT b.id,b.org_id,b.project_id,b.environment_id,b.subject,b.name,b.provider,b.region,b.engine,b.external_id,b.physical_id,b.connection_id,b.plan_id,b.revision,b.updated_at,
 encode(sha256(convert_to(b.config::text,'UTF8')),'hex') AS config_hash
 FROM loomplane_bindings b WHERE b.org_id=p_org AND b.project_id=p_project AND b.environment_id=p_environment AND b.subject='service' AND b.name=p_service)
 SELECT CASE WHEN p_operation='delete' AND EXISTS(SELECT 1 FROM elements)
 AND NOT EXISTS(SELECT 1 FROM elements e WHERE e.value->>'origin'='current' OR EXISTS(SELECT 1 FROM jsonb_array_elements(coalesce(e.value->'binding_refs','[]'::jsonb)) r WHERE r->>'origin'='current'))
 THEN (SELECT jsonb_agg(CASE WHEN e.value->>'parent_binding_id'=b.id THEN e.value||jsonb_build_object('mixed_current_binding',to_jsonb(b)) ELSE e.value END ORDER BY e.ordinality) FROM elements e LEFT JOIN parent b ON b.id=e.value->>'parent_binding_id')
 ELSE (SELECT targets FROM review) END
$$;`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_managed_cleanup_intents i CROSS JOIN LATERAL jsonb_array_elements(i.body) t WHERE t ? 'mixed_current_binding') THEN RAISE EXCEPTION 'reviewed mixed cleanup binding must be retained'; END IF; END $$;
DROP FUNCTION loomplane_managed_cleanup_targets(text,text,text,text,text);
ALTER FUNCTION loomplane_managed_cleanup_targets_prior_parent(text,text,text,text,text) RENAME TO loomplane_managed_cleanup_targets;`)
		return err
	})
}
