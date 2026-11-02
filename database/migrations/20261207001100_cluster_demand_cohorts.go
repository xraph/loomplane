package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE UNIQUE INDEX loomplane_cluster_workload_demand_id ON loomplane_cluster_workload_intents(org_id,(body->'input'->>'id'));
CREATE TABLE loomplane_cluster_demand_cohorts(id text PRIMARY KEY,org_id text NOT NULL,cloud_id text NOT NULL,resource_id text NOT NULL,pool_id text NOT NULL,profile text NOT NULL,policy_revision bigint NOT NULL,budget_revision bigint NOT NULL,native boolean NOT NULL,operation_id text,issued_at timestamptz,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),closed_at timestamptz);
CREATE UNIQUE INDEX loomplane_cluster_demand_open ON loomplane_cluster_demand_cohorts(org_id,cloud_id,resource_id,pool_id) WHERE closed_at IS NULL;
CREATE TABLE loomplane_cluster_demand_members(cohort_id text NOT NULL REFERENCES loomplane_cluster_demand_cohorts(id),org_id text NOT NULL,plan_id text NOT NULL,service text NOT NULL,demand_id text NOT NULL,reason text NOT NULL CHECK(reason IN ('review_capacity','scheduler_shortage')),observed_revision bigint NOT NULL,joined_at timestamptz NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(org_id,demand_id),FOREIGN KEY(org_id,plan_id,service) REFERENCES loomplane_cluster_workload_intents(org_id,plan_id,service));
CREATE FUNCTION loomplane_close_empty_cluster_cohort() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE loomplane_cluster_demand_cohorts SET closed_at=clock_timestamp() WHERE id=OLD.cohort_id AND closed_at IS NULL AND NOT EXISTS(SELECT 1 FROM loomplane_cluster_demand_members WHERE cohort_id=OLD.cohort_id);RETURN NULL; END $$;
CREATE TRIGGER loomplane_close_empty_cluster_cohort AFTER DELETE ON loomplane_cluster_demand_members FOR EACH ROW EXECUTE FUNCTION loomplane_close_empty_cluster_cohort();
CREATE FUNCTION loomplane_lock_cluster_demand(text,text) RETURNS void LANGUAGE SQL VOLATILE AS $$ SELECT pg_advisory_xact_lock(hashtextextended(json_build_array('cluster-demand-membership',$1,$2)::text,0)) $$;
CREATE FUNCTION loomplane_cancel_cluster_demand() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM loomplane_cluster_workload_intents WHERE org_id=NEW.org_id AND plan_id=NEW.plan_id) THEN RETURN NULL; END IF;
 PERFORM loomplane_lock_cluster_demand(NEW.org_id,NEW.plan_id); DELETE FROM loomplane_cluster_demand_members WHERE org_id=NEW.org_id AND plan_id=NEW.plan_id;RETURN NULL; END $$;
CREATE TRIGGER loomplane_cancel_cluster_demand AFTER INSERT OR UPDATE OF status ON loomplane_deployments FOR EACH ROW WHEN(NEW.status='cancelled') EXECUTE FUNCTION loomplane_cancel_cluster_demand();
CREATE FUNCTION loomplane_detach_obsolete_pool_demand() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 DELETE FROM loomplane_cluster_demand_members m USING loomplane_cluster_demand_cohorts c WHERE c.id=m.cohort_id AND c.org_id=NEW.org_id AND c.resource_id=NEW.id AND c.native AND (NEW.deleted_at IS NOT NULL OR NOT coalesce((NEW.body->'scaling_policies'->c.pool_id->>'revision')::bigint=c.policy_revision AND NEW.body->'scaling_policies'->c.pool_id->>'revoked'='false' AND coalesce(NEW.body->'scaling_policies'->c.pool_id->>'pause_reason','')='' AND NEW.body->'scaling_policies'->c.pool_id->>'mode'='native',false)); RETURN NULL; END $$;
CREATE TRIGGER loomplane_detach_obsolete_pool_demand AFTER UPDATE OF body,deleted_at ON loomplane_managed_resources FOR EACH ROW WHEN(OLD.body->'scaling_policies' IS DISTINCT FROM NEW.body->'scaling_policies' OR OLD.deleted_at IS DISTINCT FROM NEW.deleted_at) EXECUTE FUNCTION loomplane_detach_obsolete_pool_demand();
CREATE TABLE loomplane_cluster_pool_advice(org_id text NOT NULL,cloud_id text NOT NULL,resource_id text NOT NULL,pool_id text NOT NULL,policy_revision bigint NOT NULL,observed_at timestamptz NOT NULL,low_since timestamptz,review_decrease boolean NOT NULL DEFAULT false,retry_observation_at timestamptz,PRIMARY KEY(org_id,cloud_id,resource_id,pool_id));`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_cluster_demand_members) THEN RAISE EXCEPTION 'active cluster demand must be retained';END IF;END $$;DROP TRIGGER loomplane_detach_obsolete_pool_demand ON loomplane_managed_resources;DROP FUNCTION loomplane_detach_obsolete_pool_demand();DROP TRIGGER loomplane_cancel_cluster_demand ON loomplane_deployments;DROP FUNCTION loomplane_cancel_cluster_demand();DROP FUNCTION loomplane_lock_cluster_demand(text,text);DROP TRIGGER loomplane_close_empty_cluster_cohort ON loomplane_cluster_demand_members;DROP FUNCTION loomplane_close_empty_cluster_cohort();DROP TABLE loomplane_cluster_demand_members,loomplane_cluster_demand_cohorts,loomplane_cluster_pool_advice;DROP INDEX loomplane_cluster_workload_demand_id;`)
		return err
	})
}
