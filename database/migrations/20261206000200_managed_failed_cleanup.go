package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE TABLE loomplane_managed_failed_cleanups (
 id text NOT NULL, org_id text NOT NULL, deployment_id text NOT NULL, sequence bigint NOT NULL CHECK(sequence>0), predecessor_id text,
 review_digest text NOT NULL, actor jsonb NOT NULL, targets jsonb NOT NULL CHECK(jsonb_typeof(targets)='array' AND jsonb_array_length(targets)>0),
 schema_version integer NOT NULL DEFAULT 1 CHECK(schema_version=1), revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 required_protocol text NOT NULL DEFAULT 'managed-cleanup-journal-v1' CHECK(required_protocol='managed-cleanup-journal-v1'),
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','failed','succeeded','cancelled')), attempt integer NOT NULL DEFAULT 1 CHECK(attempt>0),
 delivered_attempt integer NOT NULL DEFAULT 0, error text NOT NULL DEFAULT '', cancelled_by jsonb,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), updated_at timestamptz NOT NULL DEFAULT clock_timestamp(), superseded_at timestamptz, revoked_at timestamptz,
 PRIMARY KEY(org_id,id), UNIQUE(org_id,deployment_id,sequence),
 FOREIGN KEY(deployment_id) REFERENCES loomplane_deployments(id), FOREIGN KEY(org_id,predecessor_id) REFERENCES loomplane_managed_failed_cleanups(org_id,id));
CREATE UNIQUE INDEX loomplane_managed_failed_cleanup_current ON loomplane_managed_failed_cleanups(org_id,deployment_id) WHERE superseded_at IS NULL;
CREATE FUNCTION loomplane_managed_failed_cleanup_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE authenticated jsonb;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'managed cleanup review history must be retained' USING ERRCODE='23514'; END IF;
 IF TG_OP='INSERT' THEN
 authenticated:=nullif(current_setting('loomplane.capacity_actor',true),'')::jsonb;
 IF authenticated IS DISTINCT FROM NEW.actor OR NEW.actor->>'OrgID' IS DISTINCT FROM NEW.org_id OR coalesce(NEW.actor->>'UserID','')='' OR coalesce((NEW.actor->>'ReadOnly')::boolean,false)
 OR NEW.targets IS DISTINCT FROM loomplane_managed_failed_cleanup_targets(NEW.org_id,NEW.deployment_id)
 OR NOT EXISTS(SELECT 1 FROM loomplane_deployments d WHERE d.org_id=NEW.org_id AND d.id=NEW.deployment_id AND d.status='cancelled') THEN RAISE EXCEPTION 'current authenticated cleanup actor required' USING ERRCODE='23514'; END IF;
 ELSE
 IF NEW.cancelled_by IS DISTINCT FROM OLD.cancelled_by THEN
 authenticated:=nullif(current_setting('loomplane.capacity_actor',true),'')::jsonb;
 IF OLD.cancelled_by IS NOT NULL OR NEW.status<>'cancelled' OR NEW.cancelled_by IS DISTINCT FROM authenticated OR NEW.cancelled_by->>'OrgID' IS DISTINCT FROM NEW.org_id OR coalesce(NEW.cancelled_by->>'UserID','')='' THEN RAISE EXCEPTION 'current cancellation actor required' USING ERRCODE='23514'; END IF;
 END IF;
 IF NEW.status='cancelled' AND (NEW.cancelled_by IS NULL OR NEW.revoked_at IS NULL) THEN RAISE EXCEPTION 'cancelled cleanup requires retained actor and revocation' USING ERRCODE='23514'; END IF;
 IF (NEW.id,NEW.org_id,NEW.deployment_id,NEW.sequence,NEW.predecessor_id,NEW.review_digest,NEW.actor,NEW.targets,NEW.schema_version,NEW.required_protocol,NEW.created_at) IS DISTINCT FROM
 (OLD.id,OLD.org_id,OLD.deployment_id,OLD.sequence,OLD.predecessor_id,OLD.review_digest,OLD.actor,OLD.targets,OLD.schema_version,OLD.required_protocol,OLD.created_at)
 OR NEW.revision<>OLD.revision+1 OR NEW.attempt NOT IN (OLD.attempt,OLD.attempt+1) OR (NEW.attempt<>OLD.attempt AND (OLD.status<>'failed' OR NEW.status<>'pending' OR OLD.revoked_at IS NOT NULL OR OLD.superseded_at IS NOT NULL)) OR (NEW.status='pending' AND (NEW.revoked_at IS NOT NULL OR NEW.superseded_at IS NOT NULL)) OR (OLD.status='succeeded' AND NEW.status<>OLD.status) OR (OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at) OR (OLD.superseded_at IS NOT NULL AND NEW.superseded_at IS DISTINCT FROM OLD.superseded_at)
 THEN RAISE EXCEPTION 'managed cleanup review identity is immutable' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_managed_failed_cleanup_guard BEFORE INSERT OR UPDATE OR DELETE ON loomplane_managed_failed_cleanups FOR EACH ROW EXECUTE FUNCTION loomplane_managed_failed_cleanup_guard();
CREATE FUNCTION loomplane_managed_abandoned_deployment_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF EXISTS(SELECT 1 FROM loomplane_managed_failed_cleanups c WHERE c.org_id=OLD.org_id AND c.deployment_id=OLD.id) AND
 (NEW.status<>'cancelled' OR NEW.steps<>OLD.steps OR NEW.retries<>OLD.retries OR NEW.plan_id<>OLD.plan_id OR NEW.org_id<>OLD.org_id OR NEW.environment_id<>OLD.environment_id) THEN RAISE EXCEPTION 'managed abandoned evidence is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_managed_abandoned_deployment_guard BEFORE UPDATE ON loomplane_deployments FOR EACH ROW EXECUTE FUNCTION loomplane_managed_abandoned_deployment_guard();
ALTER TABLE loomplane_managed_cleanup_grants ADD COLUMN failed_review_id text, ADD COLUMN execution_attempt integer;
ALTER TABLE loomplane_managed_cleanup_grants ADD CONSTRAINT loomplane_managed_grant_failed_review FOREIGN KEY(org_id,failed_review_id) REFERENCES loomplane_managed_failed_cleanups(org_id,id);
DO $$ DECLARE constraint_name text; BEGIN
 SELECT conname INTO constraint_name FROM pg_constraint WHERE conrelid='loomplane_managed_cleanup_grants'::regclass AND confrelid='loomplane_managed_cleanup_intents'::regclass;
 EXECUTE format('ALTER TABLE loomplane_managed_cleanup_grants DROP CONSTRAINT %I',constraint_name);
 SELECT conname INTO constraint_name FROM pg_constraint WHERE conrelid='loomplane_managed_cleanup_grants'::regclass AND contype='u';
 EXECUTE format('ALTER TABLE loomplane_managed_cleanup_grants DROP CONSTRAINT %I',constraint_name);
 EXECUTE replace(pg_get_functiondef('loomplane_managed_protocol_route()'::regprocedure),'loomplane_managed_protocol_route','loomplane_managed_protocol_route_normal');
END $$;
CREATE UNIQUE INDEX loomplane_managed_cleanup_normal_selection ON loomplane_managed_cleanup_grants(org_id,plan_id,action_id,deployment_id,target_index) WHERE failed_review_id IS NULL;
CREATE UNIQUE INDEX loomplane_managed_cleanup_failed_selection ON loomplane_managed_cleanup_grants(org_id,failed_review_id,execution_attempt,target_index) WHERE failed_review_id IS NOT NULL;
ALTER TABLE loomplane_managed_cleanup_grants ADD CONSTRAINT loomplane_managed_cleanup_source CHECK((failed_review_id IS NULL AND execution_attempt IS NULL) OR (failed_review_id IS NOT NULL AND execution_attempt IS NOT NULL AND execution_attempt>0));
CREATE FUNCTION loomplane_managed_cleanup_source_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='UPDATE' AND (NEW.failed_review_id,NEW.execution_attempt) IS DISTINCT FROM (OLD.failed_review_id,OLD.execution_attempt) THEN RAISE EXCEPTION 'cleanup source is immutable' USING ERRCODE='23514'; END IF;
 IF NEW.failed_review_id IS NULL THEN
 IF NOT EXISTS(SELECT 1 FROM loomplane_managed_cleanup_intents i WHERE i.org_id=NEW.org_id AND i.plan_id=NEW.plan_id AND i.action_id=NEW.action_id) THEN RAISE EXCEPTION 'normal cleanup source missing' USING ERRCODE='23514'; END IF;
 ELSE
 IF TG_OP='INSERT' AND NOT EXISTS(SELECT 1 FROM loomplane_managed_failed_cleanups r WHERE r.org_id=NEW.org_id AND r.id=NEW.failed_review_id AND r.attempt=NEW.execution_attempt AND r.status='pending' AND r.revoked_at IS NULL AND r.superseded_at IS NULL) THEN RAISE EXCEPTION 'current cleanup attempt required' USING ERRCODE='23514'; END IF;
 IF NOT EXISTS(SELECT 1 FROM loomplane_managed_failed_cleanups r WHERE r.org_id=NEW.org_id AND r.id=NEW.failed_review_id AND r.deployment_id=NEW.deployment_id AND r.targets->NEW.target_index->'scope'->>'Plan'=NEW.plan_id AND r.targets->NEW.target_index->'scope'->>'Action'=NEW.action_id AND r.targets->NEW.target_index->'scope'->>'Release'=NEW.release_id) THEN RAISE EXCEPTION 'managed failed cleanup source missing' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_managed_cleanup_source_guard BEFORE INSERT OR UPDATE ON loomplane_managed_cleanup_grants FOR EACH ROW EXECUTE FUNCTION loomplane_managed_cleanup_source_guard();
CREATE OR REPLACE FUNCTION loomplane_managed_protocol_route() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE required text; build text; participant record; route_org text; route_plan text; review record; cleanup_input jsonb;
BEGIN
 SELECT required_runtime_protocol INTO required FROM loomplane_plans WHERE org_id=NEW.org_id AND id=NEW.plan_id FOR SHARE;
 IF TG_TABLE_NAME='loomplane_deployment_workflow_participants' THEN
 SELECT org_id,plan_id,request->>'build_id' INTO route_org,route_plan,build FROM loomplane_deployment_workflow_runs WHERE workflow_id=NEW.workflow_id FOR SHARE;
 IF route_org IS DISTINCT FROM NEW.org_id THEN RAISE EXCEPTION 'workflow organization mismatch' USING ERRCODE='23514'; END IF;
 ELSE
 build:=NEW.request->>'build_id';
 IF TG_OP='UPDATE' AND (NEW.org_id,NEW.plan_id,NEW.workflow_id,NEW.request) IS DISTINCT FROM (OLD.org_id,OLD.plan_id,OLD.workflow_id,OLD.request) THEN RAISE EXCEPTION 'workflow identity is immutable' USING ERRCODE='23514'; END IF;
 FOR participant IN SELECT p.required_runtime_protocol FROM loomplane_plans p JOIN loomplane_deployment_workflow_participants w ON w.org_id=p.org_id AND w.plan_id=p.id WHERE w.workflow_id=NEW.workflow_id FOR SHARE OF p LOOP
 IF NOT loomplane_managed_protocol_compatible(participant.required_runtime_protocol,build) THEN RAISE EXCEPTION 'managed participant requires compatible workflow' USING ERRCODE='23514'; END IF;
 END LOOP;
 END IF;
 IF TG_TABLE_NAME='loomplane_deployment_workflow_runs' THEN
 IF NEW.request->>'workflow_type'='loomplane.managed-failed-cleanup.v1' AND NEW.plan_id NOT LIKE 'managed-failed-cleanup:%' THEN RAISE EXCEPTION 'managed cleanup workflow source required' USING ERRCODE='23514'; END IF;
 END IF;
 IF NEW.plan_id LIKE 'failed-cleanup:%' AND EXISTS(SELECT 1 FROM loomplane_managed_failed_cleanups r WHERE r.org_id=NEW.org_id AND 'failed-cleanup:'||r.deployment_id=NEW.plan_id) THEN RAISE EXCEPTION 'managed cleanup cannot use SQL workflow' USING ERRCODE='23514'; END IF;
 IF NEW.plan_id LIKE 'managed-failed-cleanup:%' THEN
 IF TG_TABLE_NAME='loomplane_deployment_workflow_participants' AND route_plan IS DISTINCT FROM NEW.plan_id THEN RAISE EXCEPTION 'managed cleanup participant source mismatch' USING ERRCODE='23514'; END IF;
 SELECT r.* INTO review FROM loomplane_managed_failed_cleanups r WHERE r.org_id=NEW.org_id AND 'managed-failed-cleanup:'||r.id=NEW.plan_id FOR SHARE;
 IF NOT FOUND OR build IS DISTINCT FROM 'deployment-v6' THEN RAISE EXCEPTION 'managed cleanup review route required' USING ERRCODE='23514'; END IF;
 IF TG_OP='INSERT' AND (review.status<>'pending' OR review.revoked_at IS NOT NULL OR review.superseded_at IS NOT NULL) THEN RAISE EXCEPTION 'current pending cleanup review required' USING ERRCODE='23514'; END IF;
 IF TG_TABLE_NAME='loomplane_deployment_workflow_runs' THEN
 cleanup_input:=convert_from(decode(NEW.request->>'input','base64'),'UTF8')::jsonb;
 IF (NEW.workflow_id IS DISTINCT FROM 'managed-failed-cleanup:'||review.id||':'||(cleanup_input->>'attempt') OR NEW.request->>'workflow_id' IS DISTINCT FROM NEW.workflow_id OR NEW.request->>'request_id' IS DISTINCT FROM NEW.workflow_id OR NEW.request->>'workflow_type' IS DISTINCT FROM 'loomplane.managed-failed-cleanup.v1' OR cleanup_input->>'review_id' IS DISTINCT FROM review.id OR cleanup_input->>'org_id' IS DISTINCT FROM review.org_id OR cleanup_input->>'deployment_id' IS DISTINCT FROM review.deployment_id OR coalesce((cleanup_input->>'attempt')::integer,0)<1 OR (TG_OP='INSERT' AND (cleanup_input->>'attempt')::integer IS DISTINCT FROM review.attempt)) THEN RAISE EXCEPTION 'managed cleanup workflow input mismatch' USING ERRCODE='23514'; END IF;
 END IF;
 END IF;
 IF NOT loomplane_managed_protocol_compatible(coalesce(required,''),build) THEN RAISE EXCEPTION 'managed workload requires compatible workflow' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION loomplane_managed_failed_cleanup_targets(p_org text,p_deployment text) RETURNS jsonb LANGUAGE sql VOLATILE AS $$
 SELECT coalesce(jsonb_agg(jsonb_build_object(
 'schema_version',1,'kind','abandon_failed_release','origin','failed_attempt','parent_binding_id','','binding',jsonb_build_object('id','','external_id','','plan_id','','connection_id','','revision','','updated_at',''),
 'scope',a.body,'authority_epoch',a.epoch,'authority_revision',a.revision,'revoked_at',a.revoked_at,
 'attachment_id',i.body->'input'->>'attachment_id','attachment_revision',rt.revision,'trust_revision',rt.body->>'trust_revision','connection_generation',cn.generation,
 'accepted',coalesce((SELECT jsonb_agg(jsonb_build_object('id',w.id,'epoch',w.epoch,'request',w.request,'outcome',w.outcome) ORDER BY w.id) FROM loomplane_managed_workload_issuances w WHERE w.org_id=a.org_id AND w.release_id=a.release_id AND w.cleanup_grant_id IS NULL AND w.outcome->>'State'='accepted'),'[]'::jsonb),
 'unresolved',coalesce((SELECT jsonb_agg(w.id ORDER BY w.id) FROM loomplane_managed_workload_issuances w WHERE w.org_id=a.org_id AND w.release_id=a.release_id AND w.outcome IS NULL),'[]'::jsonb),
 'identity_current',coalesce(a.body->>'Org'=a.org_id AND a.release_id=a.body->>'Release' AND a.epoch=(a.body->>'Epoch')::bigint AND a.body->>'Project'=d.project_id AND a.body->>'Environment'=d.environment_id AND a.body->>'Plan'=d.plan_id
 AND i.body->'input'->>'connection_id'=a.body->>'Connection' AND i.body->'input'->>'action_id'=a.body->>'Action'
 AND cn.status='ready' AND cn.cloud='kubernetes' AND cn.details->>'namespace'=a.body->>'Namespace' AND cn.details->>'namespace_uid'=a.body->>'NamespaceUID' AND cn.details->>'cluster_ca'=a.body->>'ClusterCA'
 AND rt.deleted_at IS NULL AND rt.project_id=d.project_id AND rt.runtime_connection_id=cn.id AND rt.body->>'namespace_uid'=a.body->>'NamespaceUID' AND rt.body->>'trust_fingerprint'=a.body->>'ClusterCA' AND rt.body->>'trust_revision'=i.body->'input'->>'trust_revision',false)
 ) ORDER BY a.release_id),'[]'::jsonb)
 FROM loomplane_managed_workload_authority a
 JOIN loomplane_deployments d ON d.org_id=a.org_id AND d.id=a.body->>'Deployment' AND d.status IN ('failed','cancelled')
 JOIN loomplane_plans p ON p.org_id=d.org_id AND p.id=d.plan_id AND p.status='approved'
 LEFT JOIN loomplane_cluster_workload_intents i ON i.org_id=a.org_id AND i.plan_id=a.body->>'Plan' AND i.service=a.body->>'Service'
 LEFT JOIN loomplane_runtime_attachments rt ON rt.org_id=a.org_id AND rt.id=i.body->'input'->>'attachment_id'
 LEFT JOIN loomplane_cloud_connections cn ON cn.org_id=a.org_id AND cn.id=a.body->>'Connection'
 WHERE a.org_id=p_org AND d.id=p_deployment
 AND EXISTS(SELECT 1 FROM jsonb_array_elements(p.result->'actions') action WHERE action->>'id'=a.body->>'Action' AND action->>'subject'='service' AND action->>'name'=a.body->>'Service' AND action->>'op' IN ('create','update','replace'))
 AND EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances w WHERE w.org_id=a.org_id AND w.release_id=a.release_id AND w.cleanup_grant_id IS NULL AND w.outcome->>'State'='accepted' AND w.scope-'Epoch'=a.body-'Epoch' AND w.epoch<=(a.body->>'Epoch')::bigint AND w.epoch=(w.scope->>'Epoch')::bigint)
$$;
`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_managed_failed_cleanups) THEN RAISE EXCEPTION 'managed failed cleanup history must be retained'; END IF;
 EXECUTE replace(pg_get_functiondef('loomplane_managed_protocol_route_normal()'::regprocedure),'loomplane_managed_protocol_route_normal','loomplane_managed_protocol_route');
END $$;
DROP FUNCTION loomplane_managed_protocol_route_normal();
DROP FUNCTION loomplane_managed_failed_cleanup_targets(text,text);
DROP TRIGGER loomplane_managed_cleanup_source_guard ON loomplane_managed_cleanup_grants;
DROP FUNCTION loomplane_managed_cleanup_source_guard();
DROP INDEX loomplane_managed_cleanup_normal_selection;
DROP INDEX loomplane_managed_cleanup_failed_selection;
ALTER TABLE loomplane_managed_cleanup_grants DROP COLUMN failed_review_id,DROP COLUMN execution_attempt;
ALTER TABLE loomplane_managed_cleanup_grants ADD UNIQUE(org_id,plan_id,action_id,deployment_id,target_index),ADD FOREIGN KEY(org_id,plan_id,action_id) REFERENCES loomplane_managed_cleanup_intents(org_id,plan_id,action_id);
DROP TRIGGER loomplane_managed_abandoned_deployment_guard ON loomplane_deployments;
DROP FUNCTION loomplane_managed_abandoned_deployment_guard();
DROP TABLE loomplane_managed_failed_cleanups;
DROP FUNCTION loomplane_managed_failed_cleanup_guard();
`)
		return err
	})
}
