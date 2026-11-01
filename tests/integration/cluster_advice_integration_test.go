//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
)

func TestClusterScalingAdviceFreshness(t *testing.T) {
	f, _, _, _, _ := managedDemandFixture(t)
	r, err := f.store.GetManagedResource(t.Context(), "org", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = f.sql.QueryRow(t.Context(), `SELECT body FROM loomplane_cluster_capacity_inventory WHERE resource_id='cluster'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var snap cloudinfra.ClusterCapacitySnapshot
	if err = json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	r.Observation.State = "running"
	r.Observation.Pools[0].Count = 2
	r.Revision++
	if _, err = f.store.ClaimManagedResource(t.Context(), r, r.Revision-1); err != nil {
		t.Fatal(err)
	}
	snap.Nodes[0].Requests.Pods = inventoryNumber(0)
	n := snap.Nodes[0]
	n.ExternalVMID = "second-fixture-node"
	n.NodeUID = "second-fixture-node"
	snap.Nodes = append(snap.Nodes, n)
	snap.Workloads = nil
	observe := func() {
		t.Helper()
		rev, e := f.store.BeginClusterCapacityObservation(t.Context(), "org", "cloud", "cluster")
		if e != nil {
			t.Fatal(e)
		}
		snap.ObservationRevision = rev
		snap.CheckedAt = time.Now().UTC()
		snap.StorageProvenance.CheckedAt = snap.CheckedAt
		for i := range snap.Nodes {
			snap.Nodes[i].Provenance = map[string]cloudinfra.ClusterMetricProvenance{"requests/pods": {Source: "kubernetes", Identity: snap.Nodes[i].NodeUID, CheckedAt: snap.CheckedAt}}
		}
		if e = f.store.SaveClusterCapacity(t.Context(), r, snap); e != nil {
			t.Fatal(e)
		}
	}
	advice := func(want bool) {
		t.Helper()
		v, e := f.store.CurrentClusterPoolAdvice(t.Context(), "org", "cloud", "cluster", "pool")
		if e != nil || v != want {
			t.Fatal("decrease review", v, want, e)
		}
	}
	observe()
	advice(false)
	// Model the earlier observations without waiting thirty wall-clock minutes.
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_pool_advice SET low_since=now()-interval '31 minutes',observed_at=now()-interval '1 minute' WHERE resource_id='cluster' AND pool_id='pool'`); err != nil {
		t.Fatal(err)
	}
	observe()
	advice(true)
	if _, err = f.store.BeginClusterCapacityObservation(t.Context(), "org", "cloud", "cluster"); err != nil {
		t.Fatal(err)
	}
	advice(false)
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_pool_advice SET observed_at=now()-interval '3 minutes' WHERE resource_id='cluster' AND pool_id='pool'`); err != nil {
		t.Fatal(err)
	}
	observe()
	advice(false)
	if err = f.store.DelayScalingObservation(t.Context(), "org", "cloud", "cluster", "pool", 1, 7*time.Minute); err != nil {
		t.Fatal(err)
	}
	deadline, err := f.store.ScalingObservationDeadline(t.Context(), "org", "cloud", "cluster", "pool")
	if err != nil || time.Until(deadline) < 6*time.Minute {
		t.Fatal(deadline, err)
	}
	scopes, err := f.store.ListClusterScalingObservers(t.Context())
	if err != nil || len(scopes) != 0 {
		t.Fatal("throttled observer still listed", scopes, err)
	}
	if err = f.store.DelayScalingObservation(t.Context(), "org", "cloud", "cluster", "pool", 2, time.Hour); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("stale policy extended throttle", err)
	}
	// Manual ownership can recommend a review after native authority was
	// revoked and the real pool was observed with autoscaling off.
	currentPolicy := r.ScalingPolicies["pool"]
	currentPolicy.Mode = "manual"
	currentPolicy.Revoked = true
	currentPolicy.MinNodes = 0
	currentPolicy.MaxNodes = 0
	currentPolicy.Revision++
	r.ScalingPolicies["pool"] = currentPolicy
	r.Observation.Pools[0].Autoscale = false
	r.Revision++
	if _, err = f.store.ClaimManagedResource(t.Context(), r, r.Revision-1); err != nil {
		t.Fatal(err)
	}
	observe()
	advice(false)
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_cluster_pool_advice SET low_since=now()-interval '31 minutes', observed_at=now()-interval '1 minute',retry_observation_at=now()-interval '1 second' WHERE resource_id='cluster' AND pool_id='pool'`); err != nil {
		t.Fatal(err)
	}
	observe()
	advice(true)
	var effects int
	if err = f.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_cluster_pool_growth`).Scan(&effects); err != nil || effects != 0 {
		t.Fatal("recommendation issued growth", effects, err)
	}
}
