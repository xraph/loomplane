package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

// Admission retains its original binding comparison. Execution also recognizes
// exact after-values written by this accepted compensation's sibling actions.
func init() {
	App.Add(
		func(ctx context.Context, db *migrate.DB) error {
			err := db.NewCreateIndex("loomplane_handoff_receipt_owners").IfNotExists().
				On("loomplane_route_operations").
				Columns("org_id", "project_id", "route_id").
				ColumnExpr("body->>'handoff_id'").
				Where("status<>'preview' AND body->>'kind' IN ('cutover','rollback')").
				Exec(ctx)
			if err != nil {
				return err
			}

			// loomplane_accepted_pool_sync without its last condition, which
			// compares every binding with the accepted snapshot.
			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_accepted_sync_identity(p loomplane_plans) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
	SELECT EXISTS(SELECT 1 FROM loomplane_cloud_syncs s CROSS JOIN LATERAL jsonb_array_elements(coalesce(nullif(s.snapshot->'environments','null'),'[]')) e
		WHERE s.org_id=p.org_id AND s.id=p.result->>'cloud_sync_id' AND s.status IN ('pending','running')
			AND e->>'environment_id'=p.environment_id AND coalesce(nullif(e->>'project_id',''),s.project_id)=p.project_id
			AND e->'plan'->>'org_id'=p.org_id AND e->'plan'->>'project_id'=p.project_id AND e->'plan'->>'environment_id'=p.environment_id
			AND e->'accepted_plan'->>'id'=p.id AND p.id=s.id||'.'||p.environment_id
			AND e->'accepted_plan'->>'revision'=p.revision AND e->'plan'->>'revision'=p.revision
			AND e->'plan'->>'environment'=p.environment AND (e->'accepted_plan'->'result'->>'executable')::boolean=p.executable
			AND e->'accepted_plan'->>'manifest_id'=p.manifest_id AND (e->'accepted_plan'->>'manifest_revision')::int=p.manifest_revision
			AND e->'accepted_plan'->'result'=p.result AND e->'plan'->'actions'=p.result->'actions'
			AND EXISTS(SELECT 1 FROM loomplane_environment_sync_guards g WHERE g.org_id=p.org_id AND g.project_id=p.project_id AND g.environment_id=p.environment_id AND g.sync_id=s.id)) $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_accepted_sync_identity: %w", err)
			}

			_, err = db.Exec(ctx, `CREATE OR REPLACE FUNCTION loomplane_compensation_execution(p loomplane_plans) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
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
	) $$`)
			if err != nil {
				return fmt.Errorf("create function loomplane_compensation_execution: %w", err)
			}

			_, err = db.Exec(ctx, mustReplaceSQL(retainedCompensationSQL, "loomplane_accepted_pool_sync(p)", "loomplane_compensation_execution(p)"))
			if err != nil {
				return fmt.Errorf("create function loomplane_retained_compensation: %w", err)
			}
			return nil
		},
		nil,
	)
}
