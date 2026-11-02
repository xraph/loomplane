package planning

import (
	"cmp"
	"math"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/pkg/provider"
)

// OwnedMainResources matches the main container requests sent to the runtime.
// Memory is in MiB here, matching the runtime service specification.
func OwnedMainResources(cfg *ServiceConfig) (cpu, memory int) {
	if cfg.Catalog != nil {
		cpu, memory = 1000, 512
	}
	if cfg.Component != nil {
		cpu, memory = cmp.Or(cfg.Component.Component.Resources.CPUMillis, 500), cmp.Or(cfg.Component.Component.Resources.MemoryMB, 256)
	}
	if cfg.Size != nil {
		cpu, memory = int(cfg.Size.CPU*1000), cfg.Size.Memory
	}
	if cfg.Scale != nil && cfg.Scale.Min != cfg.Scale.Max && cpu == 0 {
		cpu = 100
	}
	return cpu, memory
}

// OwnedWorkloadResources includes every container in one pod.
func OwnedWorkloadResources(cfg *ServiceConfig) (cpu, memory int) {
	cpu, memory = OwnedMainResources(cfg)
	if cfg.PodGroup == nil {
		return cpu, memory
	}
	totalCPU, totalMemory, _, err := provider.PodGroupResources(int64(cpu), int64(memory), cfg.PodGroup)
	if err != nil {
		return 0, 0
	}
	return int(totalCPU), int(totalMemory)
}

func managedWorkloadMaximum(cfg *ServiceConfig) (cloudinfra.ClusterWorkloadVector, error) {
	cpu, memory := OwnedMainResources(cfg)
	replicas := 1
	if cfg.Scale != nil {
		replicas = cfg.Scale.Max
	}
	blocked := &cloudinfra.CapacityAdmissionError{Block: cloudinfra.CapacityBlock{Reason: "workload_requests_required"}}
	if replicas < 1 {
		return cloudinfra.ClusterWorkloadVector{}, blocked
	}
	var scratch int64
	if cfg.PodGroup != nil {
		totalCPU, totalMemory, totalScratch, err := provider.PodGroupResources(int64(cpu), int64(memory), cfg.PodGroup)
		if err != nil {
			return cloudinfra.ClusterWorkloadVector{}, err
		}
		cpu, memory, scratch = int(totalCPU), int(totalMemory), totalScratch
		if replicas > math.MaxInt-cfg.PodGroup.SurgeReplicas {
			return cloudinfra.ClusterWorkloadVector{}, blocked
		}
		replicas += cfg.PodGroup.SurgeReplicas
	}
	count := int64(replicas)
	if cpu <= 0 || memory <= 0 || int64(cpu) > math.MaxInt64/count || int64(memory) > math.MaxInt64/(1<<20)/count || scratch > math.MaxInt64/(1<<20)/count {
		return cloudinfra.ClusterWorkloadVector{}, blocked
	}
	return cloudinfra.ClusterWorkloadVector{CPUMillis: int64(cpu) * count, MemoryBytes: int64(memory) * (1 << 20) * count, Pods: count, EphemeralStorageBytes: scratch * (1 << 20) * count}, nil
}
