package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, fleetNamespaceAttemptsSchema)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		// Empty journals can be removed without inventing evidence for legacy rows.
		_, err := db.Exec(ctx, `LOCK TABLE loomplane_fleet_namespaces,loomplane_fleet_namespace_attempts IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM loomplane_fleet_namespace_attempts) THEN RAISE EXCEPTION 'namespace authorization history must be retained'; END IF;
 END $$;
DROP TRIGGER loomplane_fleet_namespace_identity ON loomplane_fleet_namespaces;
CREATE OR REPLACE FUNCTION loomplane_fleet_namespace_identity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' OR (NEW.operation_id,NEW.org_id,NEW.project_id,NEW.fleet_id,NEW.instance_id,NEW.environment_id,NEW.cloud_id,NEW.cluster_id,NEW.namespace,NEW.ownership_epoch,NEW.body) IS DISTINCT FROM (OLD.operation_id,OLD.org_id,OLD.project_id,OLD.fleet_id,OLD.instance_id,OLD.environment_id,OLD.cloud_id,OLD.cluster_id,OLD.namespace,OLD.ownership_epoch,OLD.body) THEN RAISE EXCEPTION 'instance namespace identity is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW; END $$;
CREATE TRIGGER loomplane_fleet_namespace_identity BEFORE UPDATE OR DELETE ON loomplane_fleet_namespaces FOR EACH ROW EXECUTE FUNCTION loomplane_fleet_namespace_identity();
ALTER TABLE loomplane_fleet_namespaces DROP CONSTRAINT fleet_namespace_creation_attempt,DROP CONSTRAINT fleet_namespace_current_attempt,DROP CONSTRAINT fleet_namespace_revision,DROP COLUMN creation_attempt_id,DROP COLUMN current_attempt_id,DROP COLUMN revision;
DROP TABLE loomplane_fleet_namespace_attempts;
DROP FUNCTION loomplane_fleet_namespace_attempt_guard();`)
		return err
	})
}

const fleetNamespaceAttemptsSchema = `
CREATE TABLE loomplane_fleet_namespace_attempts (
 id text PRIMARY KEY,
 org_id text NOT NULL, operation_id text NOT NULL, project_id text NOT NULL,
 fleet_id text NOT NULL, instance_id text NOT NULL, environment_id text NOT NULL,
 key_id text NOT NULL, kind text NOT NULL, input_digest text NOT NULL,
 expected_namespace_revision bigint NOT NULL, result_namespace_revision bigint NOT NULL,
 expected_operation_revision bigint NOT NULL, result_operation_revision bigint NOT NULL,
 generation bigint NOT NULL, ownership_epoch bigint NOT NULL, authority_id text NOT NULL,
 authorized_at timestamptz NOT NULL, authorized_at_ns smallint NOT NULL,
 input jsonb NOT NULL, result jsonb NOT NULL,
 CONSTRAINT fleet_namespace_attempt_identity CHECK (id<>'' AND org_id<>'' AND operation_id<>'' AND project_id<>'' AND fleet_id<>'' AND instance_id<>'' AND environment_id<>'' AND key_id<>'' AND authority_id<>''),
 CONSTRAINT fleet_namespace_attempt_kind CHECK (kind IN ('reserve','checkpoint','renew','publish')),
 CONSTRAINT fleet_namespace_attempt_digest CHECK (input_digest ~ '^[a-f0-9]{64}$'),
 CONSTRAINT fleet_namespace_attempt_time CHECK (isfinite(authorized_at) AND authorized_at_ns>=0 AND authorized_at_ns<=999),
 CONSTRAINT fleet_namespace_attempt_generation CHECK (generation>0 AND ownership_epoch>0),
 CONSTRAINT fleet_namespace_attempt_revision CHECK (
  (kind='reserve' AND expected_namespace_revision=0 AND result_namespace_revision=1 AND expected_operation_revision=0 AND result_operation_revision=1)
  OR (kind IN ('checkpoint','publish') AND expected_namespace_revision>0 AND result_namespace_revision=expected_namespace_revision+1 AND expected_operation_revision>0 AND result_operation_revision=expected_operation_revision+1)
  OR (kind='renew' AND expected_namespace_revision>0 AND result_namespace_revision=expected_namespace_revision+1 AND expected_operation_revision>0 AND result_operation_revision=expected_operation_revision)
 ),
 CONSTRAINT fleet_namespace_attempt_payload CHECK (jsonb_typeof(input)='object' AND jsonb_typeof(result)='object' AND octet_length(input::text)<=2097152 AND octet_length(result::text)<=2097152),
 UNIQUE(org_id,operation_id,id), UNIQUE(org_id,operation_id,result_namespace_revision),
 FOREIGN KEY(org_id,operation_id) REFERENCES loomplane_cluster_operations(org_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,project_id,fleet_id) REFERENCES loomplane_fleets(org_id,project_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,fleet_id,instance_id) REFERENCES loomplane_fleet_instances(org_id,fleet_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(org_id,project_id,environment_id) REFERENCES loomplane_environments(org_id,project_id,id) ON DELETE RESTRICT
);
ALTER TABLE loomplane_fleet_namespaces
 ADD COLUMN revision bigint NOT NULL DEFAULT 1,
 ADD COLUMN creation_attempt_id text,
 ADD COLUMN current_attempt_id text,
 ADD CONSTRAINT fleet_namespace_revision CHECK (revision>0),
 ADD CONSTRAINT fleet_namespace_creation_attempt FOREIGN KEY(org_id,operation_id,creation_attempt_id) REFERENCES loomplane_fleet_namespace_attempts(org_id,operation_id,id) ON DELETE RESTRICT,
 ADD CONSTRAINT fleet_namespace_current_attempt FOREIGN KEY(org_id,operation_id,current_attempt_id) REFERENCES loomplane_fleet_namespace_attempts(org_id,operation_id,id) ON DELETE RESTRICT;
CREATE FUNCTION loomplane_fleet_namespace_attempt_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE stamp text; n loomplane_fleet_namespaces; o loomplane_cluster_operations;
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'namespace authorization history is immutable' USING ERRCODE='23514'; END IF;
 stamp:=to_char(NEW.authorized_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"');
 IF jsonb_typeof(NEW.input->'authorized_at') IS DISTINCT FROM 'string'
 OR jsonb_typeof(NEW.result->'authorized_at') IS DISTINCT FROM 'string'
 OR NEW.input->>'authorized_at' IS DISTINCT FROM stamp OR NEW.result->>'authorized_at' IS DISTINCT FROM stamp
 OR NEW.input->'authorized_at_ns' IS DISTINCT FROM to_jsonb(NEW.authorized_at_ns)
 OR NEW.result->'authorized_at_ns' IS DISTINCT FROM to_jsonb(NEW.authorized_at_ns)
 THEN RAISE EXCEPTION 'namespace decision timestamp differs from its evidence' USING ERRCODE='23514'; END IF;
 SELECT * INTO o FROM loomplane_cluster_operations WHERE org_id=NEW.org_id AND id=NEW.operation_id;
 IF NOT FOUND OR o.revision<>NEW.result_operation_revision OR o.body->>'kind' IS DISTINCT FROM 'instance-namespace'
 THEN RAISE EXCEPTION 'namespace operation result differs from its evidence' USING ERRCODE='23514'; END IF;
 SELECT * INTO n FROM loomplane_fleet_namespaces WHERE org_id=NEW.org_id AND operation_id=NEW.operation_id;
 IF NEW.kind='reserve' THEN
  IF FOUND THEN RAISE EXCEPTION 'namespace creation already exists' USING ERRCODE='23514'; END IF;
 ELSE
  IF NOT FOUND OR (n.project_id,n.fleet_id,n.instance_id,n.environment_id,n.ownership_epoch,n.revision) IS DISTINCT FROM (NEW.project_id,NEW.fleet_id,NEW.instance_id,NEW.environment_id,NEW.ownership_epoch,NEW.expected_namespace_revision)
  THEN RAISE EXCEPTION 'namespace input differs from its evidence' USING ERRCODE='23514'; END IF;
  IF NEW.kind<>'renew' AND (n.generation,n.authority::jsonb->>'id',n.authority::jsonb->'principal'->>'KeyID') IS DISTINCT FROM (NEW.generation,NEW.authority_id,NEW.key_id)
  THEN RAISE EXCEPTION 'namespace checkpoint cannot replace authority' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_fleet_namespace_attempt_guard BEFORE INSERT OR UPDATE OR DELETE ON loomplane_fleet_namespace_attempts FOR EACH ROW EXECUTE FUNCTION loomplane_fleet_namespace_attempt_guard();
CREATE OR REPLACE FUNCTION loomplane_fleet_namespace_identity() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE evidence loomplane_fleet_namespace_attempts;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'instance namespace history is immutable' USING ERRCODE='23514'; END IF;
 IF NEW.current_attempt_id IS NULL THEN RAISE EXCEPTION 'namespace write requires current attempt evidence' USING ERRCODE='23514'; END IF;
 SELECT * INTO evidence FROM loomplane_fleet_namespace_attempts WHERE org_id=NEW.org_id AND operation_id=NEW.operation_id AND id=NEW.current_attempt_id;
 IF NOT FOUND OR (evidence.project_id,evidence.fleet_id,evidence.instance_id,evidence.environment_id,evidence.generation,evidence.ownership_epoch,evidence.result_namespace_revision,evidence.authority_id,evidence.key_id)
 IS DISTINCT FROM (NEW.project_id,NEW.fleet_id,NEW.instance_id,NEW.environment_id,NEW.generation,NEW.ownership_epoch,NEW.revision,NEW.authority::jsonb->>'id',NEW.authority::jsonb->'principal'->>'KeyID')
 THEN RAISE EXCEPTION 'namespace result differs from current attempt evidence' USING ERRCODE='23514'; END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.revision<>1 OR NEW.creation_attempt_id IS DISTINCT FROM NEW.current_attempt_id OR evidence.kind<>'reserve'
  THEN RAISE EXCEPTION 'namespace creation requires its reserve attempt' USING ERRCODE='23514'; END IF;
 ELSE
  IF (NEW.operation_id,NEW.org_id,NEW.project_id,NEW.fleet_id,NEW.instance_id,NEW.environment_id,NEW.cloud_id,NEW.cluster_id,NEW.namespace,NEW.ownership_epoch,NEW.body,NEW.creation_attempt_id)
  IS DISTINCT FROM (OLD.operation_id,OLD.org_id,OLD.project_id,OLD.fleet_id,OLD.instance_id,OLD.environment_id,OLD.cloud_id,OLD.cluster_id,OLD.namespace,OLD.ownership_epoch,OLD.body,OLD.creation_attempt_id)
  OR NEW.revision<>OLD.revision+1 OR evidence.expected_namespace_revision<>OLD.revision OR evidence.kind='reserve' OR NEW.current_attempt_id IS NOT DISTINCT FROM OLD.current_attempt_id
  THEN RAISE EXCEPTION 'instance namespace identity or revision changed' USING ERRCODE='23514'; END IF;
  IF evidence.kind<>'renew' AND (NEW.generation,NEW.snapshot_digest,NEW.authority) IS DISTINCT FROM (OLD.generation,OLD.snapshot_digest,OLD.authority)
  THEN RAISE EXCEPTION 'namespace checkpoint cannot replace authority' USING ERRCODE='23514'; END IF;
  IF evidence.kind='renew' AND NEW.generation<OLD.generation THEN RAISE EXCEPTION 'namespace generation cannot decrease' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER loomplane_fleet_namespace_identity ON loomplane_fleet_namespaces;
CREATE TRIGGER loomplane_fleet_namespace_identity BEFORE INSERT OR UPDATE OR DELETE ON loomplane_fleet_namespaces FOR EACH ROW EXECUTE FUNCTION loomplane_fleet_namespace_identity();
`
