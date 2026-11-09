package planning

import (
	"cmp"
	"math"
	"reflect"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

func fleetCapacityPlan(plan *Plan) bool {
	return plan != nil && (plan.Kind == KindInstance || plan.ManagedInstanceID != "" || plan.FleetInput != nil)
}

func checkFleetCapacityMode(plan *Plan, cfg *ServiceConfig) error {
	if !fleetCapacityPlan(plan) {
		return nil
	}
	if plan.Kind != KindInstance || plan.ManagedInstanceID == "" || plan.FleetInput == nil || cfg == nil {
		return ErrInvalid
	}
	if cfg.CapacityMode != "" && cfg.CapacityMode != "guaranteed" || cfg.ClusterDemand != nil && cfg.ClusterDemand.Mode != "guaranteed" {
		return workloadBlock("fleet_capacity_mode_unqualified")
	}
	return nil
}

// managedPlanWorkloadMaximum separates the reservation ceiling from the runtime
// replica writer. Both review and execution derive it from the sealed snapshot.
func managedPlanWorkloadMaximum(plan *Plan, service string, cfg *ServiceConfig) (cloudinfra.ClusterWorkloadVector, error) {
	if plan == nil || cfg == nil {
		return cloudinfra.ClusterWorkloadVector{}, ErrInvalid
	}
	if plan.Kind != KindInstance {
		if plan.ManagedInstanceID != "" {
			return cloudinfra.ClusterWorkloadVector{}, ErrInvalid
		}
		return managedWorkloadMaximum(cfg)
	}
	in := plan.FleetInput
	if in == nil || plan.ManagedInstanceID == "" {
		return cloudinfra.ClusterWorkloadVector{}, ErrInvalid
	}
	snapshot, authority := in.Snapshot, in.Authority
	digest, err := fleets.SnapshotDigest(&snapshot)
	if err != nil || snapshot.Digest == "" || digest != snapshot.Digest || authority.Request.SnapshotDigest != digest || snapshot.InstanceID != plan.ManagedInstanceID || snapshot.EnvironmentID != plan.EnvironmentID || snapshot.FleetID != authority.Scope.FleetID || authority.Scope.OrgID != plan.OrgID || authority.Scope.ProjectID != plan.ProjectID || snapshot.ProfileID != authority.Request.ProfileID || snapshot.ReleaseID != authority.Request.ReleaseID || snapshot.Generation < 1 || snapshot.OwnershipEpoch < 1 {
		return cloudinfra.ClusterWorkloadVector{}, ErrInvalid
	}
	profile, ok := snapshot.Template.Profiles[snapshot.ProfileID]
	if !ok {
		return cloudinfra.ClusterWorkloadVector{}, ErrInvalid
	}
	size, ok := profile.Services[service]
	if !ok || size.MinReplicas < 1 || size.MaxReplicas < size.MinReplicas || size.MaxReplicas > manifest.MaxInstances || cfg.Size == nil || cfg.Size.CPU != float64(size.CPUMillis)/1000 || int64(cfg.Size.Memory) != size.MemoryMiB {
		return cloudinfra.ClusterWorkloadVector{}, ErrInvalid
	}
	execution, err := instanceExecutionManifest(snapshot.Manifest)
	if err != nil {
		return cloudinfra.ClusterWorkloadVector{}, err
	}
	expected, ok := execution.Services[service]
	if !ok || !reflect.DeepEqual(cfg.Size, expected.Size) || !reflect.DeepEqual(cfg.Scale, expected.Scale) || !reflect.DeepEqual(cfg.PodGroup, expected.PodGroup) {
		return cloudinfra.ClusterWorkloadVector{}, ErrInvalid
	}
	if cfg.GPU != nil || cfg.Size.GPU != nil || cfg.Size.Shm > 0 || cfg.Catalog != nil || cfg.Component != nil {
		return cloudinfra.ClusterWorkloadVector{}, workloadBlock("workload_constraints_unsupported")
	}
	demand, err := cloudinfra.FleetWorkloadDemand([]cloudinfra.FleetWorkload{{CPUMillis: size.CPUMillis, MemoryMiB: size.MemoryMiB, MaxReplicas: int64(size.MaxReplicas), PodGroup: cfg.PodGroup}})
	if err != nil {
		return cloudinfra.ClusterWorkloadVector{}, err
	}
	return demand.Workload, nil
}

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
