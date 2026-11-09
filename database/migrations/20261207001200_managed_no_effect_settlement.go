package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE TABLE loomplane_cluster_demand_settlements(
 org_id text NOT NULL,plan_id text NOT NULL,service text NOT NULL,demand_id text NOT NULL,
 schema_version integer NOT NULL DEFAULT 1,revision bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(org_id,plan_id,service),FOREIGN KEY(org_id,plan_id,service) REFERENCES loomplane_cluster_workload_intents(org_id,plan_id,service));
CREATE FUNCTION loomplane_settle_unissued_demand(tenant text,plan text,service_name text) RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE demand jsonb;
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||tenant,0));
 SELECT body INTO demand FROM loomplane_cluster_workload_intents WHERE org_id=tenant AND plan_id=plan AND service=service_name FOR UPDATE;
 IF NOT FOUND THEN RETURN false; END IF;
 PERFORM 1 FROM loomplane_managed_workload_authority WHERE org_id=tenant AND body->>'Plan'=plan AND body->>'Service'=service_name FOR UPDATE;
 IF EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances WHERE org_id=tenant AND scope->>'Plan'=plan AND scope->>'Service'=service_name
 AND (outcome IS NULL OR outcome->>'State' IS DISTINCT FROM 'rejected' OR NOT coalesce((outcome->>'StatusCode')::int IN (400,401,403,404,409,422,429),false)))
 OR EXISTS(SELECT 1 FROM loomplane_cluster_workload_holds WHERE org_id=tenant AND id=demand->'input'->>'id' AND (body->'receipt' IS NOT NULL AND body->'receipt'<>'null'::jsonb OR coalesce(body->>'binding_id','')<>''))
 THEN RETURN false; END IF;
 INSERT INTO loomplane_cluster_demand_settlements(org_id,plan_id,service,demand_id) VALUES(tenant,plan,service_name,demand->'input'->>'id') ON CONFLICT DO NOTHING;
 UPDATE loomplane_managed_workload_authority SET revoked_at=clock_timestamp(),revision=revision+1,updated_at=clock_timestamp()
 WHERE org_id=tenant AND body->>'Plan'=plan AND body->>'Service'=service_name AND revoked_at IS NULL;
 UPDATE loomplane_workload_attempts SET active=false,settled=true,generation=generation+1
 WHERE org_id=tenant AND plan_id=plan AND service=service_name AND NOT legacy_unfenced AND (active OR NOT settled);
 UPDATE loomplane_cluster_workload_holds SET released=true WHERE org_id=tenant AND id=demand->'input'->>'id' AND NOT released;
 DELETE FROM loomplane_cluster_demand_members WHERE org_id=tenant AND plan_id=plan AND service=service_name;
 RETURN true;
END $$;
CREATE FUNCTION loomplane_settle_terminal_cluster_demand() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE item record;
BEGIN
 FOR item IN SELECT service FROM loomplane_cluster_workload_intents WHERE org_id=NEW.org_id AND plan_id=NEW.plan_id ORDER BY service LOOP
 PERFORM loomplane_settle_unissued_demand(NEW.org_id,NEW.plan_id,item.service);
 END LOOP;
 RETURN NULL;
END $$;
CREATE TRIGGER loomplane_settle_terminal_cluster_demand AFTER INSERT OR UPDATE OF status ON loomplane_deployments
 FOR EACH ROW WHEN(NEW.status IN ('failed','cancelled')) EXECUTE FUNCTION loomplane_settle_terminal_cluster_demand();
CREATE FUNCTION loomplane_guard_settled_cluster_demand() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE tenant text;plan text;service_name text;
BEGIN
 IF TG_TABLE_NAME='loomplane_workload_attempts' THEN
  IF NOT NEW.active THEN RETURN NEW; END IF;
  tenant:=NEW.org_id;plan:=NEW.plan_id;service_name:=NEW.service;
 ELSIF TG_TABLE_NAME='loomplane_cluster_workload_holds' THEN
  IF NEW.released THEN RETURN NEW; END IF;
  tenant:=NEW.org_id;plan:=NEW.body->'input'->>'plan_id';service_name:=NEW.body->'input'->>'service';
 ELSE
  IF TG_TABLE_NAME='loomplane_managed_workload_issuances' THEN IF NEW.cleanup_grant_id IS NOT NULL THEN RETURN NEW; END IF; END IF;
  tenant:=NEW.org_id;
  IF TG_TABLE_NAME='loomplane_managed_workload_authority' THEN
   IF NEW.revoked_at IS NOT NULL THEN RETURN NEW; END IF;
   plan:=NEW.body->>'Plan';service_name:=NEW.body->>'Service';
  ELSE plan:=NEW.scope->>'Plan';service_name:=NEW.scope->>'Service';
  END IF;
 END IF;
 IF EXISTS(SELECT 1 FROM loomplane_cluster_demand_settlements WHERE org_id=tenant AND plan_id=plan AND service=service_name) THEN
 RAISE EXCEPTION 'cluster demand has settled without application effects' USING ERRCODE='23514';
 END IF;RETURN NEW;
END $$;
CREATE TRIGGER loomplane_guard_settled_attempt BEFORE INSERT OR UPDATE ON loomplane_workload_attempts FOR EACH ROW EXECUTE FUNCTION loomplane_guard_settled_cluster_demand();
CREATE TRIGGER loomplane_guard_settled_hold BEFORE INSERT OR UPDATE ON loomplane_cluster_workload_holds FOR EACH ROW EXECUTE FUNCTION loomplane_guard_settled_cluster_demand();
CREATE TRIGGER loomplane_guard_settled_authority BEFORE INSERT OR UPDATE ON loomplane_managed_workload_authority FOR EACH ROW EXECUTE FUNCTION loomplane_guard_settled_cluster_demand();
CREATE TRIGGER loomplane_guard_settled_issuance BEFORE INSERT ON loomplane_managed_workload_issuances FOR EACH ROW EXECUTE FUNCTION loomplane_guard_settled_cluster_demand();`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN RAISE EXCEPTION 'logical settlement fences must be retained'; END $$`)
		return err
	})
}
