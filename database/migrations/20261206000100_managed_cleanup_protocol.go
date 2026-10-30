package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN
 EXECUTE replace(pg_get_functiondef('loomplane_managed_protocol_plan()'::regprocedure),'loomplane_managed_protocol_plan','loomplane_managed_protocol_plan_v5');
 EXECUTE replace(pg_get_functiondef('loomplane_managed_protocol_route()'::regprocedure),'loomplane_managed_protocol_route','loomplane_managed_protocol_route_v5');
END $$;
ALTER TABLE loomplane_deployment_workflow_runs DROP CONSTRAINT loomplane_deployment_workflow_runs_build;
ALTER TABLE loomplane_deployment_workflow_runs ADD CONSTRAINT loomplane_deployment_workflow_runs_build CHECK (coalesce(request->>'build_id' IN ('deployment-v2','deployment-v3','deployment-v4','deployment-v5','deployment-v6'),false));
CREATE FUNCTION loomplane_managed_protocol_compatible(required text,build text) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT CASE required WHEN '' THEN true WHEN 'managed-workload-journal-v1' THEN coalesce(build IN ('deployment-v5','deployment-v6'),false) WHEN 'managed-cleanup-journal-v1' THEN build IS NOT DISTINCT FROM 'deployment-v6' ELSE false END
$$;
CREATE OR REPLACE FUNCTION loomplane_managed_protocol_plan() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE required text; managed boolean; cleanup boolean;
BEGIN
 required:=coalesce(NEW.result->>'required_runtime_protocol','');
 managed:=EXISTS(SELECT 1 FROM jsonb_array_elements(coalesce(NEW.result->'actions','[]'::jsonb)) a WHERE a->'service'->'cluster_demand' IS NOT NULL AND a->'service'->'cluster_demand'<>'null'::jsonb);
 cleanup:=EXISTS(SELECT 1 FROM jsonb_array_elements(coalesce(NEW.result->'actions','[]'::jsonb)) a WHERE jsonb_array_length(coalesce(a->'cleanup_targets','[]'::jsonb))>0);
 IF cleanup AND required<>'managed-cleanup-journal-v1' AND (TG_OP='INSERT' OR NEW.result IS DISTINCT FROM OLD.result) THEN RAISE EXCEPTION 'managed cleanup requires fresh review' USING ERRCODE='23514'; END IF;
 IF required NOT IN ('','managed-workload-journal-v1','managed-cleanup-journal-v1') OR (managed AND required='') THEN RAISE EXCEPTION 'managed workload protocol required' USING ERRCODE='23514'; END IF;
 IF TG_OP='UPDATE' AND OLD.required_runtime_protocol<>'' AND required<>OLD.required_runtime_protocol THEN RAISE EXCEPTION 'managed workload protocol is immutable' USING ERRCODE='23514'; END IF;
 IF required<>'' AND EXISTS(SELECT 1 FROM loomplane_deployment_workflow_runs r WHERE r.org_id=NEW.org_id AND (r.plan_id=NEW.id OR EXISTS(SELECT 1 FROM loomplane_deployment_workflow_participants p WHERE p.org_id=NEW.org_id AND p.plan_id=NEW.id AND p.workflow_id=r.workflow_id)) AND NOT loomplane_managed_protocol_compatible(required,r.request->>'build_id')) THEN RAISE EXCEPTION 'managed workload requires a compatible workflow; review again' USING ERRCODE='23514'; END IF;
 NEW.required_runtime_protocol:=required;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION loomplane_managed_protocol_route() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE required text; build text; participant record; route_org text;
BEGIN
 SELECT required_runtime_protocol INTO required FROM loomplane_plans WHERE org_id=NEW.org_id AND id=NEW.plan_id FOR SHARE;
 IF TG_TABLE_NAME='loomplane_deployment_workflow_participants' THEN
 SELECT org_id,request->>'build_id' INTO route_org,build FROM loomplane_deployment_workflow_runs WHERE workflow_id=NEW.workflow_id FOR SHARE;
 IF route_org IS DISTINCT FROM NEW.org_id THEN RAISE EXCEPTION 'workflow organization mismatch' USING ERRCODE='23514'; END IF;
 ELSE
 build:=NEW.request->>'build_id';
 IF TG_OP='UPDATE' AND (NEW.org_id,NEW.plan_id,NEW.workflow_id,NEW.request) IS DISTINCT FROM (OLD.org_id,OLD.plan_id,OLD.workflow_id,OLD.request) THEN RAISE EXCEPTION 'workflow identity is immutable' USING ERRCODE='23514'; END IF;
 FOR participant IN SELECT p.required_runtime_protocol FROM loomplane_plans p JOIN loomplane_deployment_workflow_participants w ON w.org_id=p.org_id AND w.plan_id=p.id WHERE w.workflow_id=NEW.workflow_id FOR SHARE OF p LOOP
 IF NOT loomplane_managed_protocol_compatible(participant.required_runtime_protocol,build) THEN RAISE EXCEPTION 'managed participant requires compatible workflow' USING ERRCODE='23514'; END IF;
 END LOOP;
 END IF;
 IF NOT loomplane_managed_protocol_compatible(coalesce(required,''),build) THEN RAISE EXCEPTION 'managed workload requires compatible workflow' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION loomplane_managed_participant_identity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' OR (NEW.org_id,NEW.plan_id,NEW.workflow_id) IS DISTINCT FROM (OLD.org_id,OLD.plan_id,OLD.workflow_id) THEN RAISE EXCEPTION 'workflow participation must be retained' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_managed_participant_identity BEFORE UPDATE OR DELETE ON loomplane_deployment_workflow_participants FOR EACH ROW EXECUTE FUNCTION loomplane_managed_participant_identity();
`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_plans WHERE required_runtime_protocol='managed-cleanup-journal-v1') OR EXISTS(SELECT 1 FROM loomplane_deployment_workflow_runs WHERE request->>'build_id'='deployment-v6') THEN RAISE EXCEPTION 'managed cleanup protocol history must be retained'; END IF;
 EXECUTE replace(pg_get_functiondef('loomplane_managed_protocol_plan_v5()'::regprocedure),'loomplane_managed_protocol_plan_v5','loomplane_managed_protocol_plan');
 EXECUTE replace(pg_get_functiondef('loomplane_managed_protocol_route_v5()'::regprocedure),'loomplane_managed_protocol_route_v5','loomplane_managed_protocol_route');
END $$;
DROP FUNCTION loomplane_managed_protocol_plan_v5();
DROP FUNCTION loomplane_managed_protocol_route_v5();
DROP FUNCTION loomplane_managed_protocol_compatible(text,text);
DROP TRIGGER loomplane_managed_participant_identity ON loomplane_deployment_workflow_participants;
DROP FUNCTION loomplane_managed_participant_identity();
ALTER TABLE loomplane_deployment_workflow_runs DROP CONSTRAINT loomplane_deployment_workflow_runs_build;
ALTER TABLE loomplane_deployment_workflow_runs ADD CONSTRAINT loomplane_deployment_workflow_runs_build CHECK (coalesce(request->>'build_id' IN ('deployment-v2','deployment-v3','deployment-v4','deployment-v5'),false));
`)
		return err
	})
}
