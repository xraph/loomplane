//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
)

func benignClusterObservation(t *testing.T, store *cloudinfra.PgStore, resource string) {
	t.Helper()
	r, err := store.GetManagedResource(t.Context(), "org", resource)
	if err != nil {
		t.Fatal(err)
	}
	before, observed := r.Revision, r.ObservedRevision
	o := *r.Observation
	o.ObservedAt = time.Now().UTC()
	if err = store.SaveClusterScalingObservation(t.Context(), r, o); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetManagedResource(t.Context(), "org", resource)
	if err != nil || current.Revision != before || current.ObservedRevision != observed || !current.Observation.ObservedAt.Equal(o.ObservedAt) {
		t.Fatal("benign observation invalidated authority", err)
	}
}
func TestClusterObservationMaterialRevision(t *testing.T) {
	f, _, _, _, _ := managedDemandFixture(t)
	benignClusterObservation(t, f.store, "cluster")
	r, err := f.store.GetManagedResource(t.Context(), "org", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	revision := r.Revision
	o := *r.Observation
	o.Pools = append(o.Pools[:0:0], o.Pools...)
	o.Pools[0].Max++
	o.ObservedAt = time.Now().UTC()
	if err = f.store.SaveClusterScalingObservation(t.Context(), r, o); err != nil {
		t.Fatal(err)
	}
	current, err := f.store.GetManagedResource(t.Context(), "org", "cluster")
	if err != nil || current.Revision != revision+1 || current.ScalingPolicies["pool"].PauseReason == "" {
		t.Fatal("material change retained authority", err)
	}
}
