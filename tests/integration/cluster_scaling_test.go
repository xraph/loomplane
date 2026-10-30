//go:build integration

package integration

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestClusterScalingExternalBreachPersists(t *testing.T) {
	db := managedClusterDB(t)
	store, _ := managedClusterStore(t, db)
	now := time.Now().UTC()
	ref := provider.ClusterRef{ID: "physical", AccountID: "account", Incarnation: "birth", Region: "region"}
	policy := cloudinfra.ClusterScalingPolicy{ID: "scaling-policy", OrgID: "org", ResourceID: "cluster", SchemaVersion: 1, Revision: 1, CreatedAt: now, UpdatedAt: now, OperationID: "review", PoolID: "pool", Mode: "native", MinNodes: 0, MaxNodes: 3, CooldownSeconds: 600, ContinuityPoolID: "system", ContinuityFloor: 1}
	resource := &cloudinfra.ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ExternalID: ref.ID, Incarnation: ref.Incarnation, Ownership: "managed", Revision: 1, SchemaVersion: 1, CreatedAt: now, UpdatedAt: now, ScalingPolicies: map[string]cloudinfra.ClusterScalingPolicy{"pool": policy}, Observation: &provider.ClusterObservation{Ref: ref, Endpoint: "https://cluster.example", CAHash: "trust", State: "running", ObservedAt: now, Pools: []provider.NodePoolSpec{{ID: "pool", Count: 1, Autoscale: true, Min: 0, Max: 3}, {ID: "system", Count: 1}}}}
	if _, err := store.ClaimManagedResource(t.Context(), resource, 0); err != nil {
		t.Fatal(err)
	}
	if checkErr := store.ValidateClusterScalingObserver(t.Context(), "org", "cloud", "cluster", "pool", 1); checkErr != nil {
		t.Fatal(checkErr)
	}
	dependencies, err := store.ClusterRetirementDependencies(t.Context(), "org", "cluster", "", "")
	if err != nil || !slices.Contains(dependencies, "native_policy:pool") {
		t.Fatal(dependencies, err)
	}
	observation := *resource.Observation
	observation.Pools = append([]provider.NodePoolSpec(nil), observation.Pools...)
	observation.Pools[1].Count = 0
	observation.ObservedAt = time.Now().UTC()
	if checkErr := store.SaveClusterScalingObservation(t.Context(), resource, observation); checkErr != nil {
		t.Fatal(checkErr)
	}
	saved, err := store.GetManagedResource(t.Context(), "org", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	if got := saved.ScalingPolicies["pool"]; got.PauseReason != "continuity_floor_lost" || got.Revision != 1 || got.Revoked {
		t.Fatalf("breach changed authority or failed to pause: %+v", got)
	}
	// A pause retains observation and retirement protection, without deleting nodes.
	if checkErr := store.ValidateClusterScalingObserver(t.Context(), "org", "cloud", "cluster", "pool", 1); checkErr != nil {
		t.Fatal(checkErr)
	}
	if checkErr := store.ValidateClusterScalingObserver(t.Context(), "org", "cloud", "cluster", "pool", 2); !errors.Is(checkErr, cloudinfra.ErrConflict) {
		t.Fatal(checkErr)
	}
	if checkErr := store.ValidateClusterScalingObserver(t.Context(), "other", "cloud", "cluster", "pool", 1); !errors.Is(checkErr, cloudinfra.ErrNotFound) {
		t.Fatal(checkErr)
	}
	// The service only publishes revocation after native disable convergence.
	prior := saved.Revision
	saved.Revision++
	revoked := saved.ScalingPolicies["pool"]
	revoked.Revision++
	revoked.Mode = "manual"
	revoked.Revoked = true
	saved.ScalingPolicies["pool"] = revoked
	if _, checkErr := store.ClaimManagedResource(t.Context(), saved, prior); checkErr != nil {
		t.Fatal(checkErr)
	}
	if checkErr := store.ValidateClusterScalingObserver(t.Context(), "org", "cloud", "cluster", "pool", 1); !errors.Is(checkErr, cloudinfra.ErrConflict) {
		t.Fatal(checkErr)
	}
	dependencies, err = store.ClusterRetirementDependencies(t.Context(), "org", "cluster", "", "")
	if err != nil || slices.Contains(dependencies, "native_policy:pool") {
		t.Fatal(dependencies, err)
	}
}
