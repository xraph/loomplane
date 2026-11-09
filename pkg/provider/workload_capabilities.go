package provider

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

const PodGroupCapabilityRevision = "kubernetes-pod-group-v1"

// WorkloadCapabilities describes a checked runtime protocol, not an assertion
// supplied by a deployment request. Revision is pinned in the reviewed plan.
type WorkloadCapabilities struct {
	Revision       string   `json:"revision" yaml:"revision"`
	PodGroups      bool     `json:"pod_groups" yaml:"pod_groups"`
	ScratchVolumes bool     `json:"scratch_volumes" yaml:"scratch_volumes"`
	LifecycleJobs  bool     `json:"lifecycle_jobs" yaml:"lifecycle_jobs"`
	ScalingSignals []string `json:"scaling_signals,omitempty" yaml:"scaling_signals,omitempty"`
}

// PodGroup shares only its declared scratch volumes and selected environment
// keys. Every sidecar is required for readiness and scales with the main process.
type PodGroup struct {
	IsolationRevision string          `json:"isolation_revision,omitempty" yaml:"isolation_revision,omitempty"`
	RuntimeStorageMiB int64           `json:"runtime_storage_mib,omitempty" yaml:"runtime_storage_mib,omitempty"`
	Sidecars          []Sidecar       `json:"sidecars,omitempty" yaml:"sidecars,omitempty"`
	Scratch           []ScratchVolume `json:"scratch,omitempty" yaml:"scratch,omitempty"`
	Mounts            []ScratchMount  `json:"mounts,omitempty" yaml:"mounts,omitempty"`
	SurgeReplicas     int             `json:"surge_replicas" yaml:"surge_replicas"`
}
type Sidecar struct {
	Name         string             `json:"name" yaml:"name"`
	Image        string             `json:"image" yaml:"image"`
	Architecture string             `json:"architecture" yaml:"architecture"`
	CPUMillis    int64              `json:"cpu_millis" yaml:"cpu_millis"`
	MemoryMiB    int64              `json:"memory_mib" yaml:"memory_mib"`
	Command      []string           `json:"command,omitempty" yaml:"command,omitempty"`
	Args         []string           `json:"args,omitempty" yaml:"args,omitempty"`
	Environment  []string           `json:"environment,omitempty" yaml:"environment,omitempty"`
	Readiness    ContainerReadiness `json:"readiness" yaml:"readiness"`
	Mounts       []ScratchMount     `json:"mounts,omitempty" yaml:"mounts,omitempty"`
}
type ContainerReadiness struct {
	Port int    `json:"port" yaml:"port"`
	Path string `json:"path,omitempty" yaml:"path,omitempty"`
}
type ScratchVolume struct {
	Name    string `json:"name" yaml:"name"`
	SizeMiB int64  `json:"size_mib" yaml:"size_mib"`
}
type ScratchMount struct {
	Volume   string `json:"volume" yaml:"volume"`
	Path     string `json:"path" yaml:"path"`
	ReadOnly bool   `json:"read_only,omitempty" yaml:"read_only,omitempty"`
}

var ErrWorkloadSpec = errors.New("invalid workload specification")
var workloadName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)
var workloadImage = regexp.MustCompile(`^[a-z0-9][a-z0-9.:/_-]*@sha256:[a-f0-9]{64}$`)
var workloadEnv = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func ValidatePodGroup(group *PodGroup) error {
	if group == nil {
		return nil
	}
	if group.IsolationRevision != "" && (group.IsolationRevision != InstanceIsolationRevision || group.RuntimeStorageMiB < DefaultInstanceContainerStorageMiB) {
		return ErrWorkloadSpec
	}
	if group.RuntimeStorageMiB < 0 || group.RuntimeStorageMiB > 4096 || len(group.Sidecars) > 8 || len(group.Scratch) > 8 || group.SurgeReplicas < 0 || group.SurgeReplicas > 10 {
		return ErrWorkloadSpec
	}
	volumes := map[string]bool{}
	for _, v := range group.Scratch {
		if !workloadName.MatchString(v.Name) || v.Name == "runtime-files" || v.Name == "dshm" || volumes[v.Name] || v.SizeMiB < 1 || v.SizeMiB > 65536 {
			return ErrWorkloadSpec
		}
		volumes[v.Name] = true
	}
	if err := validateScratchMounts(group.Mounts, volumes); err != nil {
		return err
	}
	names := map[string]bool{"main": true}
	for _, sidecar := range group.Sidecars {
		if !workloadName.MatchString(sidecar.Name) || names[sidecar.Name] || !workloadImage.MatchString(sidecar.Image) || strings.ContainsAny(sidecar.Image, "\x00") || (sidecar.Architecture != "amd64" && sidecar.Architecture != "arm64") || sidecar.CPUMillis < 1 || sidecar.CPUMillis > 8000 || sidecar.MemoryMiB < 16 || sidecar.MemoryMiB > 32768 {
			return ErrWorkloadSpec
		}
		names[sidecar.Name] = true
		if sidecar.Readiness.Port < 1 || sidecar.Readiness.Port > 65535 || len(sidecar.Readiness.Path) > 256 || (sidecar.Readiness.Path != "" && (!strings.HasPrefix(sidecar.Readiness.Path, "/") || strings.ContainsAny(sidecar.Readiness.Path, "\x00\r\n"))) {
			return ErrWorkloadSpec
		}
		if err := validateScratchMounts(sidecar.Mounts, volumes); err != nil {
			return err
		}
		for _, args := range [][]string{sidecar.Command, sidecar.Args} {
			if len(args) > 64 {
				return ErrWorkloadSpec
			}
			for _, arg := range args {
				if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
					return ErrWorkloadSpec
				}
			}
		}
		if len(sidecar.Environment) > 128 {
			return ErrWorkloadSpec
		}
		keys := map[string]bool{}
		for _, key := range sidecar.Environment {
			if !workloadEnv.MatchString(key) || keys[key] {
				return ErrWorkloadSpec
			}
			keys[key] = true
		}
	}
	return nil
}
func validateScratchMounts(mounts []ScratchMount, volumes map[string]bool) error {
	if len(mounts) > 16 {
		return ErrWorkloadSpec
	}
	paths := map[string]bool{}
	for _, mount := range mounts {
		if !volumes[mount.Volume] || !path.IsAbs(mount.Path) || path.Clean(mount.Path) != mount.Path || mount.Path == "/" || len(mount.Path) > 256 || strings.ContainsAny(mount.Path, "\x00\\") || paths[mount.Path] {
			return ErrWorkloadSpec
		}
		for _, reserved := range []string{"/proc", "/sys", "/dev", "/var/run/secrets"} {
			if mount.Path == reserved || strings.HasPrefix(mount.Path, reserved+"/") {
				return ErrWorkloadSpec
			}
		}
		for existing := range paths {
			if strings.HasPrefix(mount.Path, existing+"/") || strings.HasPrefix(existing, mount.Path+"/") {
				return ErrWorkloadSpec
			}
		}
		paths[mount.Path] = true
	}
	return nil
}

// PodGroupResources returns per-pod requests, including shared scratch once.
func PodGroupResources(mainCPU, mainMemory int64, group *PodGroup) (cpu, memory, scratch int64, err error) {
	if mainCPU < 1 || mainMemory < 1 || mainCPU > 8000 || mainMemory > 32768 {
		return 0, 0, 0, ErrWorkloadSpec
	}
	if checkErr := ValidatePodGroup(group); checkErr != nil {
		return 0, 0, 0, checkErr
	}
	cpu, memory = mainCPU, mainMemory
	if group == nil {
		return cpu, memory, 0, nil
	}
	for _, sidecar := range group.Sidecars {
		cpu += sidecar.CPUMillis
		memory += sidecar.MemoryMiB
	}
	for _, volume := range group.Scratch {
		scratch += volume.SizeMiB
	}
	scratch += group.RuntimeStorageMiB * int64(1+len(group.Sidecars))
	return cpu, memory, scratch, nil
}
func CheckPodGroupArchitecture(group *PodGroup, architecture string) error {
	if group == nil {
		return nil
	}
	for _, sidecar := range group.Sidecars {
		if sidecar.Architecture != architecture {
			return fmt.Errorf("%w: sidecar architecture does not match the selected runtime", ErrWorkloadSpec)
		}
	}
	return nil
}

// ClonePodGroup detaches every mutable field before planning or rendering.
func ClonePodGroup(group *PodGroup) *PodGroup {
	if group == nil {
		return nil
	}
	out := *group
	out.Sidecars = slices.Clone(group.Sidecars)
	out.Scratch = slices.Clone(group.Scratch)
	out.Mounts = slices.Clone(group.Mounts)
	for i := range out.Sidecars {
		sidecar := &out.Sidecars[i]
		sidecar.Command = slices.Clone(sidecar.Command)
		sidecar.Args = slices.Clone(sidecar.Args)
		sidecar.Environment = slices.Clone(sidecar.Environment)
		sidecar.Mounts = slices.Clone(sidecar.Mounts)
	}
	return &out
}
