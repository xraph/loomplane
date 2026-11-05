package fleets

import (
	"math"

	"github.com/xraph/loomplane/pkg/fleet"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// FleetDemand reserves the sealed profile's maximum, including every container
// and lifecycle job. Data-resource reservations remain with resource bindings.
func FleetDemand(snapshot InstanceSnapshot) (fleet.ResourceDemand, error) {
	digest, err := SnapshotDigest(&snapshot)
	if err != nil || snapshot.Digest == "" || digest != snapshot.Digest {
		return fleet.ResourceDemand{}, ErrInvalid
	}
	profile, ok := snapshot.Template.Profiles[snapshot.ProfileID]
	if !ok {
		return fleet.ResourceDemand{}, ErrInvalid
	}
	return templateScalingDemand(snapshot.Template, profile)
}

// templateWorkloadDemand uses the same peak requests as planning. Existing
// create grants have no GPU bounds, so GPU workloads require separate review.
func templateWorkloadDemand(template fleet.Template, profile fleet.Profile) (OperationDemand, error) {
	resources, err := templateScalingDemand(template, profile)
	if err != nil {
		return OperationDemand{}, err
	}
	if resources.GPUs > 0 {
		return OperationDemand{}, ErrScalingQualification
	}
	storage := resources.EphemeralStorageBytes / (1 << 30)
	if resources.EphemeralStorageBytes%(1<<30) != 0 {
		storage++
	}
	return OperationDemand{CPUMillis: resources.CPUMillis, MemoryMiB: resources.MemoryBytes / (1 << 20), StorageGiB: storage}, nil
}

func templateScalingDemand(template fleet.Template, profile fleet.Profile) (fleet.ResourceDemand, error) {
	if len(profile.Services) != len(template.Manifest.Services) {
		return fleet.ResourceDemand{}, ErrInvalid
	}
	workloads := make([]fleet.ScalingWorkload, 0, len(profile.Services))
	for _, name := range template.Manifest.ServiceNames() {
		service := template.Manifest.Services[name]
		size, ok := profile.Services[name]
		if !ok || size.MinReplicas < 1 || size.MaxReplicas < size.MinReplicas || size.MaxReplicas > manifest.MaxInstances {
			return fleet.ResourceDemand{}, ErrInvalid
		}
		group := provider.ClonePodGroup(service.PodGroup)
		if group == nil {
			group = &provider.PodGroup{}
		}
		if group.RuntimeStorageMiB == 0 {
			group.RuntimeStorageMiB = provider.DefaultInstanceContainerStorageMiB
		}
		if group.RuntimeStorageMiB < provider.DefaultInstanceContainerStorageMiB {
			return fleet.ResourceDemand{}, ErrInvalid
		}
		workload := fleet.ScalingWorkload{CPUMillis: size.CPUMillis, MemoryMiB: size.MemoryMiB, MaxReplicas: int64(size.MaxReplicas), PodGroup: group}
		if service.Size != nil && service.Size.GPU != nil {
			workload.GPUKind = service.Size.GPU.Kind
			workload.GPUs = int64(service.Size.GPU.Count)
		}
		workloads = append(workloads, workload)
	}
	demand, err := fleet.ComputeWorkloadDemand(workloads)
	if err != nil {
		return fleet.ResourceDemand{}, err
	}
	for _, job := range template.Jobs {
		cpu, memory := job.EffectiveResources()
		if _, _, _, err = provider.PodGroupResources(cpu, memory, nil); err != nil {
			return fleet.ResourceDemand{}, err
		}
		memoryBytes := memory * (1 << 20)
		storageBytes := provider.DefaultInstanceContainerStorageMiB * (1 << 20)
		if demand.CPUMillis > math.MaxInt64-cpu || demand.MemoryBytes > math.MaxInt64-memoryBytes || demand.EphemeralStorageBytes > math.MaxInt64-storageBytes || demand.Pods == math.MaxInt64 {
			return fleet.ResourceDemand{}, ErrInvalid
		}
		demand.CPUMillis += cpu
		demand.MemoryBytes += memoryBytes
		demand.EphemeralStorageBytes += storageBytes
		demand.Pods++
	}
	return demand, nil
}
