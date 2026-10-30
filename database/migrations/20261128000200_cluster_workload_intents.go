package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE TABLE loomplane_cluster_workload_intents (
 org_id text NOT NULL, plan_id text NOT NULL REFERENCES loomplane_plans(id), service text NOT NULL,
 plan_revision text NOT NULL, body jsonb NOT NULL, actor jsonb NOT NULL,
 schema_version integer NOT NULL DEFAULT 1, revision bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,plan_id,service));
 CREATE FUNCTION loomplane_cluster_workload_scope_current(p loomplane_plans,d jsonb) RETURNS boolean LANGUAGE SQL STABLE AS $$
 SELECT coalesce(d->'input'->>'org_id'=p.org_id AND d->'input'->>'project_id'=p.project_id AND d->'input'->>'environment_id'=p.environment_id AND d->'input'->>'plan_id'=p.id AND EXISTS(
 SELECT 1 FROM loomplane_cloud_targets t
 JOIN loomplane_cloud_profiles c ON c.org_id=t.org_id AND c.id=t.cloud_id
 JOIN loomplane_cloud_project_grants g ON g.org_id=t.org_id AND g.cloud_id=t.cloud_id AND g.project_id=p.project_id AND NOT g.revoked
 JOIN loomplane_cloud_assignments a ON a.org_id=t.org_id AND a.project_id=p.project_id AND a.environment_id=p.environment_id AND a.cloud_id=t.cloud_id
 JOIN loomplane_cloud_connections cn ON cn.org_id=t.org_id AND cn.id=t.connection_id AND cn.status='ready' AND cn.cloud='kubernetes'
 JOIN loomplane_runtime_attachments rt ON rt.org_id=t.org_id AND rt.id=d->'input'->>'attachment_id' AND rt.cluster_id=t.managed_resource_id AND rt.project_id=p.project_id AND rt.runtime_connection_id=t.connection_id AND rt.deleted_at IS NULL AND rt.body->>'trust_revision'=d->'input'->>'trust_revision' AND rt.body->>'namespace_uid'=cn.details->>'namespace_uid' AND rt.body->>'trust_fingerprint'=cn.details->>'cluster_ca'
 JOIN loomplane_managed_resources mr ON mr.org_id=t.org_id AND mr.id=t.managed_resource_id AND mr.deleted_at IS NULL AND mr.status NOT IN ('missing','identity_changed')
 WHERE t.org_id=p.org_id AND t.id=d->'input'->>'target_id' AND t.cloud_id=d->'input'->>'cloud_id' AND t.project_id=p.project_id AND t.managed_resource_id=d->'input'->>'resource_id' AND t.connection_id=d->'input'->>'connection_id'
 AND t.revision=(d->'placement'->>'target_revision')::bigint AND c.revision=(d->'placement'->>'cloud_revision')::bigint AND g.revision=(d->'placement'->>'grant_revision')::bigint AND a.revision=(d->'placement'->>'assignment_revision')::bigint AND cn.generation=(d->>'connection_generation')::bigint
 AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids))),false) $$;
 CREATE FUNCTION loomplane_cluster_workload_intent_guard() RETURNS trigger LANGUAGE plpgsql AS $$
 DECLARE a jsonb; d jsonb; actor jsonb;
 BEGIN
 IF TG_OP='UPDATE' AND EXISTS(SELECT 1 FROM loomplane_cluster_workload_intents WHERE org_id=OLD.org_id AND plan_id=OLD.id) AND (NEW.org_id,NEW.project_id,NEW.environment_id,NEW.revision,NEW.result,NEW.decided_by) IS DISTINCT FROM (OLD.org_id,OLD.project_id,OLD.environment_id,OLD.revision,OLD.result,OLD.decided_by) THEN RAISE EXCEPTION 'approved cluster workload is immutable' USING ERRCODE='23514'; END IF;
 IF NEW.status<>'approved' OR (TG_OP='UPDATE' AND OLD.status='approved') THEN RETURN NEW; END IF;
 FOR a IN SELECT value FROM jsonb_array_elements(coalesce(NEW.result->'actions','[]')) LOOP
 d:=a->'service'->'cluster_demand'; IF d IS NULL OR d='null'::jsonb THEN CONTINUE; END IF;
 IF coalesce(NEW.result->>'cloud_sync_id','')<>'' THEN
 IF NOT loomplane_accepted_pool_sync(NEW) THEN RAISE EXCEPTION 'cluster workload sync needs accepted snapshot' USING ERRCODE='23514'; END IF;
 SELECT snapshot->'actor' INTO actor FROM loomplane_cloud_syncs WHERE org_id=NEW.org_id AND id=NEW.result->>'cloud_sync_id';
 ELSE actor:=nullif(current_setting('loomplane.capacity_actor',true),'')::jsonb; END IF;
 IF actor IS NULL OR actor->>'OrgID' IS DISTINCT FROM NEW.org_id OR actor->>'UserID' IS DISTINCT FROM NEW.decided_by OR coalesce(actor->>'UserID','')='' OR coalesce((actor->>'ReadOnly')::boolean,true) THEN RAISE EXCEPTION 'cluster workload needs authenticated approval' USING ERRCODE='23514'; END IF;
 IF NOT NEW.executable OR NOT coalesce((NEW.result->>'executable')::boolean,false) OR NOT loomplane_cluster_workload_scope_current(NEW,d) OR d->>'schema_version' IS DISTINCT FROM '1' OR coalesce(d->>'mode','') NOT IN ('guaranteed','best_effort') OR a->>'provider' IS DISTINCT FROM 'kubernetes' OR a->>'subject' IS DISTINCT FROM 'service' OR coalesce(a->>'op','') NOT IN ('create','update','replace') OR d->'input'->>'action_id' IS DISTINCT FROM a->>'id' OR d->'input'->>'service' IS DISTINCT FROM a->>'name' OR d->'input'->>'connection_id' IS DISTINCT FROM a->>'connection_id' OR coalesce(d->'input'->>'review_digest','')='' THEN RAISE EXCEPTION 'invalid cluster workload approval' USING ERRCODE='23514'; END IF;
 INSERT INTO loomplane_cluster_workload_intents(org_id,plan_id,service,plan_revision,body,actor) VALUES(NEW.org_id,NEW.id,a->>'name',NEW.revision,d,actor);
 END LOOP; RETURN NEW; END $$;
 CREATE TRIGGER loomplane_cluster_workload_approval AFTER INSERT OR UPDATE ON loomplane_plans FOR EACH ROW EXECUTE FUNCTION loomplane_cluster_workload_intent_guard();
 CREATE TRIGGER loomplane_cluster_workload_saved_immutable BEFORE UPDATE OR DELETE ON loomplane_cluster_workload_intents FOR EACH ROW EXECUTE FUNCTION loomplane_capacity_saved_immutable();`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DROP TRIGGER loomplane_cluster_workload_approval ON loomplane_plans; DROP FUNCTION loomplane_cluster_workload_intent_guard(); DROP FUNCTION loomplane_cluster_workload_scope_current(loomplane_plans,jsonb); DROP TABLE loomplane_cluster_workload_intents;`)
		return err
	})
}
