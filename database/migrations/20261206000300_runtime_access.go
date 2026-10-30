package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE TABLE loomplane_runtime_access (
 org_id text NOT NULL, attachment_id text NOT NULL, revision bigint NOT NULL CHECK(revision>0),
 schema_version integer NOT NULL CHECK(schema_version=1), operation_id text NOT NULL,
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL, body jsonb NOT NULL,
 PRIMARY KEY(org_id,attachment_id,revision), UNIQUE(org_id,operation_id),
 FOREIGN KEY(org_id,attachment_id) REFERENCES loomplane_runtime_attachments(org_id,id),
 FOREIGN KEY(org_id,operation_id) REFERENCES loomplane_cluster_operations(org_id,id),
 CHECK(body->>'org_id'=org_id AND body->>'attachment_id'=attachment_id AND (body->>'revision')::bigint=revision));
CREATE FUNCTION loomplane_runtime_access_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE previous jsonb; expected bigint;
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'runtime access history is immutable' USING ERRCODE='23514'; END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
 SELECT coalesce(max(revision),0)+1 INTO expected FROM loomplane_runtime_access WHERE org_id=NEW.org_id AND attachment_id=NEW.attachment_id;
 IF NEW.revision<>expected OR NOT EXISTS(SELECT 1 FROM loomplane_runtime_attachments a WHERE a.org_id=NEW.org_id AND a.id=NEW.attachment_id AND a.deleted_at IS NULL AND a.namespace_uid=NEW.body->'input'->>'namespace_uid' AND a.service_account_uid=NEW.body->'input'->>'service_account_uid')
 OR NOT EXISTS(SELECT 1 FROM loomplane_cluster_operations o WHERE o.org_id=NEW.org_id AND o.id=NEW.operation_id AND o.status='succeeded' AND o.digest=NEW.body->'receipt'->>'request_digest'
 AND ((o.body->>'kind'='runtime-access-upgrade' AND NEW.body->'input'=o.body->'input'->'runtime_access' AND NEW.body->'receipt'=o.body->'runtime_access'->'receipt' AND o.body->'runtime_access'->>'attempted'='true')
 OR (o.body->'input'->'attachment'->'runtime_access'=NEW.body->'input'->'profile' AND NEW.body->'input'=o.body->'enrollment'->'state'->'access_input' AND NEW.body->'receipt'=o.body->'enrollment'->'state'->'access_receipt')))
 OR (NEW.body->'input'->'profile'->>'schema_version')::integer IS DISTINCT FROM 1 OR (NEW.body->'input'->'profile'->>'cleanup_dependents')::boolean IS DISTINCT FROM true
 THEN RAISE EXCEPTION 'qualified runtime access receipt required' USING ERRCODE='23514'; END IF;
 SELECT body INTO previous FROM loomplane_runtime_access WHERE org_id=NEW.org_id AND attachment_id=NEW.attachment_id ORDER BY revision DESC LIMIT 1;
 IF coalesce((previous->'input'->'profile'->>'hpa')::boolean,false) AND NOT coalesce((NEW.body->'input'->'profile'->>'hpa')::boolean,false) THEN RAISE EXCEPTION 'runtime access downgrade is unsupported' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_runtime_access_guard BEFORE INSERT OR UPDATE OR DELETE ON loomplane_runtime_access FOR EACH ROW EXECUTE FUNCTION loomplane_runtime_access_guard();
ALTER TABLE loomplane_deployment_workflow_runs DROP CONSTRAINT loomplane_deployment_workflow_runs_build;
ALTER TABLE loomplane_deployment_workflow_runs ADD CONSTRAINT loomplane_deployment_workflow_runs_build CHECK(coalesce(request->>'build_id' IN ('deployment-v2','deployment-v3','deployment-v4','deployment-v5','deployment-v6','runtime-access-v1'),false));
CREATE FUNCTION loomplane_runtime_access_operation_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.body->>'kind'='runtime-access-upgrade' AND current_setting('loomplane.runtime_access_protocol',true) IS DISTINCT FROM 'runtime-access-v1' THEN RAISE EXCEPTION 'runtime access protocol required' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_runtime_access_operation_guard BEFORE INSERT OR UPDATE ON loomplane_cluster_operations FOR EACH ROW EXECUTE FUNCTION loomplane_runtime_access_operation_guard();
CREATE FUNCTION loomplane_runtime_access_route_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE i jsonb;
BEGIN
 IF NEW.request->>'build_id'='runtime-access-v1' OR NEW.plan_id LIKE 'runtime-access:%' OR NEW.request->>'workflow_type'='loomplane.runtime-access.v1' THEN
 i:=convert_from(decode(NEW.request->>'input','base64'),'UTF8')::jsonb;
 IF NEW.request->>'build_id' IS DISTINCT FROM 'runtime-access-v1' OR NEW.request->>'workflow_type' IS DISTINCT FROM 'loomplane.runtime-access.v1' OR NEW.plan_id IS DISTINCT FROM 'runtime-access:'||(i->>'id') OR i->>'org_id' IS DISTINCT FROM NEW.org_id OR NEW.request->>'workflow_id' IS DISTINCT FROM NEW.workflow_id OR NEW.request->>'request_id' IS DISTINCT FROM NEW.workflow_id OR NOT EXISTS(SELECT 1 FROM loomplane_cluster_operations o WHERE o.org_id=NEW.org_id AND o.id=i->>'id' AND o.body->>'kind'='runtime-access-upgrade' AND o.status IN ('accepted','running','waiting_provider','needs_attention','succeeded') AND NEW.workflow_id='runtime-access:'||o.id||':'||(o.body->>'attempt')) THEN RAISE EXCEPTION 'reviewed runtime access route required' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_runtime_access_route_guard BEFORE INSERT OR UPDATE ON loomplane_deployment_workflow_runs FOR EACH ROW EXECUTE FUNCTION loomplane_runtime_access_route_guard();
CREATE FUNCTION loomplane_runtime_access_participant_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF EXISTS(SELECT 1 FROM loomplane_deployment_workflow_runs WHERE workflow_id=NEW.workflow_id AND request->>'build_id'='runtime-access-v1') THEN RAISE EXCEPTION 'runtime access cannot execute a deployment plan' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_runtime_access_participant_guard BEFORE INSERT OR UPDATE ON loomplane_deployment_workflow_participants FOR EACH ROW EXECUTE FUNCTION loomplane_runtime_access_participant_guard();
`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_runtime_access) OR EXISTS(SELECT 1 FROM loomplane_cluster_operations WHERE body->>'kind'='runtime-access-upgrade') THEN RAISE EXCEPTION 'runtime access history must be retained'; END IF; END $$;
DROP TRIGGER loomplane_runtime_access_participant_guard ON loomplane_deployment_workflow_participants; DROP FUNCTION loomplane_runtime_access_participant_guard();
DROP TRIGGER loomplane_runtime_access_route_guard ON loomplane_deployment_workflow_runs; DROP FUNCTION loomplane_runtime_access_route_guard();
DROP TRIGGER loomplane_runtime_access_operation_guard ON loomplane_cluster_operations; DROP FUNCTION loomplane_runtime_access_operation_guard();
ALTER TABLE loomplane_deployment_workflow_runs DROP CONSTRAINT loomplane_deployment_workflow_runs_build;
ALTER TABLE loomplane_deployment_workflow_runs ADD CONSTRAINT loomplane_deployment_workflow_runs_build CHECK(coalesce(request->>'build_id' IN ('deployment-v2','deployment-v3','deployment-v4','deployment-v5','deployment-v6'),false));
DROP TABLE loomplane_runtime_access; DROP FUNCTION loomplane_runtime_access_guard();`)
		return err
	})
}
