package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `LOCK TABLE loomplane_managed_cleanup_intents,loomplane_deployment_workflow_runs,loomplane_deployment_workflow_participants,loomplane_deployments IN SHARE ROW EXCLUSIVE MODE;
CREATE TABLE loomplane_legacy_cleanup_runs(
 org_id text NOT NULL,plan_id text NOT NULL,workflow_id text NOT NULL,request jsonb,
 detected_at timestamptz NOT NULL DEFAULT clock_timestamp(),state text NOT NULL CHECK(state IN ('pending','uncertain','drained')),
 detail text NOT NULL DEFAULT '',PRIMARY KEY(org_id,plan_id,workflow_id)
);
CREATE FUNCTION loomplane_retain_cutover_history() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' OR (NEW.org_id,NEW.plan_id,NEW.workflow_id,NEW.request,NEW.detected_at) IS DISTINCT FROM (OLD.org_id,OLD.plan_id,OLD.workflow_id,OLD.request,OLD.detected_at) OR (OLD.state<>'pending' AND NEW.state<>OLD.state) THEN RAISE EXCEPTION 'historical cleanup evidence must be retained' USING ERRCODE='23514'; END IF; RETURN NEW;
END $$;
CREATE TRIGGER loomplane_retain_cutover_history BEFORE UPDATE OR DELETE ON loomplane_legacy_cleanup_runs FOR EACH ROW EXECUTE FUNCTION loomplane_retain_cutover_history();
CREATE INDEX loomplane_pending_legacy_cleanup ON loomplane_legacy_cleanup_runs(org_id,plan_id) WHERE state<>'drained';
INSERT INTO loomplane_legacy_cleanup_runs(org_id,plan_id,workflow_id,request,state,detail)
 SELECT DISTINCT i.org_id,i.plan_id,coalesce(r.workflow_id,''),r.request,
 CASE WHEN r.workflow_id IS NOT NULL THEN 'pending' WHEN EXISTS(SELECT 1 FROM loomplane_deployments d WHERE d.org_id=i.org_id AND d.plan_id=i.plan_id) THEN 'uncertain' ELSE 'drained' END,
 CASE WHEN r.workflow_id IS NULL THEN 'original workflow route unavailable' ELSE '' END
 FROM loomplane_managed_cleanup_intents i JOIN loomplane_plans p ON p.org_id=i.org_id AND p.id=i.plan_id AND p.required_runtime_protocol='managed-cleanup-journal-v1'
 LEFT JOIN loomplane_deployment_workflow_runs r ON r.org_id=i.org_id AND r.request->>'build_id'='deployment-v6'
 AND (r.plan_id=i.plan_id OR EXISTS(SELECT 1 FROM loomplane_deployment_workflow_participants w WHERE w.org_id=i.org_id AND w.plan_id=i.plan_id AND w.workflow_id=r.workflow_id));
UPDATE loomplane_deployments d SET status='paused', capacity_revision=capacity_revision+1,capacity_wait=jsonb_build_object('revision',capacity_revision+1,'blocks',jsonb_build_array(jsonb_build_object('reason','historical_managed_cleanup_unresolved','resource','workload','retryable',false)),'created_at',clock_timestamp(),'updated_at',clock_timestamp()),error='The historical managed cleanup needs cancellation and a fresh review.'
 WHERE d.status='running' AND EXISTS(SELECT 1 FROM loomplane_legacy_cleanup_runs q WHERE q.org_id=d.org_id AND q.plan_id=d.plan_id AND q.state<>'drained');
CREATE FUNCTION loomplane_legacy_cleanup_blocked(p_org text,p_project text,p_env text,p_service text,p_instance text) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM loomplane_legacy_cleanup_runs q JOIN loomplane_managed_cleanup_intents i ON i.org_id=q.org_id AND i.plan_id=q.plan_id CROSS JOIN LATERAL jsonb_array_elements(i.body) target
 WHERE q.org_id=p_org AND q.state<>'drained' AND ((target->'scope'->>'Project'=p_project AND target->'scope'->>'Environment'=p_env AND target->'scope'->>'Service'=p_service)
 OR (p_instance<>'' AND target->'scope'->>'Instance'=p_instance)))
$$;
CREATE FUNCTION loomplane_guard_cleanup_cutover() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p text; e text; svc text; instance text; body jsonb;
BEGIN
 IF TG_TABLE_NAME='loomplane_managed_workload_authority' THEN
  IF NEW.revoked_at IS NOT NULL THEN RETURN NEW; END IF;
  body:=NEW.body;
 ELSIF TG_TABLE_NAME='loomplane_managed_workload_issuances' THEN body:=NEW.scope;
 ELSIF TG_TABLE_NAME='loomplane_bindings' THEN p:=NEW.project_id;e:=NEW.environment_id;svc:=CASE WHEN NEW.subject='service' THEN NEW.name ELSE '' END;instance:=NEW.external_id;
 ELSE body:=NEW.body->'input';p:=body->>'project_id';e:=body->>'environment_id';svc:=NEW.service; END IF;
 IF p IS NULL THEN p:=body->>'Project';e:=body->>'Environment';svc:=body->>'Service';instance:=body->>'Instance'; END IF;
 IF loomplane_legacy_cleanup_blocked(NEW.org_id,p,e,svc,instance) THEN RAISE EXCEPTION 'the historical cleanup must be reconciled before starting new work' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_legacy_cleanup_adoption BEFORE INSERT OR UPDATE ON loomplane_managed_workload_authority FOR EACH ROW EXECUTE FUNCTION loomplane_guard_cleanup_cutover();
CREATE TRIGGER loomplane_legacy_cleanup_write BEFORE INSERT ON loomplane_managed_workload_issuances FOR EACH ROW EXECUTE FUNCTION loomplane_guard_cleanup_cutover();
CREATE TRIGGER loomplane_legacy_cleanup_binding BEFORE INSERT OR UPDATE OF plan_id,external_id,connection_id,provider,subject,name,config ON loomplane_bindings FOR EACH ROW EXECUTE FUNCTION loomplane_guard_cleanup_cutover();
CREATE TRIGGER loomplane_legacy_cleanup_hold BEFORE INSERT ON loomplane_cluster_workload_holds FOR EACH ROW EXECUTE FUNCTION loomplane_guard_cleanup_cutover();
CREATE FUNCTION loomplane_guard_legacy_cleanup_source() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_TABLE_NAME='loomplane_managed_cleanup_intents' THEN
  IF EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.body) t WHERE loomplane_legacy_cleanup_blocked(NEW.org_id,t->'scope'->>'Project',t->'scope'->>'Environment',t->'scope'->>'Service',t->'scope'->>'Instance')) AND NOT EXISTS(SELECT 1 FROM loomplane_managed_cleanup_intents i WHERE i.org_id=NEW.org_id AND i.plan_id=NEW.plan_id AND i.action_id=NEW.action_id AND i.body=NEW.body AND i.actor=NEW.actor)
  THEN RAISE EXCEPTION 'reconcile the historical cleanup before approving a new removal' USING ERRCODE='23514'; END IF;
  IF EXISTS(SELECT 1 FROM loomplane_plans p WHERE p.org_id=NEW.org_id AND p.id=NEW.plan_id AND p.required_runtime_protocol='managed-cleanup-journal-v1')
  AND NOT EXISTS(SELECT 1 FROM loomplane_managed_cleanup_intents i WHERE i.org_id=NEW.org_id AND i.plan_id=NEW.plan_id AND i.action_id=NEW.action_id AND i.body=NEW.body AND i.actor=NEW.actor)
  THEN RAISE EXCEPTION 'historical cleanup requires a fresh execution review' USING ERRCODE='23514'; END IF;
 ELSIF TG_TABLE_NAME='loomplane_deployments' THEN
  IF NEW.status='running' AND EXISTS(SELECT 1 FROM loomplane_legacy_cleanup_runs q WHERE q.org_id=NEW.org_id AND q.plan_id=NEW.plan_id)
  THEN RAISE EXCEPTION 'historical cleanup requires a fresh execution review' USING ERRCODE='23514'; END IF;
 ELSE
  IF NEW.request->>'build_id'='deployment-v6' AND EXISTS(SELECT 1 FROM loomplane_legacy_cleanup_runs q WHERE q.org_id=NEW.org_id AND (q.plan_id=NEW.plan_id OR q.workflow_id=NEW.workflow_id)) AND NOT EXISTS(
  SELECT 1 FROM loomplane_deployment_workflow_runs r WHERE r.org_id=NEW.org_id AND r.plan_id=NEW.plan_id AND r.workflow_id=NEW.workflow_id AND r.request=NEW.request)
  THEN RAISE EXCEPTION 'historical cleanup route cannot start new work' USING ERRCODE='23514'; END IF;
 END IF; RETURN NEW;
END $$;
CREATE TRIGGER loomplane_fresh_cleanup_intent BEFORE INSERT ON loomplane_managed_cleanup_intents FOR EACH ROW EXECUTE FUNCTION loomplane_guard_legacy_cleanup_source();
CREATE TRIGGER loomplane_fresh_cleanup_deployment BEFORE INSERT OR UPDATE OF status ON loomplane_deployments FOR EACH ROW EXECUTE FUNCTION loomplane_guard_legacy_cleanup_source();
CREATE TRIGGER loomplane_fresh_cleanup_route BEFORE INSERT ON loomplane_deployment_workflow_runs FOR EACH ROW EXECUTE FUNCTION loomplane_guard_legacy_cleanup_source();
CREATE FUNCTION loomplane_guard_legacy_cleanup_participant() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF EXISTS(SELECT 1 FROM loomplane_legacy_cleanup_runs q JOIN loomplane_deployment_workflow_runs r ON r.org_id=q.org_id AND r.workflow_id=NEW.workflow_id WHERE q.org_id=NEW.org_id AND q.plan_id=NEW.plan_id AND r.request->>'build_id'='deployment-v6')
 AND NOT EXISTS(SELECT 1 FROM loomplane_deployment_workflow_participants w WHERE w.org_id=NEW.org_id AND w.plan_id=NEW.plan_id AND w.workflow_id=NEW.workflow_id)
 THEN RAISE EXCEPTION 'historical cleanup requires its original workflow' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_legacy_cleanup_participant BEFORE INSERT ON loomplane_deployment_workflow_participants FOR EACH ROW EXECUTE FUNCTION loomplane_guard_legacy_cleanup_participant();
CREATE FUNCTION loomplane_guard_reviewed_retained_binding() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.replaced IS DISTINCT FROM OLD.replaced AND EXISTS(
 SELECT 1 FROM loomplane_managed_cleanup_intents i JOIN loomplane_deployments d ON d.org_id=i.org_id AND d.plan_id=i.plan_id CROSS JOIN LATERAL jsonb_array_elements(i.body) t
 CROSS JOIN LATERAL jsonb_array_elements(OLD.replaced) prior WHERE i.org_id=OLD.org_id AND d.status IN ('running','paused') AND t->>'parent_binding_id'=OLD.id AND prior->>'external_id'=t->'scope'->>'Instance' AND prior->>'connection_id'=t->'scope'->>'Connection' AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.replaced) after_ref WHERE after_ref=prior))
 AND NOT EXISTS(SELECT 1 FROM loomplane_deployments d JOIN loomplane_plans p ON p.org_id=d.org_id AND p.id=d.plan_id JOIN loomplane_managed_cleanup_intents i ON i.org_id=p.org_id AND i.plan_id=p.id
 WHERE d.org_id=OLD.org_id AND d.status='running' AND d.id=current_setting('loomplane.reviewed_retained_deployment',true) AND p.required_runtime_protocol='managed-cleanup-execution-v1' AND EXISTS(SELECT 1 FROM jsonb_array_elements(i.body) t WHERE t->>'parent_binding_id'=OLD.id))
 THEN RAISE EXCEPTION 'managed cleanup requires its reviewed retained binding' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_reviewed_retained_binding BEFORE UPDATE OF replaced ON loomplane_bindings FOR EACH ROW EXECUTE FUNCTION loomplane_guard_reviewed_retained_binding();
CREATE FUNCTION loomplane_guard_cleanup_binding_removal() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE approval jsonb; matched boolean;
BEGIN
 SELECT EXISTS(SELECT 1 FROM loomplane_managed_cleanup_intents i JOIN loomplane_deployments d ON d.org_id=i.org_id AND d.plan_id=i.plan_id
 CROSS JOIN LATERAL jsonb_array_elements(i.body) t WHERE i.org_id=OLD.org_id AND d.status IN ('running','paused') AND (t->>'parent_binding_id'=OLD.id OR EXISTS(SELECT 1 FROM jsonb_array_elements(coalesce(t->'binding_refs','[]')) r WHERE r->>'parent_binding_id'=OLD.id))) INTO matched;
 IF NOT matched THEN RETURN OLD; END IF;
 approval:=nullif(current_setting('loomplane.reviewed_binding_delete',true),'')::jsonb;
 IF approval IS NULL OR NOT EXISTS(SELECT 1 FROM loomplane_managed_cleanup_intents i JOIN loomplane_deployments d ON d.org_id=i.org_id AND d.plan_id=i.plan_id JOIN loomplane_plans p ON p.org_id=i.org_id AND p.id=i.plan_id
 CROSS JOIN LATERAL jsonb_array_elements(i.body) t CROSS JOIN LATERAL (
 SELECT t->'mixed_current_binding' AS ref UNION ALL SELECT t->'binding' WHERE t->>'origin'='current' UNION ALL SELECT r->'binding' FROM jsonb_array_elements(coalesce(t->'binding_refs','[]')) r WHERE r->>'origin'='current') refs
 WHERE i.org_id=OLD.org_id AND i.plan_id=approval->>'plan' AND i.action_id=approval->>'action' AND d.id=approval->>'deployment' AND d.status='running' AND p.required_runtime_protocol='managed-cleanup-execution-v1'
 AND refs.ref->>'id'=OLD.id AND refs.ref->>'plan_id'=OLD.plan_id AND refs.ref->>'connection_id'=OLD.connection_id AND refs.ref->>'external_id'=OLD.external_id AND refs.ref->>'revision'=OLD.revision AND (refs.ref->>'updated_at')::timestamptz=OLD.updated_at)
 THEN RAISE EXCEPTION 'managed cleanup requires the exact reviewed binding' USING ERRCODE='23514'; END IF;
 RETURN OLD;
END $$;
CREATE TRIGGER loomplane_reviewed_binding_removal BEFORE DELETE ON loomplane_bindings FOR EACH ROW EXECUTE FUNCTION loomplane_guard_cleanup_binding_removal();`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_legacy_cleanup_runs) THEN RAISE EXCEPTION 'historical cleanup quarantine evidence must be retained'; END IF; END $$;
DROP TRIGGER loomplane_reviewed_binding_removal ON loomplane_bindings;
DROP TRIGGER loomplane_reviewed_retained_binding ON loomplane_bindings;
DROP TRIGGER loomplane_legacy_cleanup_participant ON loomplane_deployment_workflow_participants;
DROP FUNCTION loomplane_guard_reviewed_retained_binding(),loomplane_guard_legacy_cleanup_participant();
DROP TRIGGER loomplane_fresh_cleanup_route ON loomplane_deployment_workflow_runs;
DROP TRIGGER loomplane_fresh_cleanup_deployment ON loomplane_deployments;
DROP TRIGGER loomplane_fresh_cleanup_intent ON loomplane_managed_cleanup_intents;
DROP TRIGGER loomplane_legacy_cleanup_hold ON loomplane_cluster_workload_holds;
DROP TRIGGER loomplane_legacy_cleanup_binding ON loomplane_bindings;
DROP TRIGGER loomplane_legacy_cleanup_write ON loomplane_managed_workload_issuances;
DROP TRIGGER loomplane_legacy_cleanup_adoption ON loomplane_managed_workload_authority;
DROP FUNCTION loomplane_guard_cleanup_binding_removal(),loomplane_guard_legacy_cleanup_source(),loomplane_guard_cleanup_cutover(),loomplane_legacy_cleanup_blocked(text,text,text,text,text);
DROP TABLE loomplane_legacy_cleanup_runs; DROP FUNCTION loomplane_retain_cutover_history();`)
		return err
	})
}
