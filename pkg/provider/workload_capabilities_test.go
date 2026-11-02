package provider

import (
	"strings"
	"testing"
)

func testPodGroup() *PodGroup {
	return &PodGroup{Sidecars: []Sidecar{{Name: "helper", Image: "registry.example.test/helper@sha256:" + strings.Repeat("a", 64), Architecture: "arm64", CPUMillis: 250, MemoryMiB: 128, Readiness: ContainerReadiness{Port: 9001, Path: "/ready"}, Mounts: []ScratchMount{{Volume: "scratch", Path: "/scratch"}}}}, Scratch: []ScratchVolume{{Name: "scratch", SizeMiB: 256}}, Mounts: []ScratchMount{{Volume: "scratch", Path: "/scratch"}}, SurgeReplicas: 1}
}
func TestPodGroupAccountsEveryContainer(t *testing.T) {
	cpu, memory, scratch, err := PodGroupResources(500, 256, testPodGroup())
	if err != nil || cpu != 750 || memory != 384 || scratch != 256 {
		t.Fatalf("pod request: cpu=%d memory=%d scratch=%d err=%v", cpu, memory, scratch, err)
	}
}
func TestPodGroupRejectsUnsafeContract(t *testing.T) {
	cases := map[string]func(*PodGroup){
		"duplicate-container": func(g *PodGroup) { g.Sidecars = append(g.Sidecars, g.Sidecars[0]) },
		"main-name":           func(g *PodGroup) { g.Sidecars[0].Name = "main" },
		"tagged-image":        func(g *PodGroup) { g.Sidecars[0].Image = "registry.example.test/helper:latest" },
		"missing-readiness":   func(g *PodGroup) { g.Sidecars[0].Readiness = ContainerReadiness{} },
		"zero-request":        func(g *PodGroup) { g.Sidecars[0].CPUMillis = 0 },
		"unbounded-scratch":   func(g *PodGroup) { g.Scratch[0].SizeMiB = 0 },
		"unknown-volume":      func(g *PodGroup) { g.Mounts[0].Volume = "unknown" },
		"host-path":           func(g *PodGroup) { g.Mounts[0].Path = "/proc" },
		"token-path":          func(g *PodGroup) { g.Mounts[0].Path = "/var/run/secrets/kubernetes.io" },
		"path-traversal":      func(g *PodGroup) { g.Mounts[0].Path = "/scratch/../etc" },
		"overlapping-mounts": func(g *PodGroup) {
			g.Mounts = append(g.Mounts, ScratchMount{Volume: "scratch", Path: "/scratch/nested"})
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			g := testPodGroup()
			change(g)
			if err := ValidatePodGroup(g); err == nil {
				t.Fatal("unsafe pod group accepted")
			}
		})
	}
}
func TestPodGroupArchitectureMismatch(t *testing.T) {
	if err := CheckPodGroupArchitecture(testPodGroup(), "amd64"); err == nil {
		t.Fatal("cross-architecture sidecar accepted")
	}
}
