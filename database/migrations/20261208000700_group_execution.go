package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE TABLE loomplane_group_configurations (
 org_id text NOT NULL,group_id text NOT NULL,generation bigint NOT NULL,
 config_revision text NOT NULL,body jsonb NOT NULL,
 PRIMARY KEY(org_id,group_id,generation),
 FOREIGN KEY(org_id,group_id,generation) REFERENCES loomplane_placement_group_revisions(org_id,group_id,generation),
 CHECK(config_revision<>'' AND body->>'revision'=config_revision)
);
CREATE TRIGGER group_configuration_immutable BEFORE UPDATE OR DELETE ON loomplane_group_configurations FOR EACH ROW EXECUTE FUNCTION loomplane_group_immutable();
CREATE TABLE loomplane_group_executions (
 org_id text NOT NULL,group_id text NOT NULL,generation bigint NOT NULL,
 plan_id text NOT NULL,plan_revision text NOT NULL,actor jsonb NOT NULL,
 snapshot_id text NOT NULL UNIQUE,epoch bigint NOT NULL DEFAULT 0 CHECK(epoch>=0),
 snapshot_candidate text,snapshot_identity text,snapshot_version bigint,
 cancelled_at timestamptz,accepted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(org_id,group_id,generation),
 FOREIGN KEY(org_id,group_id,generation) REFERENCES loomplane_group_configurations(org_id,group_id,generation),
 FOREIGN KEY(org_id,plan_id) REFERENCES loomplane_plans(org_id,id),
 CHECK(snapshot_id<>'' AND plan_revision<>''),
 CHECK((snapshot_candidate IS NULL AND snapshot_identity IS NULL AND snapshot_version IS NULL) OR (snapshot_candidate IS NOT NULL AND snapshot_identity IS NOT NULL AND snapshot_version IS NOT NULL AND snapshot_candidate<>'' AND snapshot_identity<>'' AND snapshot_version=1))
);
CREATE TABLE loomplane_group_snapshot_candidates (
 org_id text NOT NULL,group_id text NOT NULL,generation bigint NOT NULL,id text NOT NULL,
 epoch bigint NOT NULL CHECK(epoch>0),vault_key text NOT NULL UNIQUE,
 issued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(org_id,group_id,generation,id),UNIQUE(org_id,group_id,generation,epoch),
 FOREIGN KEY(org_id,group_id,generation) REFERENCES loomplane_group_executions(org_id,group_id,generation)
);
ALTER TABLE loomplane_group_executions ADD CONSTRAINT group_snapshot_winner FOREIGN KEY(org_id,group_id,generation,snapshot_candidate) REFERENCES loomplane_group_snapshot_candidates(org_id,group_id,generation,id);
CREATE TRIGGER group_snapshot_candidate_immutable BEFORE UPDATE OR DELETE ON loomplane_group_snapshot_candidates FOR EACH ROW EXECUTE FUNCTION loomplane_group_immutable();
CREATE FUNCTION loomplane_group_execution_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE review jsonb; approved_actor jsonb;
BEGIN
 IF TG_OP='INSERT' THEN
  approved_actor:=nullif(current_setting('loomplane.capacity_actor',true),'')::jsonb;
  SELECT r.body INTO review FROM loomplane_placement_group_revisions r JOIN loomplane_plans p ON p.org_id=r.org_id AND p.id=r.plan_id
   WHERE r.org_id=NEW.org_id AND r.group_id=NEW.group_id AND r.generation=NEW.generation AND p.id=NEW.plan_id AND p.status='ready' AND p.executable AND p.revision=NEW.plan_revision
   AND p.result->'placement_groups' @> jsonb_build_array(r.body);
  IF review IS NULL OR approved_actor IS NULL OR NEW.actor<>approved_actor OR NEW.actor->>'OrgID'<>NEW.org_id OR coalesce(NEW.actor->>'UserID','')='' OR coalesce((NEW.actor->>'ReadOnly')::boolean,true) OR coalesce(NEW.actor->>'Kind','')<>'' OR NEW.actor ? 'fleet_scope' THEN
   RAISE EXCEPTION 'group acceptance authority changed' USING ERRCODE='23514';
  END IF;
 ELSE
  IF ROW(NEW.org_id,NEW.group_id,NEW.generation,NEW.plan_id,NEW.plan_revision,NEW.actor,NEW.snapshot_id,NEW.accepted_at) IS DISTINCT FROM ROW(OLD.org_id,OLD.group_id,OLD.generation,OLD.plan_id,OLD.plan_revision,OLD.actor,OLD.snapshot_id,OLD.accepted_at)
   OR NEW.epoch NOT IN(OLD.epoch,OLD.epoch+1) OR (OLD.cancelled_at IS NOT NULL AND NEW.cancelled_at IS DISTINCT FROM OLD.cancelled_at)
   OR (OLD.snapshot_candidate IS NOT NULL AND ROW(NEW.snapshot_candidate,NEW.snapshot_identity,NEW.snapshot_version,NEW.epoch) IS DISTINCT FROM ROW(OLD.snapshot_candidate,OLD.snapshot_identity,OLD.snapshot_version,OLD.epoch)) THEN
   RAISE EXCEPTION 'group execution identity or snapshot is immutable' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER group_execution_guard BEFORE INSERT OR UPDATE ON loomplane_group_executions FOR EACH ROW EXECUTE FUNCTION loomplane_group_execution_guard();
CREATE OR REPLACE FUNCTION loomplane_group_activation_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status='approved' AND NEW.result ? 'placement_groups' AND (
  NOT NEW.executable OR jsonb_array_length(NEW.result->'placement_groups')=0 OR EXISTS(
   SELECT 1 FROM jsonb_array_elements(NEW.result->'placement_groups') g WHERE NOT EXISTS(
    SELECT 1 FROM loomplane_group_executions e JOIN loomplane_placement_group_revisions r ON r.org_id=e.org_id AND r.group_id=e.group_id AND r.generation=e.generation
     WHERE e.org_id=NEW.org_id AND e.plan_id=NEW.id AND e.plan_revision=NEW.revision AND e.cancelled_at IS NULL AND r.body=g))) THEN
  RAISE EXCEPTION 'placement group accepted execution is unavailable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('variable-sources/v1',0));
 LOCK TABLE loomplane_group_configurations,loomplane_group_executions,loomplane_group_snapshot_candidates IN ACCESS EXCLUSIVE MODE;
 DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM loomplane_group_executions) OR EXISTS(SELECT 1 FROM loomplane_group_snapshot_candidates) THEN
   RAISE EXCEPTION 'retained group snapshot state prevents downgrade' USING ERRCODE='23514';
  END IF;
 END $$;
 CREATE OR REPLACE FUNCTION loomplane_group_activation_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status='approved' AND NEW.result ? 'placement_groups' THEN RAISE EXCEPTION 'placement group runtime is unavailable' USING ERRCODE='23514'; END IF; RETURN NEW; END $$;
 ALTER TABLE loomplane_group_executions DROP CONSTRAINT group_snapshot_winner;
 DROP TABLE loomplane_group_snapshot_candidates; DROP TABLE loomplane_group_executions; DROP FUNCTION loomplane_group_execution_guard(); DROP TABLE loomplane_group_configurations;`)
		return err
	})
}
