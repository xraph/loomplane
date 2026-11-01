//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/planning"
)

// Substitute only the provider policy fixture. Demand resolution uses the
// installed cloudinfra -> planning -> PostgreSQL call path.
type cohortPolicyFixture struct {
	*cloudinfra.Service
	policy cloudinfra.ClusterScalingPolicy
}

func (f cohortPolicyFixture) CurrentClusterScalingPolicy(ctx context.Context, org, cloud, resource, pool string) (*cloudinfra.ClusterScalingPolicy, error) {
	return managedDemandAuthority{policy: f.policy}.CurrentClusterScalingPolicy(ctx, org, cloud, resource, pool)
}

// Attach two simultaneous immutable demands to the same pending review.
// Neither a wait nor its cancellation acquires a hold or issues a node change.
func TestClusterScalingCohortDedup(t *testing.T) {
	f, _, p, result, auth := managedDemandFixture(t)
	r, err := f.store.GetManagedResource(t.Context(), "org", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	route := cloudinfra.NewService(cloudinfra.Deps{Clusters: f.store, ManagedClusters: config.ManagedClustersConfig{Enabled: true}, ClusterCapacity: config.ClusterCapacityConfig{Enabled: true}})
	svc := planning.NewService(planning.Deps{Store: f.plans, Authz: auth, Capacity: cohortPolicyFixture{route, r.ScalingPolicies["pool"]}})
	route.CapacityDemandAuthority = svc
	svc.BindManagedRuntime(ledgerRuntimeFixture{})
	result.Actions[0].Service.Scale.Max = 90
	if err := svc.PrepareClusterWorkloadDemands(t.Context(), p, result); err != nil {
		t.Fatal(err)
	}
	saveManagedDemandPlan(t, f, p, result)
	id1 := result.Actions[0].Service.ClusterDemand.Input.ID
	another := *p
	another.ID = "second-managed-wait"
	cfg := *result.Actions[0].Service
	cfg.ClusterDemand = nil
	action := result.Actions[0]
	action.Service = &cfg
	second := *result
	second.Actions = []planning.Action{action}
	if err := svc.PrepareClusterWorkloadDemands(t.Context(), &another, &second); err != nil {
		t.Fatal(err)
	}
	saveManagedDemandPlan(t, f, &another, &second)
	var before time.Time
	ids := []string{id1, cfg.ClusterDemand.Input.ID}
	type res struct {
		d cloudinfra.CapacityDecision
		e error
	}
	ch := make(chan res, len(ids))
	for _, id := range ids {
		go func() { d, e := route.ReconcileClusterDemand(t.Context(), "org", "cloud", id); ch <- res{d, e} }()
	}
	var group string
	for range ids {
		x := <-ch
		if x.e != nil || x.d.Block == nil || x.d.ClusterCohortID == "" || group != "" && group != x.d.ClusterCohortID {
			t.Fatal(x)
		}
		group = x.d.ClusterCohortID
	}
	if err := f.sql.QueryRow(t.Context(), `SELECT created_at FROM loomplane_cluster_demand_cohorts WHERE id=$1`, group).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		d, err := svc.ReconcileClusterDemand(t.Context(), "org", "cloud", id)
		if err != nil || d.ClusterCohortID != group {
			t.Fatal(d, err)
		}
	}
	if err := svc.ResolveCapacityIntents(t.Context(), p.OrgID, p.ID); err == nil {
		t.Fatal("installed demand resolver bypassed review")
	}
	var members, ops int
	var after time.Time
	if err := f.sql.QueryRow(t.Context(), `SELECT c.created_at,(SELECT count(*) FROM loomplane_cluster_demand_members WHERE cohort_id=c.id),(SELECT count(*) FROM loomplane_cluster_pool_growth) FROM loomplane_cluster_demand_cohorts c WHERE c.id=$1 AND c.operation_id IS NULL AND c.closed_at IS NULL`, group).Scan(&after, &members, &ops); err != nil || members != 2 || ops != 0 || !after.Equal(before) {
		t.Fatal("duplicate wake changed cohort", members, ops, err)
	}
}
