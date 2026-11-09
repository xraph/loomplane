package manifest

import (
	"regexp"
	"slices"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
)

// Workload declarations share the same validated contract with runtime adapters.
type PodGroup = provider.PodGroup
type Sidecar = provider.Sidecar
type ScratchVolume = provider.ScratchVolume
type ScratchMount = provider.ScratchMount
type ContainerReadiness = provider.ContainerReadiness

// LifecycleJob is an operator-authored release step. Dependencies select named
// resource bindings; they never grant provider or Kubernetes administration.
type LifecycleJob struct {
	Phase          string   `json:"phase" yaml:"phase"`
	Image          string   `json:"image" yaml:"image"`
	Command        []string `json:"command,omitempty" yaml:"command,omitempty"`
	Args           []string `json:"args,omitempty" yaml:"args,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds" yaml:"timeout_seconds"`
	MaxAttempts    int      `json:"max_attempts" yaml:"max_attempts"`
	Required       bool     `json:"required,omitempty" yaml:"required,omitempty"`
	SchemaLock     string   `json:"schema_lock,omitempty" yaml:"schema_lock,omitempty"`
	Dependencies   []string `json:"dependencies,omitempty" yaml:"dependencies,omitempty"`
	CPUMillis      int64    `json:"cpu_millis,omitempty" yaml:"cpu_millis,omitempty"`
	MemoryMiB      int64    `json:"memory_mib,omitempty" yaml:"memory_mib,omitempty"`
	Architecture   string   `json:"architecture,omitempty" yaml:"architecture,omitempty"`
}

// EffectiveResources preserves schema-1 releases that predate explicit job sizes.
// The resulting values are pinned in the execution receipt before issuance.
func (j LifecycleJob) EffectiveResources() (cpu, memory int64) {
	cpu, memory = j.CPUMillis, j.MemoryMiB
	if cpu == 0 {
		cpu = 500
	}
	if memory == 0 {
		memory = 256
	}
	return cpu, memory
}

func (j LifecycleJob) Validate() error {
	cpu, memory := j.EffectiveResources()
	if !lifecycleImage.MatchString(j.Image) || j.TimeoutSeconds < 1 || j.TimeoutSeconds > 3600 || j.MaxAttempts < 1 || j.MaxAttempts > 5 || cpu < 1 || cpu > 8000 || memory < 16 || memory > 32768 || len(j.SchemaLock) > 128 || strings.ContainsAny(j.SchemaLock, "\x00\r\n") || len(j.Command) > 32 || len(j.Args) > 64 || len(j.Dependencies) > 32 {
		return provider.ErrWorkloadSpec
	}
	if !slices.Contains([]string{"pre-deploy", "pre_deploy", "bootstrap", "verify"}, j.Phase) || j.Architecture != "" && j.Architecture != "arm64" && j.Architecture != "amd64" {
		return provider.ErrWorkloadSpec
	}
	for _, args := range [][]string{j.Command, j.Args} {
		for _, arg := range args {
			if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
				return provider.ErrWorkloadSpec
			}
		}
	}
	dependencies := map[string]bool{}
	for _, dependency := range j.Dependencies {
		if dependency == "" || len(dependency) > 128 || dependencies[dependency] || strings.ContainsAny(dependency, "\x00\r\n") {
			return provider.ErrWorkloadSpec
		}
		dependencies[dependency] = true
	}
	return nil
}

var lifecycleImage = regexp.MustCompile(`^[a-z0-9][a-z0-9.:/_-]*@sha256:[a-f0-9]{64}$`)
