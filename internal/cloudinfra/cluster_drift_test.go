package cloudinfra

import (
	"context"
	"testing"
)

type driftMemory struct{ *clusterMemory }

func (m *driftMemory) ClaimManagedResource(_ context.Context, r *ManagedResource, expected int64) (*ManagedResource, error) {
	if m.resource.Revision != expected {
		return nil, ErrConflict
	}
	m.resource = r
	return r, nil
}
func TestClusterRetirementDriftSeparatesDesiredRevision(t *testing.T) {
	s, base, adapter, _, p, _ := maintenanceFixture(t)
	before := *base.resource.Observation
	base.resource.Observation = &before
	base.resource.DesiredRevision = 7
	s.Clusters = &driftMemory{base}
	adapter.observation.Version = "external-upgrade"
	r, err := s.ObserveClusterDrift(t.Context(), p, "cloud", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	if r.DesiredRevision != 7 || r.ObservedRevision != 2 || r.Revision != 2 || r.Status != "drifted" || adapter.effects != 0 {
		t.Fatal(r)
	}
	again, err := s.ObserveClusterDrift(t.Context(), p, "cloud", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != r.Revision {
		t.Fatal("unchanged observation invalidated reviews")
	}
	adapter.observation.Ref.Incarnation = "replacement"
	changed, err := s.ObserveClusterDrift(t.Context(), p, "cloud", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	if changed.Status != "identity_changed" || changed.Incarnation == "replacement" || changed.Observation.Ref.Incarnation == "replacement" {
		t.Fatal("replacement trusted", changed)
	}
}
