package migrations

import (
	"context"
	"fmt"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		if _, err := db.Exec(ctx, `DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM loomplane_cluster_demand_cohorts WHERE id='' OR org_id='' OR cloud_id='' OR resource_id='' OR pool_id='' OR profile='' OR policy_revision<0 OR budget_revision<0 OR NOT isfinite(created_at) OR closed_at<created_at)
 OR EXISTS(SELECT 1 FROM loomplane_cluster_pool_growth WHERE org_id='' OR cloud_id='' OR resource_id='' OR pool_id='' OR operation_id='' OR NOT isfinite(issued_at) OR NOT isfinite(next_growth_at) OR next_growth_at<=issued_at)
 OR EXISTS(SELECT 1 FROM loomplane_cluster_pool_advice WHERE org_id='' OR cloud_id='' OR resource_id='' OR pool_id='' OR policy_revision<1 OR NOT isfinite(observed_at))
 OR EXISTS(SELECT 1 FROM loomplane_cluster_capacity_inventory WHERE org_id='' OR cloud_id='' OR resource_id='' OR saved_revision>0 AND (body IS NULL OR jsonb_typeof(body)<>'object'))
 THEN RAISE EXCEPTION 'legacy cluster controls require qualified repair before versioning'; END IF;
 END $$`); err != nil {
			return err
		}

		for _, table := range []string{"loomplane_cluster_demand_cohorts", "loomplane_cluster_pool_growth", "loomplane_cluster_pool_advice", "loomplane_cluster_capacity_inventory"} {
			if _, err := db.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN schema_version integer NOT NULL DEFAULT 1,ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),ADD COLUMN updated_at timestamptz NOT NULL DEFAULT clock_timestamp()`, table)); err != nil {
				return err
			}
			if table != "loomplane_cluster_demand_cohorts" {
				if _, err := db.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN created_at timestamptz NOT NULL DEFAULT clock_timestamp()`, table)); err != nil {
					return err
				}
			}
		}
		_, err := db.Exec(ctx, `UPDATE loomplane_cluster_demand_cohorts SET updated_at=greatest(created_at,issued_at,closed_at);
 UPDATE loomplane_cluster_pool_growth SET created_at=issued_at,updated_at=issued_at;
 UPDATE loomplane_cluster_pool_advice SET created_at=observed_at,updated_at=observed_at;
 UPDATE loomplane_cluster_capacity_inventory SET created_at=coalesce(checked_at,created_at),updated_at=coalesce(checked_at,updated_at);
 CREATE FUNCTION loomplane_cluster_control_supported(version integer) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$ BEGIN IF version IS DISTINCT FROM 1 THEN RAISE EXCEPTION 'unsupported cluster control schema' USING ERRCODE='23514'; END IF;RETURN true;END $$;
 CREATE FUNCTION loomplane_cluster_controls_current(tenant text,resource text) RETURNS boolean LANGUAGE plpgsql STABLE AS $$
 BEGIN
 IF EXISTS(SELECT 1 FROM loomplane_cluster_demand_cohorts WHERE org_id=tenant AND (resource='' OR resource_id=resource) AND schema_version<>1)
 OR EXISTS(SELECT 1 FROM loomplane_cluster_pool_growth WHERE org_id=tenant AND (resource='' OR resource_id=resource) AND schema_version<>1)
 OR EXISTS(SELECT 1 FROM loomplane_cluster_pool_advice WHERE org_id=tenant AND (resource='' OR resource_id=resource) AND schema_version<>1)
 OR EXISTS(SELECT 1 FROM loomplane_cluster_capacity_inventory WHERE org_id=tenant AND (resource='' OR resource_id=resource) AND schema_version<>1)
 THEN RAISE EXCEPTION 'unsupported cluster control schema' USING ERRCODE='23514'; END IF;
 RETURN true;
 END $$;
 CREATE OR REPLACE FUNCTION loomplane_claim_pool_growth(p_org text,p_cloud text,p_resource text,p_pool text,p_op text,p_seconds bigint)
RETURNS timestamptz LANGUAGE plpgsql AS $$
DECLARE issued timestamptz:=clock_timestamp(); deadline timestamptz;
BEGIN
 PERFORM loomplane_cluster_controls_current(p_org,p_resource);
 IF p_seconds NOT BETWEEN 1 AND 86400 THEN RAISE EXCEPTION 'invalid scale-up cooldown'; END IF;
 INSERT INTO loomplane_cluster_pool_growth(org_id,cloud_id,resource_id,pool_id,operation_id,issued_at,next_growth_at)
 VALUES(p_org,p_cloud,p_resource,p_pool,p_op,issued,issued+make_interval(secs=>p_seconds))
 ON CONFLICT(org_id,cloud_id,resource_id,pool_id) DO UPDATE SET operation_id=excluded.operation_id,issued_at=excluded.issued_at,next_growth_at=excluded.next_growth_at
 WHERE loomplane_cluster_pool_growth.next_growth_at<=excluded.issued_at;
 IF FOUND THEN RETURN NULL; END IF;
 SELECT next_growth_at INTO STRICT deadline FROM loomplane_cluster_pool_growth WHERE org_id=p_org AND cloud_id=p_cloud AND resource_id=p_resource AND pool_id=p_pool;
 RETURN deadline;
END $$;
 CREATE FUNCTION loomplane_cluster_control_metadata() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
 IF TG_OP='INSERT' THEN RETURN NEW; END IF;
 PERFORM loomplane_cluster_control_supported(OLD.schema_version);
 IF NEW.schema_version<>OLD.schema_version OR NEW.created_at<>OLD.created_at THEN RAISE EXCEPTION 'cluster control identity is immutable' USING ERRCODE='23514'; END IF;
 IF (to_jsonb(NEW)-ARRAY['revision','updated_at']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['revision','updated_at']) OR NEW.revision<>OLD.revision THEN
 NEW.revision:=OLD.revision+1;NEW.updated_at:=greatest(clock_timestamp(),OLD.updated_at+interval '1 microsecond',NEW.created_at);
 ELSE NEW.revision:=OLD.revision;NEW.updated_at:=OLD.updated_at; END IF;
 RETURN NEW;
 END $$;
 CREATE FUNCTION loomplane_cluster_member_revision() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
 IF TG_OP='UPDATE' AND NEW IS NOT DISTINCT FROM OLD THEN RETURN NULL; END IF;
 IF TG_OP<>'INSERT' THEN UPDATE loomplane_cluster_demand_cohorts SET revision=revision+1 WHERE id=OLD.cohort_id; END IF;
 IF TG_OP='INSERT' OR TG_OP='UPDATE' AND NEW.cohort_id<>OLD.cohort_id THEN UPDATE loomplane_cluster_demand_cohorts SET revision=revision+1 WHERE id=NEW.cohort_id; END IF;
 RETURN NULL;
 END $$;
 CREATE TRIGGER loomplane_cluster_member_revision AFTER INSERT OR UPDATE OR DELETE ON loomplane_cluster_demand_members FOR EACH ROW EXECUTE FUNCTION loomplane_cluster_member_revision();`)
		if err != nil {
			return err
		}
		for _, table := range []string{"loomplane_cluster_demand_cohorts", "loomplane_cluster_pool_growth", "loomplane_cluster_pool_advice", "loomplane_cluster_capacity_inventory"} {
			if _, err := db.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER loomplane_cluster_control_metadata BEFORE INSERT OR UPDATE ON %s FOR EACH ROW EXECUTE FUNCTION loomplane_cluster_control_metadata()`, table)); err != nil {
				return err
			}
		}
		return nil
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN RAISE EXCEPTION 'versioned cluster control records must be retained'; END $$`)
		return err
	})
}
