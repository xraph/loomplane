//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

// Supply is the approved policy fixture. The real observer supplies every
// workload, its scheduler condition, and its controller and release identities.
// This fixture never represents kind as live DigitalOcean capacity.
type cohortSupplyFixture struct {
	*cloudinfra.PgStore
	profile cloudinfra.ClusterSchedulingProfile
	nodes   []cloudinfra.ClusterNodeCapacity
}

func (s cohortSupplyFixture) SaveClusterCapacity(ctx context.Context, r *cloudinfra.ManagedResource, snapshot cloudinfra.ClusterCapacitySnapshot) error {
	snapshot.Complete = true
	snapshot.Nodes = s.nodes
	snapshot.SchedulingProfiles = []cloudinfra.ClusterSchedulingProfile{s.profile}
	return s.PgStore.SaveClusterCapacity(ctx, r, snapshot)
}
func TestClusterScalingInstalledPendingCohort(t *testing.T) {
	managedCleanupEffectsRealAPI(t, false, "application", "native-cohort")
}
func checkNativeCohort(ctx context.Context, t *testing.T, e *enrollmentServiceFixture, infra *cloudinfra.Service, resolver *planning.Service, f *poolFixture, profile cloudinfra.ClusterSchedulingProfile, id, depID string) {
	t.Helper()
	var raw []byte
	if err := f.sql.QueryRow(ctx, `SELECT body FROM loomplane_cluster_capacity_inventory WHERE org_id='org' AND resource_id='cluster'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var supply cloudinfra.ClusterCapacitySnapshot
	if err := json.Unmarshal(raw, &supply); err != nil || !supply.Complete || len(supply.Nodes) != 1 {
		t.Fatal("missing reviewed supply fixture", err)
	}
	observer := *infra
	observer.Clusters = cohortSupplyFixture{f.store, profile, supply.Nodes}
	adapters := cloudinfra.NewAdapters()
	if err := adapters.RegisterCluster("digitalocean", clusterCapacityKindReader{e.provider}, provider.ClusterDiscovery, provider.ClusterBootstrapAccess, provider.ClusterCapacityObservation); err != nil {
		t.Fatal(err)
	}
	observer.Adapters = adapters
	var first time.Time
	for range 2 {
		snap, err := observer.ObserveClusterCapacity(ctx, e.principal, "cloud", "cluster")
		if err != nil || snap.Complete {
			t.Fatal("kind provider mapping", err)
		}
		var created time.Time
		var count int
		err = f.sql.QueryRow(ctx, `SELECT c.created_at,(SELECT count(*) FROM loomplane_cluster_pool_growth) FROM loomplane_cluster_demand_cohorts c JOIN loomplane_cluster_demand_members m ON m.cohort_id=c.id WHERE m.demand_id=$1 AND m.reason='scheduler_shortage' AND m.observed_revision=$2 AND c.operation_id IS NULL AND c.closed_at IS NULL`, id, snap.ObservationRevision).Scan(&created, &count)
		if err != nil || count != 0 || !first.IsZero() && !created.Equal(first) {
			t.Fatal("pending cohort changed a node or duplicated demand", count, err)
		}
		decision, lookupErr := resolver.ReconcileClusterDemand(ctx, "org", "cloud", id)
		if lookupErr != nil || decision.Block != nil || decision.ClusterDemandState != "waiting_native_capacity" || decision.ClusterCohortID == "" || decision.ClusterPlacement == nil {
			t.Fatal("pending workload decision", decision, lookupErr)
		}
		first = created
	}
	// Without the supplied provider mapping the next observation withdraws the
	// cohort. It cannot release the already admitted workload maximum.
	observer.Clusters = f.store
	_, err := observer.ObserveClusterCapacity(ctx, e.principal, "cloud", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	var members, holds int
	if err := f.sql.QueryRow(ctx, `SELECT (SELECT count(*) FROM loomplane_cluster_demand_members WHERE demand_id=$1),(SELECT count(*) FROM loomplane_cluster_workload_holds WHERE id=$1 AND NOT released)`, id).Scan(&members, &holds); err != nil || members != 0 || holds != 1 {
		t.Fatal("incomplete evidence changed hold or retained demand", members, holds, err)
	}

	observer.Clusters = cohortSupplyFixture{f.store, profile, supply.Nodes}
	if _, err := observer.ObserveClusterCapacity(ctx, e.principal, "cloud", "cluster"); err != nil {
		t.Fatal(err)
	}
	if err := deployments.NewPgStore(f.db).Finish(ctx, "org", depID, deployments.StatusCancelled, "fixture cancellation", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.sql.QueryRow(ctx, `SELECT (SELECT count(*) FROM loomplane_cluster_demand_members WHERE demand_id=$1),(SELECT count(*) FROM loomplane_cluster_workload_holds WHERE id=$1 AND NOT released)`, id).Scan(&members, &holds); err != nil || members != 0 || holds != 1 {
		t.Fatal("cancel changed submitted reservation", members, holds, err)
	}
	if _, err := observer.ObserveClusterCapacity(ctx, e.principal, "cloud", "cluster"); err != nil {
		t.Fatal(err)
	}
	if err := f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_cluster_demand_members WHERE demand_id=$1`, id).Scan(&members); err != nil || members != 0 {
		t.Fatal("observer restored cancelled demand", members, err)
	}
}
