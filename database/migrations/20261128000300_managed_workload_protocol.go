package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `
ALTER TABLE loomplane_plans ADD COLUMN required_runtime_protocol text NOT NULL DEFAULT '';
ALTER TABLE loomplane_deployment_workflow_runs DROP CONSTRAINT loomplane_deployment_workflow_runs_build;
ALTER TABLE loomplane_deployment_workflow_runs ADD CONSTRAINT loomplane_deployment_workflow_runs_build CHECK (coalesce(request->>'build_id' IN ('deployment-v2','deployment-v3','deployment-v4','deployment-v5'),false));
CREATE FUNCTION loomplane_managed_protocol_plan() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE required text; managed boolean;
BEGIN
 required:=coalesce(NEW.result->>'required_runtime_protocol','');
 managed:=EXISTS(SELECT 1 FROM jsonb_array_elements(coalesce(NEW.result->'actions','[]'::jsonb)) a WHERE a->'service'->'cluster_demand' IS NOT NULL AND a->'service'->'cluster_demand'<>'null'::jsonb);
 IF required NOT IN ('','managed-workload-journal-v1') OR (managed AND required='') THEN RAISE EXCEPTION 'managed workload protocol required' USING ERRCODE='23514'; END IF;
 IF TG_OP='UPDATE' AND OLD.required_runtime_protocol<>'' AND required<>OLD.required_runtime_protocol THEN RAISE EXCEPTION 'managed workload protocol is immutable' USING ERRCODE='23514'; END IF;
 IF required<>'' AND EXISTS(SELECT 1 FROM loomplane_deployment_workflow_runs r WHERE r.org_id=NEW.org_id AND (r.plan_id=NEW.id OR EXISTS(SELECT 1 FROM loomplane_deployment_workflow_participants p WHERE p.org_id=NEW.org_id AND p.plan_id=NEW.id AND p.workflow_id=r.workflow_id)) AND r.request->>'build_id' IS DISTINCT FROM 'deployment-v5') THEN RAISE EXCEPTION 'managed workload requires deployment-v5; plan again' USING ERRCODE='23514'; END IF;
 NEW.required_runtime_protocol:=required;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_managed_protocol_plan BEFORE INSERT OR UPDATE ON loomplane_plans FOR EACH ROW EXECUTE FUNCTION loomplane_managed_protocol_plan();
CREATE FUNCTION loomplane_managed_protocol_route() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE required text; build text; participant record;
BEGIN
 SELECT required_runtime_protocol INTO required FROM loomplane_plans WHERE org_id=NEW.org_id AND id=NEW.plan_id FOR SHARE;
 IF TG_TABLE_NAME='loomplane_deployment_workflow_participants' THEN
 SELECT request->>'build_id' INTO build FROM loomplane_deployment_workflow_runs WHERE org_id=NEW.org_id AND workflow_id=NEW.workflow_id FOR SHARE;
 ELSE
 build:=NEW.request->>'build_id';
 FOR participant IN SELECT p.required_runtime_protocol FROM loomplane_plans p JOIN loomplane_deployment_workflow_participants w ON w.org_id=p.org_id AND w.plan_id=p.id WHERE w.workflow_id=NEW.workflow_id FOR SHARE OF p LOOP
 IF participant.required_runtime_protocol<>'' AND build IS DISTINCT FROM 'deployment-v5' THEN RAISE EXCEPTION 'managed participant requires deployment-v5' USING ERRCODE='23514'; END IF;
 END LOOP;
 END IF;
 IF coalesce(required,'')<>'' AND build IS DISTINCT FROM 'deployment-v5' THEN RAISE EXCEPTION 'managed workload requires deployment-v5' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_managed_protocol_route BEFORE INSERT OR UPDATE ON loomplane_deployment_workflow_runs FOR EACH ROW EXECUTE FUNCTION loomplane_managed_protocol_route();
CREATE TRIGGER loomplane_managed_protocol_participant BEFORE INSERT OR UPDATE ON loomplane_deployment_workflow_participants FOR EACH ROW EXECUTE FUNCTION loomplane_managed_protocol_route();`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_plans WHERE required_runtime_protocol<>'') OR EXISTS(SELECT 1 FROM loomplane_deployment_workflow_runs WHERE request->>'build_id'='deployment-v5') THEN RAISE EXCEPTION 'managed workload protocol history must be retained'; END IF; END $$;
DROP TRIGGER loomplane_managed_protocol_participant ON loomplane_deployment_workflow_participants;
DROP TRIGGER loomplane_managed_protocol_route ON loomplane_deployment_workflow_runs;
DROP FUNCTION loomplane_managed_protocol_route();
DROP TRIGGER loomplane_managed_protocol_plan ON loomplane_plans;
DROP FUNCTION loomplane_managed_protocol_plan();
ALTER TABLE loomplane_plans DROP COLUMN required_runtime_protocol;
ALTER TABLE loomplane_deployment_workflow_runs DROP CONSTRAINT loomplane_deployment_workflow_runs_build;
ALTER TABLE loomplane_deployment_workflow_runs ADD CONSTRAINT loomplane_deployment_workflow_runs_build CHECK (coalesce(request->>'build_id' IN ('deployment-v2','deployment-v3','deployment-v4'),false));`)
		return err
	})
}
