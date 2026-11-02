package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN
 EXECUTE replace(pg_get_functiondef('loomplane_managed_protocol_plan()'::regprocedure),'loomplane_managed_protocol_plan','loomplane_managed_protocol_plan_v6');
 EXECUTE replace(pg_get_functiondef('loomplane_managed_protocol_route()'::regprocedure),'loomplane_managed_protocol_route','loomplane_managed_protocol_route_v6');
 EXECUTE replace(pg_get_functiondef('loomplane_managed_protocol_compatible(text,text)'::regprocedure),'loomplane_managed_protocol_compatible','loomplane_managed_protocol_compatible_v6');
END $$;
ALTER TABLE loomplane_deployment_workflow_runs DROP CONSTRAINT loomplane_deployment_workflow_runs_build;
ALTER TABLE loomplane_deployment_workflow_runs ADD CONSTRAINT loomplane_deployment_workflow_runs_build CHECK(coalesce(request->>'build_id' IN ('deployment-v2','deployment-v3','deployment-v4','deployment-v5','deployment-v6','deployment-v7','runtime-access-v1'),false));
ALTER TABLE loomplane_managed_failed_cleanups DROP CONSTRAINT loomplane_managed_failed_cleanups_required_protocol_check;
ALTER TABLE loomplane_managed_failed_cleanups ADD CONSTRAINT loomplane_managed_failed_cleanups_required_protocol_check CHECK(required_protocol IN ('managed-cleanup-journal-v1','managed-cleanup-execution-v1'));
CREATE OR REPLACE FUNCTION loomplane_managed_protocol_compatible(required text,build text) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT CASE required WHEN '' THEN true WHEN 'managed-workload-journal-v1' THEN coalesce(build IN ('deployment-v5','deployment-v6','deployment-v7'),false) WHEN 'managed-cleanup-journal-v1' THEN build IS NOT DISTINCT FROM 'deployment-v6' WHEN 'managed-cleanup-execution-v1' THEN build IS NOT DISTINCT FROM 'deployment-v7' ELSE false END
$$;
DO $$ DECLARE definition text; BEGIN
 definition:=pg_get_functiondef('loomplane_managed_protocol_plan()'::regprocedure);
 definition:=replace(definition,$old$cleanup AND required<>'managed-cleanup-journal-v1'$old$,$new$cleanup AND required<>'managed-cleanup-execution-v1'$new$);
 definition:=replace(definition,$old$NOT IN ('','managed-workload-journal-v1','managed-cleanup-journal-v1')$old$,$new$NOT IN ('','managed-workload-journal-v1','managed-cleanup-journal-v1','managed-cleanup-execution-v1')$new$);
 EXECUTE definition;
 definition:=pg_get_functiondef('loomplane_managed_protocol_route()'::regprocedure);
 definition:=replace(definition,$old$IF NOT FOUND OR build IS DISTINCT FROM 'deployment-v6' THEN$old$,$new$IF NOT FOUND OR NOT loomplane_managed_protocol_compatible(review.required_protocol,build) THEN$new$);
 EXECUTE definition;
END $$;`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_plans WHERE required_runtime_protocol='managed-cleanup-execution-v1') OR EXISTS(SELECT 1 FROM loomplane_managed_failed_cleanups WHERE required_protocol='managed-cleanup-execution-v1') OR EXISTS(SELECT 1 FROM loomplane_deployment_workflow_runs WHERE request->>'build_id'='deployment-v7') THEN RAISE EXCEPTION 'cleanup execution history must be retained'; END IF;
 EXECUTE replace(pg_get_functiondef('loomplane_managed_protocol_plan_v6()'::regprocedure),'loomplane_managed_protocol_plan_v6','loomplane_managed_protocol_plan');
 EXECUTE replace(pg_get_functiondef('loomplane_managed_protocol_route_v6()'::regprocedure),'loomplane_managed_protocol_route_v6','loomplane_managed_protocol_route');
 EXECUTE replace(pg_get_functiondef('loomplane_managed_protocol_compatible_v6(text,text)'::regprocedure),'loomplane_managed_protocol_compatible_v6','loomplane_managed_protocol_compatible');
END $$;
DROP FUNCTION loomplane_managed_protocol_plan_v6(),loomplane_managed_protocol_route_v6(),loomplane_managed_protocol_compatible_v6(text,text);
ALTER TABLE loomplane_managed_failed_cleanups DROP CONSTRAINT loomplane_managed_failed_cleanups_required_protocol_check;
ALTER TABLE loomplane_managed_failed_cleanups ADD CONSTRAINT loomplane_managed_failed_cleanups_required_protocol_check CHECK(required_protocol='managed-cleanup-journal-v1');
ALTER TABLE loomplane_deployment_workflow_runs DROP CONSTRAINT loomplane_deployment_workflow_runs_build;
ALTER TABLE loomplane_deployment_workflow_runs ADD CONSTRAINT loomplane_deployment_workflow_runs_build CHECK(coalesce(request->>'build_id' IN ('deployment-v2','deployment-v3','deployment-v4','deployment-v5','deployment-v6','runtime-access-v1'),false));`)
		return err
	})
}
