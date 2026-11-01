//go:build integration

package integration

import (
	"testing"
	"time"
)

func TestClusterControlUnsupportedVersion(t *testing.T) {
	for _, kind := range []string{"cohort", "growth", "advice", "inventory"} {
		t.Run(kind, func(t *testing.T) {
			f, svc, p, result, _ := managedDemandFixture(t)
			svc.BindManagedRuntime(ledgerRuntimeFixture{})
			if err := svc.PrepareClusterWorkloadDemands(t.Context(), p, result); err != nil {
				t.Fatal(err)
			}
			saveManagedDemandPlan(t, f, p, result)
			var query string
			switch kind {
			case "cohort":
				query = `INSERT INTO loomplane_cluster_demand_cohorts(id,org_id,cloud_id,resource_id,pool_id,profile,policy_revision,budget_revision,native,schema_version) VALUES('historical-uuid-preserved','org','cloud','cluster','pool','future',1,1,true,2)`
			case "growth":
				query = `INSERT INTO loomplane_cluster_pool_growth(org_id,cloud_id,resource_id,pool_id,operation_id,issued_at,next_growth_at,schema_version) VALUES('org','cloud','cluster','pool','future',now(),now()+interval '10 minutes',2)`
			case "advice":
				if _, err := f.sql.Exec(t.Context(), `DELETE FROM loomplane_cluster_pool_advice WHERE resource_id='cluster'`); err != nil {
					t.Fatal(err)
				}
				query = `INSERT INTO loomplane_cluster_pool_advice(org_id,cloud_id,resource_id,pool_id,policy_revision,observed_at,schema_version) VALUES('org','cloud','cluster','pool',1,now(),2)`
			case "inventory":
				query = `WITH old AS (DELETE FROM loomplane_cluster_capacity_inventory WHERE resource_id='cluster' RETURNING *) INSERT INTO loomplane_cluster_capacity_inventory(org_id,cloud_id,resource_id,issued_revision,saved_revision,checked_at,complete,body,schema_version) SELECT org_id,cloud_id,resource_id,issued_revision,saved_revision,checked_at,complete,body,2 FROM old`
			}
			if _, err := f.sql.Exec(t.Context(), query); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ReconcileClusterDemand(t.Context(), "org", "cloud", result.Actions[0].Service.ClusterDemand.Input.ID); err == nil {
				t.Fatal("future control version admitted")
			}
			if _, err := f.store.CurrentClusterPoolAdvice(t.Context(), "org", "cloud", "cluster", "pool"); err == nil {
				t.Fatal("future control version returned advice")
			}
			if _, err := f.store.ScalingObservationDeadline(t.Context(), "org", "cloud", "cluster", "pool"); err == nil {
				t.Fatal("future control version allowed observation")
			}
			if _, err := f.store.BeginClusterCapacityObservation(t.Context(), "org", "cloud", "cluster"); err == nil {
				t.Fatal("old writer changed future control")
			}
			if _, err := f.sql.Exec(t.Context(), `SELECT loomplane_claim_pool_growth('org','cloud','cluster','pool','new',600)`); err == nil {
				t.Fatal("old growth writer accepted future control")
			}
			var held int
			if err := f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cluster_workload_holds`).Scan(&held); err != nil || held != 0 {
				t.Fatal("version refusal created hold", held, err)
			}
		})
	}
}
func TestClusterGrowthMetadataRetry(t *testing.T) {
	db := managedClusterDB(t)
	if _, err := db.Exec(t.Context(), `SELECT loomplane_claim_pool_growth('org','cloud','cluster','pool','one',600)`); err != nil {
		t.Fatal(err)
	}
	var revision int64
	var updated time.Time
	if err := db.QueryRow(t.Context(), `SELECT revision,updated_at FROM loomplane_cluster_pool_growth WHERE schema_version=1`).Scan(&revision, &updated); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `SELECT loomplane_claim_pool_growth('org','cloud','cluster','pool','retry',600)`); err != nil {
		t.Fatal(err)
	}
	var same int64
	var after time.Time
	if err := db.QueryRow(t.Context(), `SELECT revision,updated_at FROM loomplane_cluster_pool_growth`).Scan(&same, &after); err != nil || same != revision || !after.Equal(updated) {
		t.Fatal("cooldown retry changed metadata", err)
	}
}
