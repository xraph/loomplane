package cloudinfra

import "github.com/xraph/loomplane/pkg/fleet"

// FleetWorkload carries the reviewed per-pod requests and replica ceiling.
type FleetWorkload = fleet.ScalingWorkload

// FleetDemandVectors preserves GPU and pod scheduling dimensions. Admission must
// check both vectors; node capacity alone does not prove a pod is schedulable.
type FleetDemandVectors struct {
	Resources ResourceVector
	Workload  ClusterWorkloadVector
}

// FleetWorkloadDemand converts peak requests into the existing admission units.
func FleetWorkloadDemand(workloads []FleetWorkload) (FleetDemandVectors, error) {
	demand, err := fleet.ComputeWorkloadDemand(workloads)
	if err != nil {
		return FleetDemandVectors{}, err
	}
	return FleetDemandVectors{
		Resources: ResourceVector{CPUMillis: demand.CPUMillis, MemoryBytes: demand.MemoryBytes, StorageBytes: demand.EphemeralStorageBytes, GPUKind: demand.GPUKind, GPUs: demand.GPUs},
		Workload:  ClusterWorkloadVector{CPUMillis: demand.CPUMillis, MemoryBytes: demand.MemoryBytes, EphemeralStorageBytes: demand.EphemeralStorageBytes, Pods: demand.Pods},
	}, nil
}
