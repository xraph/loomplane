package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

var managedInstances = migrate.NewGroup("loomplane_managed_instances", migrate.DependsOn(GroupName, "ctrlplane"))

func managedInstancesGroup() *migrate.Group {
	g := migrate.NewGroup("loomplane_managed_instances", migrate.DependsOn(GroupName, "ctrlplane"))
	for _, m := range managedInstances.Migrations() {
		copyMigration := *m
		g.MustRegister(&copyMigration)
	}
	return g
}

func init() {
	managedInstances.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE TABLE loomplane_managed_retired_instances (
 org_id text NOT NULL,instance_id text NOT NULL,release_id text NOT NULL,
 connection_id text NOT NULL, runtime_ref text NOT NULL, runtime_files boolean NOT NULL,
 retired_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (org_id,instance_id),
 FOREIGN KEY(org_id,release_id) REFERENCES loomplane_managed_cleanup_tombstones(org_id,release_id)
);
CREATE TRIGGER loomplane_retain_retired_instance BEFORE UPDATE OR DELETE ON loomplane_managed_retired_instances FOR EACH ROW EXECUTE FUNCTION loomplane_retain_managed_cleanup_history();

CREATE FUNCTION loomplane_lock_managed_instance(p_org text,p_id text) RETURNS void LANGUAGE sql AS $$
 SELECT pg_advisory_xact_lock(hashtextextended('managed-instance/'||p_org||'/'||p_id,0))
$$;
CREATE FUNCTION loomplane_assert_unretired_instance(p_org text,p_id text) RETURNS void LANGUAGE plpgsql AS $$ BEGIN
 IF p_org IS NULL OR p_id IS NULL OR p_id='' THEN RETURN; END IF;
 PERFORM loomplane_lock_managed_instance(p_org,p_id);
 IF EXISTS(SELECT 1 FROM loomplane_managed_retired_instances WHERE org_id=p_org AND instance_id=p_id)
 THEN RAISE EXCEPTION 'managed instance was permanently retired' USING ERRCODE='23514'; END IF;
END $$;

CREATE FUNCTION loomplane_managed_update_current(text,text,text,text,text,text,boolean) RETURNS boolean LANGUAGE sql AS $$ SELECT EXISTS(SELECT true FROM cp_instances i JOIN cp_releases r ON r.tenant_id=i.tenant_id AND r.instance_id=i.id JOIN cp_deployments d ON d.tenant_id=r.tenant_id AND d.instance_id=r.instance_id AND d.release_id=r.id
	WHERE i.tenant_id=$1 AND i.id=$2 AND i.provider_name='kubernetes.'||$3 AND i.labels->>'loomplane.plan'=$4 AND i.labels->>'loomplane.managed-revision-base'=$5
	AND r.id=$6 AND r.notes='Managed workload '||$4||'/'||$5 AND r.active AND NOT EXISTS(SELECT 1 FROM cp_releases n WHERE n.tenant_id=r.tenant_id AND n.instance_id=r.instance_id AND n.version>r.version)
	AND ((NOT $7 AND d.state='running') OR ($7 AND d.state='succeeded' AND i.state='running' AND i.labels->>'loomplane.revision'=$5||'/'||r.id AND i.labels->>'loomplane.runtime.receipt.release'=r.id))
	FOR SHARE OF i,r,d) $$;
CREATE FUNCTION loomplane_guard_retired_managed_owner() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE instance_id text; scope jsonb; base text;
BEGIN
 IF TG_TABLE_NAME='loomplane_managed_workload_authority' THEN
  IF TG_OP='UPDATE' AND NEW.revoked_at IS NOT NULL AND NEW.body=OLD.body THEN RETURN NEW; END IF;
  scope:=NEW.body; instance_id:=scope->>'Instance';
 ELSIF TG_TABLE_NAME='loomplane_managed_workload_issuances' THEN
  IF NEW.cleanup_grant_id IS NOT NULL THEN RETURN NEW; END IF;
  scope:=NEW.scope; instance_id:=scope->>'Instance';
 ELSIF TG_TABLE_NAME='loomplane_workload_controllers' THEN
  instance_id:=NEW.instance_id;
 ELSE
  instance_id:=NEW.external_id;
 END IF;
 PERFORM loomplane_assert_unretired_instance(NEW.org_id,instance_id);
 IF scope IS NOT NULL THEN
  SELECT i.labels->>'loomplane.managed-revision-base' INTO base FROM cp_instances i WHERE i.tenant_id=NEW.org_id AND i.id=scope->>'Instance';
  IF base IS NOT NULL AND EXISTS(SELECT 1 FROM cp_deployments d WHERE d.tenant_id=NEW.org_id AND d.instance_id=scope->>'Instance') AND
  (scope->>'RuntimeRevision' IS DISTINCT FROM base||'/'||split_part(scope->>'RuntimeRevision','/',-1) OR NOT loomplane_managed_update_current(NEW.org_id,scope->>'Instance',scope->>'Connection',scope->>'Plan',base,split_part(scope->>'RuntimeRevision','/',-1),false))
  THEN RAISE EXCEPTION 'managed update needs its running controller release' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_guard_retired_authority BEFORE INSERT OR UPDATE ON loomplane_managed_workload_authority FOR EACH ROW EXECUTE FUNCTION loomplane_guard_retired_managed_owner();
CREATE TRIGGER loomplane_guard_retired_issue BEFORE INSERT ON loomplane_managed_workload_issuances FOR EACH ROW EXECUTE FUNCTION loomplane_guard_retired_managed_owner();
CREATE TRIGGER loomplane_guard_retired_controller BEFORE INSERT OR UPDATE ON loomplane_workload_controllers FOR EACH ROW EXECUTE FUNCTION loomplane_guard_retired_managed_owner();
CREATE TRIGGER loomplane_guard_retired_binding BEFORE INSERT OR UPDATE OF external_id,connection_id,plan_id,provider,config,org_id,project_id,environment_id,name,subject,revision,physical_id ON loomplane_bindings FOR EACH ROW EXECUTE FUNCTION loomplane_guard_retired_managed_owner();

CREATE FUNCTION loomplane_guard_retired_ctrlplane_instance() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='UPDATE' AND NEW.tenant_id=OLD.tenant_id AND NEW.id=OLD.id AND NEW.state IN ('destroying','failed') AND (to_jsonb(NEW)-'state'-'updated_at')=(to_jsonb(OLD)-'state'-'updated_at') THEN RETURN NEW; END IF;
 IF TG_OP='UPDATE' AND (OLD.tenant_id,OLD.id) IS DISTINCT FROM (NEW.tenant_id,NEW.id) THEN
  PERFORM loomplane_assert_unretired_instance(OLD.tenant_id,OLD.id);
 END IF;
 PERFORM loomplane_assert_unretired_instance(NEW.tenant_id,NEW.id);
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_guard_retired_ctrlplane_instance BEFORE INSERT OR UPDATE ON cp_instances FOR EACH ROW EXECUTE FUNCTION loomplane_guard_retired_ctrlplane_instance();
CREATE FUNCTION loomplane_guard_retired_ctrlplane_deployment() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_TABLE_NAME='cp_deployments' AND TG_OP='UPDATE' THEN
  IF (NEW.tenant_id,NEW.instance_id)=(OLD.tenant_id,OLD.instance_id) AND NEW.state IN ('succeeded','failed','rolled_back','cancelled') THEN RETURN NEW; END IF;
 END IF;
 PERFORM loomplane_assert_unretired_instance(NEW.tenant_id,NEW.instance_id);
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_guard_retired_ctrlplane_deployment BEFORE INSERT OR UPDATE OF instance_id,tenant_id,state ON cp_deployments FOR EACH ROW EXECUTE FUNCTION loomplane_guard_retired_ctrlplane_deployment();
CREATE TRIGGER loomplane_guard_retired_ctrlplane_release BEFORE INSERT OR UPDATE OF instance_id,tenant_id ON cp_releases FOR EACH ROW EXECUTE FUNCTION loomplane_guard_retired_ctrlplane_deployment();
CREATE FUNCTION loomplane_guard_reviewed_instance_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF loomplane_legacy_cleanup_blocked(OLD.tenant_id,OLD.labels->>'loomplane.project',OLD.labels->>'loomplane.environment',OLD.labels->>'loomplane.service',OLD.id)
 OR (EXISTS(SELECT 1 FROM loomplane_managed_cleanup_intents i JOIN loomplane_deployments d ON d.org_id=i.org_id AND d.plan_id=i.plan_id CROSS JOIN LATERAL jsonb_array_elements(i.body) t WHERE i.org_id=OLD.tenant_id AND d.status IN ('running','paused') AND t->'scope'->>'Instance'=OLD.id AND t->>'kind'='remove_release')
 AND NOT EXISTS(SELECT 1 FROM loomplane_managed_retired_instances r WHERE r.org_id=OLD.tenant_id AND r.instance_id=OLD.id))
 THEN RAISE EXCEPTION 'the reviewed instance cleanup must finish before metadata removal' USING ERRCODE='23514'; END IF;
 RETURN OLD;
END $$;
CREATE TRIGGER loomplane_guard_reviewed_instance_delete BEFORE DELETE ON cp_instances FOR EACH ROW EXECUTE FUNCTION loomplane_guard_reviewed_instance_delete();
CREATE FUNCTION loomplane_can_retire_managed_instance(p_org text,p_instance text,p_targets jsonb) RETURNS boolean LANGUAGE sql VOLATILE AS $$
 WITH owners AS (
  SELECT target, target->'scope' AS scope FROM jsonb_array_elements(p_targets) target
  WHERE target->'scope'->>'Org'=p_org AND target->'scope'->>'Instance'=p_instance
  AND target->>'kind' IN ('remove_release','abandon_failed_release')
 ), refs AS (
  SELECT o.scope,o.target->>'origin' AS origin,o.target->>'parent_binding_id' AS parent,o.target->'binding' AS b FROM owners o
  UNION SELECT o.scope,r->>'origin',r->>'parent_binding_id',r->'binding' FROM owners o CROSS JOIN LATERAL jsonb_array_elements(coalesce(o.target->'binding_refs','[]')) r
 ), live_bindings AS (
  SELECT b.id AS parent,b.org_id,b.project_id,b.environment_id,b.subject,b.name,'current' AS origin,to_jsonb(b) AS val FROM loomplane_bindings b
  WHERE b.org_id=p_org AND b.external_id=p_instance
  UNION ALL SELECT b.id,b.org_id,b.project_id,b.environment_id,b.subject,b.name,'retained',r FROM loomplane_bindings b CROSS JOIN LATERAL jsonb_array_elements(b.replaced) r
  WHERE b.org_id=p_org AND r->>'external_id'=p_instance
 )
 SELECT EXISTS(SELECT 1 FROM owners) AND NOT EXISTS(
 SELECT 1 FROM loomplane_managed_workload_authority a WHERE a.org_id=p_org AND a.body->>'Instance'=p_instance AND
 (a.revoked_at IS NULL OR NOT EXISTS(SELECT 1 FROM owners o WHERE o.scope=a.body AND o.scope->>'Release'=a.release_id)
 OR NOT EXISTS(SELECT 1 FROM loomplane_managed_cleanup_tombstones t WHERE t.org_id=a.org_id AND t.release_id=a.release_id AND t.body->>'instance_id'=p_instance)
 OR EXISTS(SELECT 1 FROM loomplane_managed_cleanup_current_drift d WHERE d.org_id=a.org_id AND d.release_id=a.release_id AND d.present)
 OR EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances w WHERE w.org_id=a.org_id AND w.release_id=a.release_id AND w.outcome IS NULL)))
 AND NOT EXISTS(SELECT 1 FROM live_bindings l WHERE NOT EXISTS(SELECT 1 FROM refs r WHERE r.parent=l.parent
 AND (r.origin=l.origin OR (r.origin='current' AND l.origin='retained'))
 AND r.scope->>'Project'=l.project_id AND r.scope->>'Environment'=l.environment_id AND r.scope->>'Service'=l.name AND l.subject='service'
 AND r.b->>'id'=l.val->>'id' AND r.b->>'external_id'=l.val->>'external_id' AND r.b->>'plan_id'=l.val->>'plan_id'
 AND r.b->>'connection_id'=l.val->>'connection_id' AND r.b->>'revision'=l.val->>'revision'
 AND (r.b->>'updated_at')::timestamptz=(l.val->>'updated_at')::timestamptz))
 AND NOT EXISTS(SELECT 1 FROM loomplane_workload_controllers c WHERE c.org_id=p_org AND c.instance_id=p_instance AND NOT EXISTS(
 SELECT 1 FROM owners o WHERE o.scope->>'Release'=c.body->>'release_id' AND o.scope->>'Instance'=c.instance_id AND o.scope->>'Connection'=c.connection_id AND o.scope->>'RuntimeRef'=c.runtime_ref AND o.scope->>'RuntimeRevision'=c.body->>'runtime_revision' AND o.scope->>'Project'=c.project_id AND o.scope->>'Environment'=c.environment_id AND o.scope->>'Service'=c.body->>'service_id'))
 AND NOT EXISTS(SELECT 1 FROM cp_deployments WHERE tenant_id=p_org AND instance_id=p_instance AND state NOT IN ('succeeded','failed','rolled_back','cancelled'))
 AND NOT EXISTS(SELECT 1 FROM (VALUES('cp_instances','loomplane_guard_retired_ctrlplane_instance'),('cp_deployments','loomplane_guard_retired_ctrlplane_deployment'),('cp_releases','loomplane_guard_retired_ctrlplane_release')) g(rel,trg)
 WHERE NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=g.rel::regclass AND tgname=g.trg AND tgenabled IN ('O','A')))
$$;
`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_managed_retired_instances) THEN RAISE EXCEPTION 'retired instance identities must be retained'; END IF; END $$;
DROP TRIGGER loomplane_guard_reviewed_instance_delete ON cp_instances;
DROP FUNCTION loomplane_guard_reviewed_instance_delete(),loomplane_can_retire_managed_instance(text,text,jsonb);
DROP TRIGGER loomplane_guard_retired_ctrlplane_release ON cp_releases;
DROP TRIGGER loomplane_guard_retired_ctrlplane_deployment ON cp_deployments;
DROP TRIGGER loomplane_guard_retired_ctrlplane_instance ON cp_instances;
DROP FUNCTION loomplane_guard_retired_ctrlplane_deployment(),loomplane_guard_retired_ctrlplane_instance();
DROP TRIGGER loomplane_guard_retired_authority ON loomplane_managed_workload_authority;
DROP TRIGGER loomplane_guard_retired_issue ON loomplane_managed_workload_issuances;
DROP TRIGGER loomplane_guard_retired_controller ON loomplane_workload_controllers;
DROP TRIGGER loomplane_guard_retired_binding ON loomplane_bindings;
DROP FUNCTION loomplane_guard_retired_managed_owner(),loomplane_assert_unretired_instance(text,text),loomplane_lock_managed_instance(text,text);
DROP FUNCTION loomplane_managed_update_current(text,text,text,text,text,text,boolean);
DROP TABLE loomplane_managed_retired_instances;`)
		return err
	})
}
