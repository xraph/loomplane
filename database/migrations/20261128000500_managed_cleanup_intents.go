package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `
CREATE TABLE loomplane_managed_cleanup_intents (
 org_id text NOT NULL, plan_id text NOT NULL REFERENCES loomplane_plans(id), action_id text NOT NULL,
 plan_revision text NOT NULL, body jsonb NOT NULL, actor jsonb NOT NULL,
 schema_version integer NOT NULL DEFAULT 1, revision bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(org_id,plan_id,action_id));
CREATE FUNCTION loomplane_managed_cleanup_targets(p_org text, p_project text, p_environment text, p_service text, p_operation text)
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
 )
 SELECT coalesce(jsonb_agg(jsonb_build_object(
 'schema_version',1,
 'kind',CASE WHEN (p_operation='update' AND b.origin='current') OR (b.origin='retained' AND b.identity->>'external_id'=b.current_instance AND b.identity->>'connection_id'=b.current_connection) THEN 'retire_release_dependents' ELSE 'remove_release' END,
 'origin',b.origin,'parent_binding_id',b.parent_id,
 'binding',jsonb_build_object('id',b.identity->>'id','external_id',b.identity->>'external_id','plan_id',b.identity->>'plan_id','connection_id',b.identity->>'connection_id','revision',b.identity->>'revision','updated_at',b.identity->>'updated_at'),
 'scope',a.body,'authority_epoch',a.epoch,'authority_revision',a.revision,'revoked_at',a.revoked_at,
 'attachment_id',i.body->'input'->>'attachment_id','attachment_revision',rt.revision,
 'trust_revision',rt.body->>'trust_revision','connection_generation',cn.generation,
 'accepted',coalesce((SELECT jsonb_agg(jsonb_build_object('id',w.id,'epoch',w.epoch,'request',w.request,'outcome',w.outcome) ORDER BY w.id)
 FROM loomplane_managed_workload_issuances w WHERE w.org_id=a.org_id AND w.release_id=a.release_id AND w.outcome->>'State'='accepted'),'[]'::jsonb),
 'unresolved',coalesce((SELECT jsonb_agg(w.id ORDER BY w.id) FROM loomplane_managed_workload_issuances w WHERE w.org_id=a.org_id AND w.release_id=a.release_id AND w.outcome IS NULL),'[]'::jsonb),
 'identity_current',coalesce(b.identity->>'org_id'=b.org_id AND b.identity->>'project_id'=b.project_id AND b.identity->>'environment_id'=b.environment_id AND b.identity->>'name'=b.name AND a.epoch=(a.body->>'Epoch')::bigint AND cn.status='ready' AND cn.cloud='kubernetes' AND cn.details->>'namespace'=a.body->>'Namespace' AND cn.details->>'namespace_uid'=a.body->>'NamespaceUID' AND cn.details->>'cluster_ca'=a.body->>'ClusterCA'
 AND rt.deleted_at IS NULL AND rt.project_id=p_project AND rt.runtime_connection_id=cn.id AND rt.body->>'namespace_uid'=a.body->>'NamespaceUID' AND rt.body->>'trust_fingerprint'=a.body->>'ClusterCA' AND rt.body->>'trust_revision'=i.body->'input'->>'trust_revision',false)
 ) ORDER BY b.parent_id,b.origin,a.release_id,b.identity->>'id',b.identity->>'revision',b.identity->>'updated_at'),'[]'::jsonb) INTO targets
 FROM normalized b JOIN loomplane_managed_workload_authority a ON a.org_id=b.org_id AND a.body->>'Instance'=b.identity->>'external_id' AND a.body->>'Connection'=b.identity->>'connection_id' AND a.body->>'Plan'=b.identity->>'plan_id'
 AND a.body->>'Project'=b.project_id AND a.body->>'Environment'=b.environment_id AND a.body->>'Service'=b.name
 LEFT JOIN loomplane_cluster_workload_intents i ON i.org_id=a.org_id AND i.plan_id=a.body->>'Plan' AND i.service=b.name
 LEFT JOIN loomplane_runtime_attachments rt ON rt.org_id=a.org_id AND rt.id=i.body->'input'->>'attachment_id'
 LEFT JOIN loomplane_cloud_connections cn ON cn.org_id=a.org_id AND cn.id=b.identity->>'connection_id'
 ;
 RETURN targets;
END $$;
CREATE FUNCTION loomplane_managed_cleanup_approval() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE action jsonb; targets jsonb; actor jsonb;
BEGIN
 IF TG_OP='UPDATE' AND EXISTS(SELECT 1 FROM loomplane_managed_cleanup_intents WHERE org_id=OLD.org_id AND plan_id=OLD.id)
 AND (NEW.org_id,NEW.project_id,NEW.environment_id,NEW.revision,NEW.result,NEW.decided_by) IS DISTINCT FROM (OLD.org_id,OLD.project_id,OLD.environment_id,OLD.revision,OLD.result,OLD.decided_by)
 THEN RAISE EXCEPTION 'approved cleanup targets are immutable' USING ERRCODE='23514'; END IF;
 IF NEW.status<>'approved' OR (TG_OP='UPDATE' AND OLD.status='approved') THEN RETURN NEW; END IF;
 FOR action IN SELECT value FROM jsonb_array_elements(coalesce(NEW.result->'actions','[]')) LOOP
 IF action->>'subject'<>'service' OR action->>'op' NOT IN ('delete','replace','update') THEN
 IF coalesce(action->'cleanup_targets','[]'::jsonb)<>'[]'::jsonb THEN RAISE EXCEPTION 'invalid cleanup action' USING ERRCODE='23514'; END IF;
 CONTINUE;
 END IF;
 PERFORM 1 FROM loomplane_workload_attempts WHERE org_id=NEW.org_id AND environment_id=NEW.environment_id AND service=action->>'name' FOR SHARE;
 PERFORM 1 FROM loomplane_bindings WHERE org_id=NEW.org_id AND project_id=NEW.project_id AND environment_id=NEW.environment_id AND subject='service' AND name=action->>'name' FOR SHARE;
 PERFORM 1 FROM loomplane_managed_workload_authority WHERE org_id=NEW.org_id AND body->>'Environment'=NEW.environment_id AND body->>'Service'=action->>'name' FOR SHARE;
 PERFORM 1 FROM loomplane_managed_workload_issuances WHERE org_id=NEW.org_id AND scope->>'Environment'=NEW.environment_id AND scope->>'Service'=action->>'name' FOR SHARE;
 PERFORM 1 FROM loomplane_cloud_connections WHERE org_id=NEW.org_id AND id IN (SELECT body->>'Connection' FROM loomplane_managed_workload_authority WHERE org_id=NEW.org_id AND body->>'Environment'=NEW.environment_id AND body->>'Service'=action->>'name') FOR SHARE;
 PERFORM 1 FROM loomplane_runtime_attachments WHERE org_id=NEW.org_id AND project_id=NEW.project_id FOR SHARE;
 targets:=loomplane_managed_cleanup_targets(NEW.org_id,NEW.project_id,NEW.environment_id,action->>'name',action->>'op');
 IF targets IS DISTINCT FROM coalesce(action->'cleanup_targets','[]'::jsonb) THEN RAISE EXCEPTION 'cleanup target changed; review again' USING ERRCODE='23514'; END IF;
 IF targets='[]'::jsonb THEN CONTINUE; END IF;
 IF NOT NEW.executable OR NOT coalesce((NEW.result->>'executable')::boolean,false) OR EXISTS(SELECT 1 FROM jsonb_array_elements(targets) t WHERE NOT coalesce((t->>'identity_current')::boolean,false) OR t->'unresolved'<>'[]'::jsonb)
 THEN RAISE EXCEPTION 'cleanup target is unresolved or untrusted' USING ERRCODE='23514'; END IF;
 actor:=nullif(current_setting('loomplane.capacity_actor',true),'')::jsonb;
 IF actor IS NULL OR actor->>'OrgID' IS DISTINCT FROM NEW.org_id OR actor->>'UserID' IS DISTINCT FROM NEW.decided_by OR coalesce(actor->>'UserID','')='' OR coalesce((actor->>'ReadOnly')::boolean,true)
 THEN RAISE EXCEPTION 'cleanup needs authenticated approval' USING ERRCODE='23514'; END IF;
 INSERT INTO loomplane_managed_cleanup_intents(org_id,plan_id,action_id,plan_revision,body,actor) VALUES(NEW.org_id,NEW.id,action->>'id',NEW.revision,targets,actor);
 END LOOP;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_managed_cleanup_approval AFTER INSERT OR UPDATE ON loomplane_plans FOR EACH ROW EXECUTE FUNCTION loomplane_managed_cleanup_approval();
CREATE TRIGGER loomplane_managed_cleanup_immutable BEFORE UPDATE OR DELETE ON loomplane_managed_cleanup_intents FOR EACH ROW EXECUTE FUNCTION loomplane_capacity_saved_immutable();`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_managed_cleanup_intents) THEN RAISE EXCEPTION 'cleanup intent history must be retained'; END IF; END $$;
DROP TRIGGER loomplane_managed_cleanup_approval ON loomplane_plans;
DROP FUNCTION loomplane_managed_cleanup_approval();
DROP FUNCTION loomplane_managed_cleanup_targets(text,text,text,text,text);
DROP TABLE loomplane_managed_cleanup_intents;`)
		return err
	})
}
