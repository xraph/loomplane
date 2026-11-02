package cloudinfra

import "math"

// ClusterWorkloadVector counts scheduling requests independently of node quotas.
// GPU, topology, taints and volume reachability require a separate scheduling check.
type ClusterWorkloadVector struct {
	CPUMillis             int64 `json:"cpu_millis"`
	MemoryBytes           int64 `json:"memory_bytes"`
	EphemeralStorageBytes int64 `json:"ephemeral_storage_bytes"`
	Pods                  int64 `json:"pods"`
}

type clusterWorkloadCoverage struct {
	Maximum  ClusterWorkloadVector
	Observed ClusterWorkloadVector
}

func workloadDimensions(v ClusterWorkloadVector) [4]int64 {
	return [4]int64{v.CPUMillis, v.MemoryBytes, v.EphemeralStorageBytes, v.Pods}
}

// Coverage is recomputed from one complete snapshot. It never releases a hold
// permanently: missing or replaced proof restores the full conservative hold.
func clusterWorkloadUsage(observed ClusterWorkloadVector, holds []clusterWorkloadCoverage) (ClusterWorkloadVector, error) {
	used := workloadDimensions(observed)
	for _, value := range used {
		if value < 0 {
			return ClusterWorkloadVector{}, ErrInvalid
		}
	}
	for _, hold := range holds {
		maximum, matched := workloadDimensions(hold.Maximum), workloadDimensions(hold.Observed)
		for i, value := range maximum {
			if value < 0 || matched[i] < 0 {
				return ClusterWorkloadVector{}, ErrInvalid
			}
			remainder := value - min(value, matched[i])
			if remainder > math.MaxInt64-used[i] {
				return ClusterWorkloadVector{}, ErrInvalid
			}
			used[i] += remainder
		}
	}
	return ClusterWorkloadVector{CPUMillis: used[0], MemoryBytes: used[1], EphemeralStorageBytes: used[2], Pods: used[3]}, nil
}
