package fleets

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

func metricTestWindow() (fleet.MetricWindow, time.Time) {
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	return fleet.MetricWindow{Start: now.Add(-time.Hour), End: now}, now
}
func floatMetric(v float64) *float64 { return &v }

func TestFleetMetricsUnknownNotZero(t *testing.T) {
	w, now := metricTestWindow()
	got, err := AggregateMetrics(w, now, MetricBatch{Expected: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUUtilization != nil || got.MemoryBytes != nil || got.LatencyP95Millis != nil || !got.Partial || got.Freshness != "unknown" {
		t.Fatalf("missing telemetry: %+v", got)
	}
}
func TestFleetMetricsWeightedCPU(t *testing.T) {
	w, now := metricTestWindow()
	got, err := AggregateMetrics(w, now, MetricBatch{Expected: 2, Samples: []MetricSample{
		{Component: "small", ObservedAt: now, CPUUsageMillicores: floatMetric(100), CPURequestMillicores: floatMetric(100)},
		{Component: "large", ObservedAt: now, CPUUsageMillicores: floatMetric(100), CPURequestMillicores: floatMetric(900)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUUtilization == nil || math.Abs(*got.CPUUtilization-0.2) > 1e-10 {
		t.Fatalf("weighted CPU: %+v", got)
	}
}
func TestFleetMetricsNoAverageP95(t *testing.T) {
	w, now := metricTestWindow()
	got, err := AggregateMetrics(w, now, MetricBatch{Expected: 2, Samples: []MetricSample{
		{Component: "a", ObservedAt: now, LatencyP95Millis: floatMetric(10)},
		{Component: "b", ObservedAt: now, LatencyP95Millis: floatMetric(100)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.LatencyP95Millis != nil || got.LatencyHistogram != nil {
		t.Fatalf("fabricated latency distribution: %+v", got)
	}
}
func TestFleetMetricsHistogramAndPartialCoverage(t *testing.T) {
	w, now := metricTestWindow()
	got, err := AggregateMetrics(w, now, MetricBatch{Expected: 3, Samples: []MetricSample{
		{Component: "a", ObservedAt: now, LatencyHistogram: &fleet.LatencyHistogram{UpperBoundsMillis: []float64{10, 100}, Counts: []uint64{9, 1, 0}}},
		{Component: "b", ObservedAt: now, LatencyHistogram: &fleet.LatencyHistogram{UpperBoundsMillis: []float64{10, 100}, Counts: []uint64{1, 9, 0}}},
		{Component: "stale", ObservedAt: now.Add(-10 * time.Minute), CPUUsageMillicores: floatMetric(0), CPURequestMillicores: floatMetric(100)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.LatencyHistogram == nil || got.LatencyHistogram.Counts[0] != 10 || got.LatencyHistogram.Counts[1] != 10 || got.LatencyP95Millis == nil || *got.LatencyP95Millis != 100 || !got.Partial || got.CPUUtilization != nil || got.Coverage.Fresh != 2 {
		t.Fatalf("aggregate: %+v", got)
	}
}
func TestFleetMetricsRejectInvalidInput(t *testing.T) {
	w, now := metricTestWindow()
	for _, batch := range []MetricBatch{
		{Expected: 1, Samples: []MetricSample{{Component: "a", ObservedAt: now, CPUUsageMillicores: floatMetric(math.NaN())}}},
		{Expected: 1, Samples: []MetricSample{{Component: "a", ObservedAt: now, MemoryBytes: floatMetric(-1)}}},
		{Expected: 2, Samples: []MetricSample{{Component: "a", ObservedAt: now}, {Component: "a", ObservedAt: now}}},
		{Expected: 1, Samples: []MetricSample{{Component: "a", ObservedAt: now, LatencyHistogram: &fleet.LatencyHistogram{UpperBoundsMillis: []float64{100, 10}, Counts: []uint64{1, 2, 0}}}}},
	} {
		if _, err := AggregateMetrics(w, now, batch); err == nil {
			t.Fatalf("accepted invalid batch: %+v", batch)
		}
	}
}

func TestFleetMetricsIncompatibleHistograms(t *testing.T) {
	w, now := metricTestWindow()
	got, err := AggregateMetrics(w, now, MetricBatch{Expected: 2, Samples: []MetricSample{
		{Component: "a", ObservedAt: now, LatencyHistogram: &fleet.LatencyHistogram{UpperBoundsMillis: []float64{10, 100}, Counts: []uint64{9, 1, 0}}},
		{Component: "b", ObservedAt: now, LatencyHistogram: &fleet.LatencyHistogram{UpperBoundsMillis: []float64{20, 200}, Counts: []uint64{1, 9, 0}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.LatencyHistogram != nil || got.LatencyP95Millis != nil || got.Coverage.Latency != 0 || got.Reason != "histogram_incompatible" {
		t.Fatalf("incompatible histograms merged: %+v", got)
	}
}
func TestFleetMetricsFutureObservations(t *testing.T) {
	w, now := metricTestWindow()
	got, err := AggregateMetrics(w, now, MetricBatch{Expected: 1, Samples: []MetricSample{{Component: "future", ObservedAt: now.Add(time.Second), CPUUsageMillicores: floatMetric(100), CPURequestMillicores: floatMetric(100)}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUUtilization != nil || got.Coverage.Fresh != 0 || !got.Partial {
		t.Fatalf("future metric accepted: %+v", got)
	}
}

func TestFleetMetricsOverflowAndUnboundedLatency(t *testing.T) {
	w, now := metricTestWindow()
	if _, err := AggregateMetrics(w, now, MetricBatch{Expected: 1, Samples: []MetricSample{{Component: "overflow", ObservedAt: now, CPUUsageMillicores: floatMetric(1e308), CPURequestMillicores: floatMetric(1e-308)}}}); err == nil {
		t.Fatal("infinite CPU ratio accepted")
	}
	got, err := AggregateMetrics(w, now, MetricBatch{Expected: 1, Samples: []MetricSample{{Component: "tail", ObservedAt: now, LatencyHistogram: &fleet.LatencyHistogram{UpperBoundsMillis: []float64{10}, Counts: []uint64{0, 100}}}}})
	if err != nil || got.LatencyP95Millis != nil || got.LatencyHistogram == nil {
		t.Fatalf("unbounded latency: %+v %v", got, err)
	}
}

func TestFleetMetricsHistoricalWindow(t *testing.T) {
	_, now := metricTestWindow()
	w := fleet.MetricWindow{Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour)}
	got, err := AggregateMetrics(w, now, MetricBatch{Expected: 1, Samples: []MetricSample{{Component: "web", ObservedAt: w.End.Add(-time.Minute), CPUUsageMillicores: floatMetric(50), CPURequestMillicores: floatMetric(100)}}})
	if err != nil || got.CPUUtilization == nil || *got.CPUUtilization != .5 || got.Freshness != "historical" {
		t.Fatalf("historical observation discarded: %+v %v", got, err)
	}
}

type metricFixtureSource struct{ fail bool }

func (m metricFixtureSource) ReadMetrics(_ context.Context, scope MetricScope, w fleet.MetricWindow) (MetricBatch, error) {
	if scope.OrgID != "org" || scope.FleetID != "fleet" || scope.InstanceID != "instance" {
		return MetricBatch{}, errors.New("bad scope")
	}
	if m.fail {
		return MetricBatch{}, errors.New("provider credential=synthetic-secret")
	}
	return MetricBatch{Expected: 1, Samples: []MetricSample{{Component: "web", ObservedAt: w.End, CPUUsageMillicores: floatMetric(25), CPURequestMillicores: floatMetric(100)}}}, nil
}
func TestFleetMetricsAccessAndRedaction(t *testing.T) {
	keys := &readVerifier{}
	s := NewService(Deps{Store: readFixtureStore{}, Authz: readAuthorizer{}, Keys: keys, Metrics: metricFixtureSource{}})
	p := security.Principal{OrgID: "org", UserID: security.FleetSubject("key"), KeyID: "key", Kind: security.KindFleet, Fleet: &security.FleetScope{ProjectID: "project", FleetID: "fleet"}}
	now := time.Now().UTC()
	w := fleet.MetricWindow{Start: now.Add(-time.Hour), End: now}
	got, err := s.Metrics(t.Context(), p, "instance", w)
	if err != nil || got.CPUUtilization == nil || *got.CPUUtilization != .25 {
		t.Fatalf("metrics: %+v %v", got, err)
	}
	keys.err = security.ErrForbidden
	if _, err = s.Metrics(t.Context(), p, "instance", w); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("revoked: %v", err)
	}
	keys.err = nil
	s.metrics = metricFixtureSource{fail: true}
	got, err = s.Metrics(t.Context(), p, "instance", w)
	if err != nil || got.Reason != "source_failed" || got.CPUUtilization != nil {
		t.Fatalf("failed source: %+v %v", got, err)
	}
	s.metrics = nil
	got, err = s.Metrics(t.Context(), p, "instance", w)
	if err != nil || got.Reason != "source_unavailable" {
		t.Fatalf("missing source: %+v %v", got, err)
	}
}
