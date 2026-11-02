package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE TABLE loomplane_managed_cleanup_tombstones (
 org_id text NOT NULL,release_id text NOT NULL,cloud_id text NOT NULL,resource_id text NOT NULL,grant_id text NOT NULL,
 schema_version integer NOT NULL CHECK(schema_version=1),revision bigint GENERATED ALWAYS AS IDENTITY,
 observation_started_at timestamptz NOT NULL,observed_at timestamptz NOT NULL,inventory_barrier bigint NOT NULL CHECK(inventory_barrier>=0),body jsonb NOT NULL,
 PRIMARY KEY(org_id,release_id), FOREIGN KEY(org_id,release_id) REFERENCES loomplane_managed_workload_authority(org_id,release_id),
 FOREIGN KEY(org_id,grant_id) REFERENCES loomplane_managed_cleanup_grants(org_id,id),
 CHECK(observation_started_at<=observed_at),
 CHECK((body->>'org_id'=org_id AND body->>'release_id'=release_id AND body->>'grant_id'=grant_id AND body->>'cloud_id'=cloud_id AND body->>'resource_id'=resource_id AND (body->>'schema_version')::int=1 AND body->>'namespace_uid'<>'' AND body->>'cluster_ca'<>'' AND body->'resource_versions'->>'deployments'<>'' AND body->'resource_versions'->>'replicasets'<>'' AND body->'resource_versions'->>'pods'<>'') IS TRUE)
);
CREATE FUNCTION loomplane_check_cleanup_tombstone() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
 IF NOT EXISTS(SELECT 1 FROM loomplane_managed_cleanup_grants g JOIN loomplane_managed_workload_authority a USING(org_id,release_id)
 WHERE g.org_id=NEW.org_id AND g.release_id=NEW.release_id AND g.id=NEW.grant_id AND g.revoked_at IS NULL AND a.revoked_at IS NOT NULL AND a.revision=g.authority_revision AND a.revoked_at<NEW.observation_started_at)
 OR EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances w WHERE w.org_id=NEW.org_id AND w.release_id=NEW.release_id AND (w.outcome IS NULL OR w.issued_at>=NEW.observation_started_at OR w.resolved_at>=NEW.observation_started_at))
 OR NEW.observed_at>clock_timestamp() OR NEW.observation_started_at<clock_timestamp()-interval '2 minutes'
 THEN RAISE EXCEPTION 'newer complete cleanup observation required' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_check_cleanup_tombstone BEFORE INSERT ON loomplane_managed_cleanup_tombstones FOR EACH ROW EXECUTE FUNCTION loomplane_check_cleanup_tombstone();
CREATE INDEX loomplane_managed_cleanup_resource ON loomplane_managed_cleanup_tombstones(org_id,cloud_id,resource_id);
CREATE TABLE loomplane_managed_cleanup_coverage (
 org_id text NOT NULL,release_id text NOT NULL,observation_revision bigint NOT NULL CHECK(observation_revision>0),
 checked_at timestamptz NOT NULL,qualified boolean NOT NULL,PRIMARY KEY(org_id,release_id),
 FOREIGN KEY(org_id,release_id) REFERENCES loomplane_managed_cleanup_tombstones(org_id,release_id)
);
CREATE TABLE loomplane_managed_cleanup_drift_events (
 org_id text NOT NULL,release_id text NOT NULL,resource text NOT NULL,uid text NOT NULL,observation_revision bigint NOT NULL CHECK(observation_revision>0),
 checked_at timestamptz NOT NULL,present boolean NOT NULL,
 PRIMARY KEY(org_id,release_id,resource,uid,observation_revision),
 FOREIGN KEY(org_id,release_id) REFERENCES loomplane_managed_cleanup_tombstones(org_id,release_id)
);
CREATE FUNCTION loomplane_retain_managed_cleanup_history() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 RAISE EXCEPTION 'managed cleanup history must be retained' USING ERRCODE='23514';
END $$;
CREATE TRIGGER loomplane_retain_cleanup_tombstone BEFORE UPDATE OR DELETE ON loomplane_managed_cleanup_tombstones FOR EACH ROW EXECUTE FUNCTION loomplane_retain_managed_cleanup_history();
CREATE TRIGGER loomplane_retain_cleanup_drift BEFORE UPDATE OR DELETE ON loomplane_managed_cleanup_drift_events FOR EACH ROW EXECUTE FUNCTION loomplane_retain_managed_cleanup_history();
CREATE VIEW loomplane_managed_cleanup_current_drift AS
 SELECT DISTINCT ON (org_id,release_id,resource,uid) org_id,release_id,resource,uid,observation_revision,checked_at,present
 FROM loomplane_managed_cleanup_drift_events ORDER BY org_id,release_id,resource,uid,observation_revision DESC;
CREATE FUNCTION loomplane_managed_cleanup_admission_block(p_org text,p_cloud text,p_resource text) RETURNS text LANGUAGE sql STABLE AS $$
 SELECT CASE
 WHEN EXISTS(SELECT 1 FROM loomplane_managed_cleanup_tombstones t JOIN loomplane_managed_cleanup_current_drift d USING(org_id,release_id) WHERE t.org_id=p_org AND t.cloud_id=p_cloud AND t.resource_id=p_resource AND d.present)
 THEN 'managed_cleanup_drift'
 WHEN EXISTS(SELECT 1 FROM loomplane_managed_cleanup_tombstones t WHERE t.org_id=p_org AND t.cloud_id=p_cloud AND t.resource_id=p_resource AND NOT EXISTS(
 SELECT 1 FROM loomplane_managed_cleanup_coverage c JOIN loomplane_cluster_capacity_inventory i ON i.org_id=t.org_id AND i.cloud_id=t.cloud_id AND i.resource_id=t.resource_id AND i.saved_revision=c.observation_revision
 WHERE c.org_id=t.org_id AND c.release_id=t.release_id AND c.qualified AND c.observation_revision>t.inventory_barrier AND c.checked_at>t.observed_at AND c.checked_at<=clock_timestamp() AND c.checked_at>clock_timestamp()-interval '2 minutes'))
 THEN 'managed_cleanup_observation_incomplete' ELSE '' END
$$;
CREATE FUNCTION loomplane_guard_cleanup_guarantees() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE reason text; app jsonb; cloud text; physical_resource text;
BEGIN
 IF TG_TABLE_NAME='loomplane_cluster_workload_holds' THEN
  PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
  cloud:=NEW.cloud_id; physical_resource:=NEW.resource_id;
 ELSE
  PERFORM pg_advisory_xact_lock(hashtextextended('cloud-organization/'||NEW.org_id,0));
  IF NEW.cleanup_grant_id IS NOT NULL THEN
   IF EXISTS(SELECT 1 FROM loomplane_managed_cleanup_tombstones t JOIN loomplane_managed_cleanup_grants g ON g.org_id=t.org_id AND g.id=NEW.cleanup_grant_id
   LEFT JOIN loomplane_managed_cleanup_intents i ON g.failed_review_id IS NULL AND i.org_id=g.org_id AND i.plan_id=g.plan_id AND i.action_id=g.action_id
   LEFT JOIN loomplane_managed_failed_cleanups f ON f.org_id=g.org_id AND f.id=g.failed_review_id
   WHERE t.org_id=NEW.org_id AND t.release_id=NEW.release_id AND coalesce(f.created_at,i.created_at,'-infinity'::timestamptz)<=t.observed_at)
   THEN RAISE EXCEPTION 'the accepted cleanup review has settled' USING ERRCODE='23514'; END IF;
   RETURN NEW;
  END IF;
  IF EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances w WHERE w.org_id=NEW.org_id AND w.release_id=NEW.release_id AND w.epoch=NEW.epoch AND w.cleanup_grant_id IS NULL AND w.request->>'Group'='apps' AND w.request->>'Resource'='deployments' AND w.request->>'Method' IN ('POST','PUT') AND w.outcome->>'State'='accepted') THEN RETURN NEW; END IF;
  SELECT body INTO app FROM loomplane_cluster_workload_intents WHERE org_id=NEW.org_id AND plan_id=NEW.scope->>'Plan' AND service=NEW.scope->>'Service';
  IF app->>'mode' IS DISTINCT FROM 'guaranteed' THEN RETURN NEW; END IF;
  cloud:=app->'input'->>'cloud_id'; physical_resource:=app->'input'->>'resource_id';
 END IF;
 reason:=loomplane_managed_cleanup_admission_block(NEW.org_id,cloud,physical_resource);
 IF reason='' AND EXISTS(SELECT 1 FROM loomplane_managed_cleanup_tombstones t WHERE t.org_id=NEW.org_id AND t.cloud_id=cloud AND t.resource_id=physical_resource)
 AND NOT EXISTS(SELECT 1 FROM loomplane_cluster_capacity_inventory i WHERE i.org_id=NEW.org_id AND i.cloud_id=cloud AND i.resource_id=physical_resource AND i.saved_revision=i.issued_revision AND i.complete AND i.body->>'workloads_complete'='true' AND i.checked_at<=clock_timestamp() AND i.checked_at>clock_timestamp()-interval '2 minutes')
 THEN reason:='workload_inventory_incomplete'; END IF;
 IF reason<>'' THEN RAISE EXCEPTION '%',reason USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER loomplane_guard_cleanup_reservation BEFORE INSERT ON loomplane_cluster_workload_holds FOR EACH ROW EXECUTE FUNCTION loomplane_guard_cleanup_guarantees();
CREATE TRIGGER loomplane_guard_cleanup_first_effect BEFORE INSERT ON loomplane_managed_workload_issuances FOR EACH ROW EXECUTE FUNCTION loomplane_guard_cleanup_guarantees();`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_managed_cleanup_tombstones) THEN RAISE EXCEPTION 'cleanup tombstones and drift history must be retained'; END IF; END $$;
DROP TRIGGER loomplane_guard_cleanup_first_effect ON loomplane_managed_workload_issuances;
DROP TRIGGER loomplane_guard_cleanup_reservation ON loomplane_cluster_workload_holds;
DROP FUNCTION loomplane_guard_cleanup_guarantees();
DROP FUNCTION loomplane_managed_cleanup_admission_block(text,text,text);
DROP VIEW loomplane_managed_cleanup_current_drift;
DROP TABLE loomplane_managed_cleanup_drift_events,loomplane_managed_cleanup_coverage,loomplane_managed_cleanup_tombstones;
DROP FUNCTION loomplane_retain_managed_cleanup_history();
DROP FUNCTION loomplane_check_cleanup_tombstone();`)
		return err
	})
}
