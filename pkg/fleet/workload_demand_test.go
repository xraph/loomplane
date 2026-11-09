package fleet

import (
	"math"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestFleetScaleIncludesSidecarsAndSurge(t *testing.T) {
	group := &provider.PodGroup{SurgeReplicas: 1, RuntimeStorageMiB: 64, Scratch: []provider.ScratchVolume{{Name: "cache", SizeMiB: 128}}, Sidecars: []provider.Sidecar{{Name: "helper", Image: "example.invalid/helper@sha256:" + strings.Repeat("a", 64), Architecture: "amd64", CPUMillis: 250, MemoryMiB: 128, Readiness: provider.ContainerReadiness{Port: 9000}}}}
	demand, err := ComputeWorkloadDemand([]ScalingWorkload{{CPUMillis: 500, MemoryMiB: 256, MaxReplicas: 4, PodGroup: group}})
	if err != nil {
		t.Fatal(err)
	}
	if demand.CPUMillis != 3750 || demand.MemoryBytes != 2013265920 || demand.EphemeralStorageBytes != 1342177280 || demand.Pods != 5 {
		t.Fatalf("incomplete pod demand: %+v", demand)
	}
}

func TestFleetScaleDefaultSurge(t *testing.T) {
	demand, err := ComputeWorkloadDemand([]ScalingWorkload{{CPUMillis: 500, MemoryMiB: 256, MaxReplicas: 5}})
	if err != nil {
		t.Fatal(err)
	}
	if demand.Pods != 7 || demand.CPUMillis != 3500 {
		t.Fatalf("default rollout surge omitted: %+v", demand)
	}
}

func TestFleetScaleGPUCeiling(t *testing.T) {
	demand, err := ComputeWorkloadDemand([]ScalingWorkload{{CPUMillis: 500, MemoryMiB: 256, MaxReplicas: 4, GPUKind: "gpu-a", GPUs: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if demand.GPUs != 8 || demand.GPUKind != "gpu-a" || demand.Pods != 4 {
		t.Fatalf("GPU recreate demand: %+v", demand)
	}
	_, err = ComputeWorkloadDemand([]ScalingWorkload{{CPUMillis: 500, MemoryMiB: 256, MaxReplicas: 4, GPUKind: "gpu-a", GPUs: 2}, {CPUMillis: 500, MemoryMiB: 256, MaxReplicas: 1, GPUKind: "gpu-b", GPUs: 1}})
	if err == nil {
		t.Fatal("mixed GPU kinds silently combined")
	}
}

func TestFleetScaleDemandRejectsOverflow(t *testing.T) {
	for _, workload := range []ScalingWorkload{
		{CPUMillis: 500, MemoryMiB: 256, MaxReplicas: math.MaxInt64},
		{CPUMillis: math.MaxInt64, MemoryMiB: 256, MaxReplicas: 2},
		{CPUMillis: 500, MemoryMiB: math.MaxInt64, MaxReplicas: 2},
		{CPUMillis: 500, MemoryMiB: 256, MaxReplicas: 1, GPUKind: "gpu-a", GPUs: math.MaxInt64},
		{CPUMillis: 500, MemoryMiB: 256, MaxReplicas: -1},
		{CPUMillis: 500, MemoryMiB: 256, MaxReplicas: 1, GPUs: 1},
	} {
		if _, err := ComputeWorkloadDemand([]ScalingWorkload{workload}); err == nil {
			t.Fatalf("invalid demand accepted: %+v", workload)
		}
	}
}

func TestFleetScaleAggregateDemandRejectsOverflow(t *testing.T) {
	w := ScalingWorkload{CPUMillis: 1, MemoryMiB: 256, MaxReplicas: 34359738367, PodGroup: &provider.PodGroup{}}
	if _, err := ComputeWorkloadDemand([]ScalingWorkload{w}); err != nil {
		t.Fatalf("single bounded vector rejected: %v", err)
	}
	if _, err := ComputeWorkloadDemand([]ScalingWorkload{w, w}); err == nil {
		t.Fatal("aggregate memory overflow accepted")
	}
}
