package planning

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/pkg/manifest"
)

func planningPodGroup() *manifest.PodGroup {
	return &manifest.PodGroup{Sidecars: []manifest.Sidecar{{Name: "helper", Image: "registry.example.test/helper@sha256:" + strings.Repeat("a", 64), Architecture: "arm64", CPUMillis: 250, MemoryMiB: 128, Readiness: manifest.ContainerReadiness{Port: 9001}}}, SurgeReplicas: 1}
}
func TestPodGroupAccountsEveryContainer(t *testing.T) {
	cfg := &ServiceConfig{Size: &manifest.Size{CPU: 0.5, Memory: 256}, Scale: &manifest.Scale{Min: 1, Max: 2}, PodGroup: planningPodGroup()}
	cpu, memory := OwnedWorkloadResources(cfg)
	if cpu != 750 || memory != 384 {
		t.Fatalf("pod totals: %d/%d", cpu, memory)
	}
	maximum, err := managedWorkloadMaximum(cfg)
	if err != nil || maximum.CPUMillis != 2250 || maximum.MemoryBytes != 384*(1<<20)*3 || maximum.Pods != 3 {
		t.Fatalf("surge not reserved: %+v %v", maximum, err)
	}
	mainCPU, mainMemory := OwnedMainResources(cfg)
	if mainCPU != 500 || mainMemory != 256 {
		t.Fatal("sidecars charged twice to main container")
	}
}
func TestUnsupportedPodGroupBlocksPlan(t *testing.T) {
	m := &manifest.Manifest{Version: 1, Services: map[string]manifest.Service{"api": {Source: "api", Kind: "http-service", Settings: manifest.Settings{Provider: "server", Target: targetA, Port: 8080, Public: new(false), Build: &manifest.Build{Image: "registry.example.test/api@sha256:" + strings.Repeat("a", 64)}, PodGroup: planningPodGroup()}}}}
	result := Compute(Input{Manifest: m, Graph: &graph.Graph{Version: graph.SchemaVersion, Services: []graph.Service{{ID: "svc:api", Name: "api", Kind: "http-service", Deployable: true, Ecosystem: "docker", Path: "api", Entrypoint: "api"}}}, Environment: "production", Providers: infrastructureProviders(t), Connections: infrastructureConnections(t)})
	found := false
	for _, finding := range result.Problems {
		if finding.Code == "workload-capability" {
			found = true
		}
	}
	if result.Executable || !found {
		t.Fatalf("unsupported target accepted pod group: %+v", result.Problems)
	}
}

func TestPodGroupMaximumRejectsInvalidReplicaAndScratchRequests(t *testing.T) {
	for _, replicas := range []int{-1, 0, int(^uint(0) >> 1)} {
		cfg := &ServiceConfig{Size: &manifest.Size{CPU: 0.5, Memory: 256}, Scale: &manifest.Scale{Min: 1, Max: replicas}, PodGroup: planningPodGroup()}
		cfg.PodGroup.SurgeReplicas = 2
		if _, err := managedWorkloadMaximum(cfg); err == nil {
			t.Fatalf("invalid base replicas %d accepted", replicas)
		}
	}
	cfg := &ServiceConfig{Size: &manifest.Size{CPU: 0.5, Memory: 256}, Scale: &manifest.Scale{Min: 1, Max: 1}, PodGroup: planningPodGroup()}
	cfg.PodGroup.Scratch = []manifest.ScratchVolume{{Name: "scratch", SizeMiB: 64}}
	maximum, err := managedWorkloadMaximum(cfg)
	if err != nil || maximum.EphemeralStorageBytes != 128<<20 {
		t.Fatalf("scratch not reserved: %+v %v", maximum, err)
	}
	cfg.PodGroup.Scratch[0].SizeMiB = int64(^uint64(0) >> 1)
	if _, err = managedWorkloadMaximum(cfg); err == nil {
		t.Fatal("unbounded scratch accepted")
	}
}
