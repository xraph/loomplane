package fleets

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/fleet"
	"github.com/xraph/loomplane/pkg/manifest"
)

func TestPodGroupGrantCountsSurgeScratchAndJobs(t *testing.T) {
	group := &manifest.PodGroup{SurgeReplicas: 1, Scratch: []manifest.ScratchVolume{{Name: "scratch", SizeMiB: 600}}, Sidecars: []manifest.Sidecar{{Name: "helper", Image: "registry.example.test/helper@sha256:" + strings.Repeat("a", 64), Architecture: "arm64", CPUMillis: 250, MemoryMiB: 128, Readiness: manifest.ContainerReadiness{Port: 9001}}}}
	template := fleet.Template{Manifest: manifest.Manifest{Services: map[string]manifest.Service{"api": {Settings: manifest.Settings{PodGroup: group}}}}, Jobs: map[string]fleet.LifecycleJob{"migrate": {}}}
	profile := fleet.Profile{Services: map[string]fleet.ServiceProfile{"api": {CPUMillis: 500, MemoryMiB: 256, MinReplicas: 1, MaxReplicas: 2}}}
	demand, err := templateWorkloadDemand(template, profile)
	if err != nil || demand.CPUMillis != 2750 || demand.MemoryMiB != 1408 || demand.StorageGiB != 2 {
		t.Fatal(demand, err)
	}
}
