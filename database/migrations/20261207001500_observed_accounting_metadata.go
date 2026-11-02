package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM loomplane_cluster_workload_observed_accounting WHERE org_id='' OR cloud_id='' OR resource_id='' OR revision<1 OR NOT isfinite(checked_at) OR jsonb_typeof(totals)<>'object'
 OR NOT totals ?& ARRAY['charged','actual','running','maximum'] OR totals-ARRAY['charged','actual','running','maximum']<>'{}'::jsonb
 OR jsonb_typeof(totals->'charged')<>'object' OR jsonb_typeof(totals->'actual')<>'object' OR jsonb_typeof(totals->'running')<>'object' OR jsonb_typeof(totals->'maximum')<>'object')
 THEN RAISE EXCEPTION 'legacy observed accounting requires qualified repair before versioning'; END IF;
 END $$;
 ALTER TABLE loomplane_cluster_workload_observed_accounting ADD COLUMN schema_version integer NOT NULL DEFAULT 1,
 ADD COLUMN created_at timestamptz NOT NULL DEFAULT clock_timestamp(), ADD COLUMN updated_at timestamptz NOT NULL DEFAULT clock_timestamp();
 UPDATE loomplane_cluster_workload_observed_accounting SET created_at=checked_at,updated_at=checked_at;
 CREATE OR REPLACE FUNCTION loomplane_cluster_controls_current(tenant text,resource text) RETURNS boolean LANGUAGE plpgsql STABLE AS $$
 BEGIN
 IF EXISTS(SELECT 1 FROM loomplane_cluster_demand_cohorts WHERE org_id=tenant AND (resource='' OR resource_id=resource) AND schema_version<>1)
 OR EXISTS(SELECT 1 FROM loomplane_cluster_pool_growth WHERE org_id=tenant AND (resource='' OR resource_id=resource) AND schema_version<>1)
 OR EXISTS(SELECT 1 FROM loomplane_cluster_pool_advice WHERE org_id=tenant AND (resource='' OR resource_id=resource) AND schema_version<>1)
 OR EXISTS(SELECT 1 FROM loomplane_cluster_capacity_inventory WHERE org_id=tenant AND (resource='' OR resource_id=resource) AND schema_version<>1)
 OR EXISTS(SELECT 1 FROM loomplane_cluster_workload_observed_accounting WHERE org_id=tenant AND (resource='' OR resource_id=resource) AND schema_version<>1)
 THEN RAISE EXCEPTION 'unsupported cluster control schema' USING ERRCODE='23514'; END IF;
 RETURN true;
 END $$;
 CREATE FUNCTION loomplane_observed_accounting_metadata() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
 PERFORM loomplane_cluster_control_supported(OLD.schema_version);
 PERFORM loomplane_cluster_control_supported(NEW.schema_version);
 IF NEW.org_id<>OLD.org_id OR NEW.cloud_id<>OLD.cloud_id OR NEW.resource_id<>OLD.resource_id OR NEW.created_at<>OLD.created_at
 OR NEW.revision<OLD.revision OR NEW.checked_at<OLD.checked_at OR NOT isfinite(NEW.checked_at)
 THEN RAISE EXCEPTION 'observed accounting identity or ordering changed' USING ERRCODE='23514'; END IF;
 IF NEW.revision=OLD.revision THEN
 IF NEW.totals IS DISTINCT FROM OLD.totals OR NEW.checked_at<>OLD.checked_at THEN RAISE EXCEPTION 'observed accounting revision must advance' USING ERRCODE='23514'; END IF;
 NEW.updated_at:=OLD.updated_at;
 ELSE NEW.updated_at:=greatest(clock_timestamp(),OLD.updated_at+interval '1 microsecond',NEW.created_at); END IF;
 RETURN NEW;
 END $$;
 CREATE TRIGGER loomplane_observed_accounting_metadata BEFORE UPDATE ON loomplane_cluster_workload_observed_accounting FOR EACH ROW EXECUTE FUNCTION loomplane_observed_accounting_metadata();`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN RAISE EXCEPTION 'versioned observed accounting must be retained'; END $$`)
		return err
	})
}
