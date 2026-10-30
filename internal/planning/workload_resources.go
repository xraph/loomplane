package planning

import (
	"cmp"
	"math"

	"github.com/xraph/loomplane/internal/cloudinfra"
)

// OwnedWorkloadResources matches the request values sent to the owned runtime.
// Memory is in MiB here, matching the runtime service specification.
func OwnedWorkloadResources(cfg *ServiceConfig) (cpu, memory int) {
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

func managedWorkloadMaximum(cfg *ServiceConfig) (cloudinfra.ClusterWorkloadVector, error) {
	cpu, memory := OwnedWorkloadResources(cfg)
	replicas := 1
	if cfg.Scale != nil {
		replicas = cfg.Scale.Max
	}
	if cpu <= 0 || memory <= 0 || replicas < 1 || int64(cpu) > math.MaxInt64/int64(replicas) || int64(memory) > math.MaxInt64/(1<<20)/int64(replicas) {
		return cloudinfra.ClusterWorkloadVector{}, &cloudinfra.CapacityAdmissionError{Block: cloudinfra.CapacityBlock{Reason: "workload_requests_required"}}
	}
	return cloudinfra.ClusterWorkloadVector{CPUMillis: int64(cpu) * int64(replicas), MemoryBytes: int64(memory) * (1 << 20) * int64(replicas), Pods: int64(replicas)}, nil
}
