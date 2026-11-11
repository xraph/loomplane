package fleets

import (
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/fleet"
)

func TestFleetRuntimeMetricsSignalTimesDoNotRewriteTrafficWindow(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	end := now.Add(-time.Second)
	cpu, request, memory := 125.0, 500.0, 64.0
	replicas := int64(1)
	sample := MetricSample{Component: "web", Source: "kubernetes", RuntimeLive: true, RuntimeObservedAt: now, ReplicasObservedAt: now, RuntimeIdentity: "pod_name_window", CPUUsageMillicores: &cpu, CPURequestMillicores: &request, MemoryBytes: &memory, DesiredReplicas: &replicas, CurrentReplicas: &replicas, ReadyReplicas: &replicas}
	got, err := AggregateMetrics(fleet.MetricWindow{Start: end.Add(-time.Hour), End: end}, now, MetricBatch{Expected: 1, Samples: []MetricSample{sample}})
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUUtilization == nil || *got.CPUUtilization != 0.25 || got.CurrentReplicas == nil || got.Requests != nil {
		t.Fatalf("live gauges: %+v", got)
	}
	if got.Sources[0].RuntimeObservedAt == nil || !got.Sources[0].RuntimeObservedAt.Equal(now) || got.Sources[0].ObservedAt != nil {
		t.Fatal("gauge observation timestamp was relabeled as traffic")
	}
	historical := fleet.MetricWindow{Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour)}
	old, err := AggregateMetrics(historical, now, MetricBatch{Expected: 1, Samples: []MetricSample{sample}})
	if err != nil {
		t.Fatal(err)
	}
	if old.CPUUtilization != nil || old.CurrentReplicas != nil {
		t.Fatal("current gauges leaked into historical window")
	}
}
func TestFleetRuntimeMetricsReplicaFreshnessIndependentOfCPU(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	cpu, request := 125.0, 500.0
	replicas := int64(1)
	sample := MetricSample{Component: "web", Source: "kubernetes", RuntimeLive: true, RuntimeObservedAt: now.Add(-10 * time.Minute), ReplicasObservedAt: now, CPUUsageMillicores: &cpu, CPURequestMillicores: &request, DesiredReplicas: &replicas, CurrentReplicas: &replicas, ReadyReplicas: &replicas}
	got, err := AggregateMetrics(fleet.MetricWindow{Start: now.Add(-time.Hour), End: now}, now, MetricBatch{Expected: 1, Samples: []MetricSample{sample}})
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUUtilization != nil || got.CurrentReplicas == nil || got.Coverage.Replicas != 1 || got.Coverage.CPU != 0 {
		t.Fatalf("freshness: %+v", got)
	}
}

func TestFleetRuntimeMetricsEmptyControllerKeepsKnownZero(t *testing.T) {
	now := time.Now().UTC()
	zero := float64(0)
	replicas := int64(0)
	got, err := AggregateMetrics(fleet.MetricWindow{Start: now.Add(-time.Minute), End: now}, now, MetricBatch{Expected: 1, Samples: []MetricSample{{Component: "api", Source: "kubernetes", RuntimeLive: true, RuntimeObservedAt: now, ReplicasObservedAt: now, RuntimeIdentity: "empty_controller", CPUUsageMillicores: &zero, CPURequestMillicores: &zero, MemoryBytes: &zero, DesiredReplicas: &replicas, CurrentReplicas: &replicas, ReadyReplicas: &replicas}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUUsageMillicores == nil || *got.CPUUsageMillicores != 0 || got.CPURequestMillicores == nil || *got.CPURequestMillicores != 0 || got.CPUUtilization != nil || got.Coverage.CPU != 1 {
		t.Fatalf("known zero became unknown: %+v", got)
	}
}
