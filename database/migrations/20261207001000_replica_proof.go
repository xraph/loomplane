package migrations

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	App.Add(func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `CREATE TABLE loomplane_cluster_workload_observed_accounting(org_id text NOT NULL,cloud_id text NOT NULL,resource_id text NOT NULL,revision bigint NOT NULL,checked_at timestamptz NOT NULL,totals jsonb NOT NULL,PRIMARY KEY(org_id,resource_id));ALTER TABLE loomplane_managed_workload_issuances ADD COLUMN replica_spec_mac bytea CHECK(replica_spec_mac IS NULL OR coalesce(octet_length(replica_spec_mac)=32 AND cleanup_grant_id IS NULL AND outcome IS NOT NULL AND outcome->>'State'='accepted' AND request->>'Resource' IN ('deployments','horizontalpodautoscalers') AND request->>'Method' IN ('POST','PUT'),false));
CREATE FUNCTION loomplane_keep_replica_proof() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.outcome IS NOT NULL AND OLD.replica_spec_mac IS DISTINCT FROM NEW.replica_spec_mac THEN RAISE EXCEPTION 'accepted replica evidence is immutable'; END IF; RETURN NEW; END $$;
CREATE TRIGGER loomplane_keep_replica_proof BEFORE UPDATE OF replica_spec_mac ON loomplane_managed_workload_issuances FOR EACH ROW EXECUTE FUNCTION loomplane_keep_replica_proof();`)
		return err
	}, func(ctx context.Context, db *migrate.DB) error {
		_, err := db.Exec(ctx, `DO $$ BEGIN IF EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances WHERE replica_spec_mac IS NOT NULL) THEN RAISE EXCEPTION 'replica evidence must be retained';END IF;END $$;DROP TRIGGER loomplane_keep_replica_proof ON loomplane_managed_workload_issuances;DROP FUNCTION loomplane_keep_replica_proof();ALTER TABLE loomplane_managed_workload_issuances DROP COLUMN replica_spec_mac;DROP TABLE loomplane_cluster_workload_observed_accounting;`)
		return err
	})
}
