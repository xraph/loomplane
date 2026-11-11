package usage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/fleet"
)

type fleetMetricIdentity struct{}

func (fleetMetricIdentity) GetInstance(_ context.Context, org, id string) (*fleet.Instance, error) {
	if org != "org" || id != "instance" {
		return nil, fleets.ErrNotFound
	}
	return &fleet.Instance{ID: id, FleetID: "fleet", EnvironmentID: "env"}, nil
}

type fleetMetricBindings struct {
	calls int
	items []*planning.Binding
}

func (s *fleetMetricBindings) ListBindings(_ context.Context, org, env string) ([]*planning.Binding, error) {
	s.calls++
	if org != "org" || env != "env" {
		return nil, errors.New("foreign bindings read")
	}
	return s.items, nil
}
func TestFleetMetricsAdapterScope(t *testing.T) {
	bindings := &fleetMetricBindings{}
	reader := NewFleetMetricSource(&Service{}, fleetMetricIdentity{}, bindings)
	now := time.Now()
	w := fleet.MetricWindow{Start: now.Add(-time.Hour), End: now}
	for _, scope := range []fleets.MetricScope{
		{OrgID: "other", FleetID: "fleet", InstanceID: "instance", EnvironmentID: "env"},
		{OrgID: "org", FleetID: "other", InstanceID: "instance", EnvironmentID: "env"},
		{OrgID: "org", FleetID: "fleet", InstanceID: "instance", EnvironmentID: "other"},
	} {
		if _, err := reader.ReadMetrics(t.Context(), scope, w); !errors.Is(err, fleets.ErrNotFound) {
			t.Fatalf("foreign scope: %v", err)
		}
	}
	if bindings.calls != 0 {
		t.Fatal("foreign scope reached bindings")
	}
}

func TestFleetMetricsAdapterPartialProviderObservations(t *testing.T) {
	observed := time.Now().UTC().Add(-time.Minute)
	w := fleet.MetricWindow{Start: observed.Add(-time.Hour), End: observed.Add(time.Minute)}
	bindings := &fleetMetricBindings{items: []*planning.Binding{
		{ID: "service-a", OrgID: "org", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "web", Provider: "cloud-run", ConnectionID: "connection"},
		{ID: "service-b", OrgID: "org", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "worker", Provider: "kubernetes", ConnectionID: "connection"},
	}}
	meter := &fakeMeter{fn: func(binding *planning.Binding, window metrics.Window) (*metrics.Measurement, error) {
		if binding.ID != "service-a" || !window.Start.Equal(w.Start) || !window.End.Equal(w.End) {
			t.Fatal("meter received another scope or window")
		}
		return &metrics.Measurement{Requests: []metrics.Point{{T: observed, V: 42}}, Latency: 50, Statistic: metrics.P95}, nil
	}}
	reads := NewService(Deps{Connections: &fakeConnections{list: []*connections.Connection{conn("connection", connections.GCP, connections.StatusReady)}}, Meters: map[string]Meter{"cloud-run": meter}})
	reader := NewFleetMetricSource(reads, fleetMetricIdentity{}, bindings)
	batch, err := reader.ReadMetrics(t.Context(), fleets.MetricScope{OrgID: "org", FleetID: "fleet", InstanceID: "instance", EnvironmentID: "env"}, w)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := fleets.AggregateMetrics(w, time.Now().UTC(), batch)
	if err != nil || summary.Requests == nil || *summary.Requests != 42 || summary.CPUUtilization != nil || summary.LatencyP95Millis != nil || !summary.Partial || summary.Coverage.Expected != 2 || summary.Coverage.Requests != 1 {
		t.Fatalf("partial provider: %+v %v", summary, err)
	}
	if len(summary.Sources) != 2 || summary.Sources[0].ObservedAt == nil || !summary.Sources[0].ObservedAt.Equal(observed) || summary.Sources[1].Reason != "source_unavailable" {
		t.Fatalf("provenance: %+v", summary.Sources)
	}
}

func TestFleetMetricsAdapterValidatesAllBindingsBeforeCredentials(t *testing.T) {
	bindings := &fleetMetricBindings{items: []*planning.Binding{
		{ID: "first", OrgID: "org", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "web", Provider: "cloud-run", ConnectionID: "connection"},
		{ID: "foreign", OrgID: "other", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "worker", Provider: "cloud-run", ConnectionID: "connection"},
	}}
	cons := &fakeConnections{list: []*connections.Connection{conn("connection", connections.GCP, connections.StatusReady)}}
	reads := NewService(Deps{Connections: cons, Meters: map[string]Meter{}})
	reader := NewFleetMetricSource(reads, fleetMetricIdentity{}, bindings)
	now := time.Now()
	_, err := reader.ReadMetrics(t.Context(), fleets.MetricScope{OrgID: "org", FleetID: "fleet", InstanceID: "instance", EnvironmentID: "env"}, fleet.MetricWindow{Start: now.Add(-time.Hour), End: now})
	if !errors.Is(err, fleets.ErrNotFound) || cons.reveals != 0 {
		t.Fatalf("foreign binding accessed credentials: %v, %d", err, cons.reveals)
	}
}
