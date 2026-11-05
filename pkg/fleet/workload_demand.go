package fleet

import (
	"errors"
	"math"
	"regexp"

	"github.com/xraph/loomplane/pkg/provider"
)

// ErrScalingDemand identifies incomplete requests or arithmetic overflow.
var ErrScalingDemand = errors.New("invalid fleet workload demand")
var scalingGPUKind = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ScalingWorkload uses the main container's requests and approved replica ceiling.
// Pod groups include their explicit surge. Other CPU deployments reserve the
// runtime's default 25 percent surge; GPU deployments use its recreate strategy.
type ScalingWorkload struct {
	CPUMillis   int64
	MemoryMiB   int64
	MaxReplicas int64
	PodGroup    *provider.PodGroup
	GPUKind     string
	GPUs        int64
}

// ResourceDemand retains scheduler dimensions without depending on Cloud stores.
// Different GPU kinds need separate reservations and cannot share this vector.
type ResourceDemand struct {
	CPUMillis             int64  `json:"cpu_millis"`
	MemoryBytes           int64  `json:"memory_bytes"`
	EphemeralStorageBytes int64  `json:"ephemeral_storage_bytes"`
	Pods                  int64  `json:"pods"`
	GPUKind               string `json:"gpu_kind,omitempty"`
	GPUs                  int64  `json:"gpus,omitempty"`
}

// ComputeWorkloadDemand sums peak requests and refuses mixed GPU kinds.
func ComputeWorkloadDemand(workloads []ScalingWorkload) (ResourceDemand, error) {
	var total ResourceDemand
	if len(workloads) == 0 {
		return total, ErrScalingDemand
	}
	for _, workload := range workloads {
		part, err := scalingWorkloadDemand(workload)
		if err != nil {
			return ResourceDemand{}, err
		}
		if part.GPUKind != "" {
			if total.GPUKind != "" && total.GPUKind != part.GPUKind {
				return ResourceDemand{}, ErrScalingDemand
			}
			total.GPUKind = part.GPUKind
		}
		for _, dimension := range []struct {
			target *int64
			value  int64
		}{
			{&total.CPUMillis, part.CPUMillis}, {&total.MemoryBytes, part.MemoryBytes}, {&total.EphemeralStorageBytes, part.EphemeralStorageBytes}, {&total.Pods, part.Pods}, {&total.GPUs, part.GPUs},
		} {
			if dimension.value > math.MaxInt64-*dimension.target {
				return ResourceDemand{}, ErrScalingDemand
			}
			*dimension.target += dimension.value
		}
	}
	return total, nil
}

func scalingWorkloadDemand(w ScalingWorkload) (ResourceDemand, error) {
	if w.MaxReplicas < 1 || w.GPUs < 0 || w.GPUs > 8 || (w.GPUs == 0) != (w.GPUKind == "") || w.GPUKind != "" && !scalingGPUKind.MatchString(w.GPUKind) {
		return ResourceDemand{}, ErrScalingDemand
	}
	cpu, memory, scratch, err := provider.PodGroupResources(w.CPUMillis, w.MemoryMiB, w.PodGroup)
	if err != nil {
		return ResourceDemand{}, err
	}
	surge := w.MaxReplicas / 4
	if w.MaxReplicas%4 != 0 {
		surge++
	}
	if w.PodGroup != nil {
		surge = int64(w.PodGroup.SurgeReplicas)
	}
	if w.GPUs > 0 {
		surge = 0
	}
	if surge > math.MaxInt64-w.MaxReplicas {
		return ResourceDemand{}, ErrScalingDemand
	}
	count := w.MaxReplicas + surge
	if cpu > math.MaxInt64/count || memory > math.MaxInt64/(1<<20)/count || scratch > math.MaxInt64/(1<<20)/count || w.GPUs > math.MaxInt64/count {
		return ResourceDemand{}, ErrScalingDemand
	}
	return ResourceDemand{CPUMillis: cpu * count, MemoryBytes: memory * (1 << 20) * count, EphemeralStorageBytes: scratch * (1 << 20) * count, Pods: count, GPUKind: w.GPUKind, GPUs: w.GPUs * count}, nil
}
