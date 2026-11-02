package fleets

import (
	"github.com/xraph/loomplane/pkg/fleet"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// templateWorkloadDemand includes peak pod requests and every declared job.
// Scratch uses the existing storage budget, rounded up once across the instance.
func templateWorkloadDemand(template fleet.Template, profile fleet.Profile) (OperationDemand, error) {
	var demand OperationDemand
	var scratch int64
	for name, size := range profile.Services {
		service, ok := template.Manifest.Services[name]
		if !ok || size.MaxReplicas < 1 || size.MaxReplicas > manifest.MaxInstances {
			return demand, ErrInvalid
		}
		group := provider.ClonePodGroup(service.PodGroup)
		if group == nil {
			group = &provider.PodGroup{}
		}
		if group.RuntimeStorageMiB == 0 {
			group.RuntimeStorageMiB = provider.DefaultInstanceContainerStorageMiB
		}
		if group.RuntimeStorageMiB < provider.DefaultInstanceContainerStorageMiB {
			return demand, ErrInvalid
		}
		cpu, memory, disk, err := provider.PodGroupResources(size.CPUMillis, size.MemoryMiB, group)
		if err != nil {
			return demand, err
		}
		replicas := int64(size.MaxReplicas)
		if service.PodGroup != nil {
			replicas += int64(service.PodGroup.SurgeReplicas)
		}
		demand.CPUMillis += cpu * replicas
		demand.MemoryMiB += memory * replicas
		scratch += disk * replicas
	}
	for _, job := range template.Jobs {
		cpu, memory := job.EffectiveResources()
		demand.CPUMillis += cpu
		demand.MemoryMiB += memory
		scratch += provider.DefaultInstanceContainerStorageMiB
	}
	demand.StorageGiB = (scratch + 1023) / 1024
	return demand, nil
}
