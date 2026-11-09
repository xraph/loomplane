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
	if err != nil || demand.CPUMillis != 2750 || demand.MemoryMiB != 1408 || demand.StorageGiB != 3 {
		t.Fatal(demand, err)
	}
}

func TestFleetScaleGPUGrantRequiresReviewedBound(t *testing.T) {
	template := fleet.Template{Manifest: manifest.Manifest{Services: map[string]manifest.Service{"api": {Settings: manifest.Settings{Size: &manifest.Size{GPU: &manifest.GPU{Kind: "gpu-a", Count: 1}}}}}}}
	profile := fleet.Profile{Services: map[string]fleet.ServiceProfile{"api": {CPUMillis: 500, MemoryMiB: 256, MinReplicas: 1, MaxReplicas: 3}}}
	if _, err := templateWorkloadDemand(template, profile); err == nil {
		t.Fatal("GPU admitted by a grant without GPU bounds")
	}
}

func TestFleetScaleSnapshotUsesProfileMaximum(t *testing.T) {
	release, instance, input := renderFixture(t)
	snapshot, err := RenderInstance(release, instance, input)
	if err != nil {
		t.Fatal(err)
	}
	demand, err := FleetDemand(*snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if demand.CPUMillis != 1500 || demand.MemoryBytes != 805306368 || demand.Pods != 3 || demand.EphemeralStorageBytes != 201326592 {
		t.Fatalf("profile maximum not reserved: %+v", demand)
	}
}

func TestInstanceGrantCountsContainerAndJobStorage(t *testing.T) {
	template := fleet.Template{Manifest: manifest.Manifest{Services: map[string]manifest.Service{"api": {}}}, Jobs: map[string]fleet.LifecycleJob{"bootstrap": {}}}
	profile := fleet.Profile{Services: map[string]fleet.ServiceProfile{"api": {CPUMillis: 500, MemoryMiB: 256, MinReplicas: 1, MaxReplicas: 16}}}
	demand, err := templateWorkloadDemand(template, profile)
	if err != nil || demand.StorageGiB != 2 {
		t.Fatal(demand, err)
	}
	if template.Manifest.Services["api"].PodGroup != nil {
		t.Fatal("demand calculation mutated the sealed template")
	}
}
