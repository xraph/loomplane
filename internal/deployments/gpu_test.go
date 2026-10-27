package deployments

import (
	"testing"

	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestGPUSpecForComponents(t *testing.T) {
	sp, labels := &cpprovider.ServiceSpec{}, map[string]string{}
	gpuSpec(sp, labels, &provider.GPUPlacement{GPURequest: provider.GPURequest{Kind: "nvidia-l4", Count: 1}}, 2048, &recipes.WeightCache{MountPath: "/cache", KubernetesSizeMB: 20480}, "server")
	if labels[owned.LabelGPU] != `{"kind":"nvidia-l4","count":1}` || labels[owned.LabelShmMB] != "2048" || labels[owned.LabelStateful] != "true" {
		t.Fatalf("labels: %v", labels)
	}
	if len(sp.Volumes) != 1 || sp.Volumes[0].Name != "weights" || sp.Volumes[0].SizeMB != 0 {
		t.Fatalf("server weights volume carries no size quota: %+v", sp.Volumes)
	}
	if sp.Volumes[0].Type != "persistent" || sp.Volumes[0].MountPath != "/cache" || sp.Resources.GPU != "nvidia-l4:1" {
		t.Fatalf("weights volume or display GPU: %+v", sp)
	}

	sp, labels = &cpprovider.ServiceSpec{}, map[string]string{}
	gpuSpec(sp, labels, &provider.GPUPlacement{GPURequest: provider.GPURequest{Kind: "nvidia-l4", Count: 2}}, 0, &recipes.WeightCache{MountPath: "/cache", KubernetesSizeMB: 20480}, "kubernetes")
	if len(sp.Volumes) != 1 || sp.Volumes[0].SizeMB != 20480 || sp.Resources.GPU != "nvidia-l4:2" {
		t.Fatalf("kubernetes weights volume asks for its size: %+v", sp)
	}
	if _, ok := labels[owned.LabelShmMB]; ok {
		t.Fatalf("no shared memory size means no label: %v", labels)
	}

	sp, labels = &cpprovider.ServiceSpec{}, map[string]string{}
	gpuSpec(sp, labels, nil, 0, nil, "server")
	if len(labels) != 0 || len(sp.Volumes) != 0 || sp.Resources.GPU != "" {
		t.Fatalf("nothing asked, nothing set: %v %+v", labels, sp)
	}
}

func gpuJob(cfg *planning.ServiceConfig) *Job {
	return &Job{Deployment: &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, Connection: &connections.Connection{ID: "selected"}, Action: planning.Action{Name: "model", Provider: cfg.Provider, Service: cfg}}
}

func TestOwnedSpecCarriesServiceGPUAndSharedMemory(t *testing.T) {
	cfg := &planning.ServiceConfig{Provider: "server", Size: &manifest.Size{CPU: 4, Memory: 16384, Shm: 1024}, GPU: &provider.GPUPlacement{GPURequest: provider.GPURequest{Kind: "nvidia-l4", Count: 1}}}
	sp, labels := ownedSpec(gpuJob(cfg), "registry.example/model@sha256:abc", ownedState{})
	if labels[owned.LabelGPU] != `{"kind":"nvidia-l4","count":1}` || labels[owned.LabelShmMB] != "1024" || sp.Resources.GPU != "nvidia-l4:1" {
		t.Fatalf("service GPU: %v %+v", labels, sp.Resources)
	}
	if sp.Resources.MemoryMB != 16384 || sp.Resources.CPUMillis != 4000 || len(sp.Volumes) != 0 {
		t.Fatalf("service resources: %+v", sp)
	}

	cfg = &planning.ServiceConfig{Provider: "server", Size: &manifest.Size{CPU: 1, Memory: 512, Shm: 128}}
	_, labels = ownedSpec(gpuJob(cfg), "registry.example/app@sha256:abc", ownedState{})
	if _, ok := labels[owned.LabelGPU]; ok || labels[owned.LabelShmMB] != "128" {
		t.Fatalf("shared memory alone: %v", labels)
	}
}

func TestOwnedSpecCarriesComponentGPUAndWeightCache(t *testing.T) {
	for _, target := range []struct {
		provider string
		size     int
	}{{"server", 0}, {"kubernetes", 20480}} {
		t.Run(target.provider, func(t *testing.T) {
			component := recipes.Component{
				Name: "engine", Role: recipes.RoleProcess,
				Resources:   recipes.Resources{CPUMillis: 4000, MemoryMB: 16384, ShmMB: 2048, GPU: &recipes.GPU{Kind: "nvidia-l4", Count: 1}},
				Volume:      &recipes.Volume{MountPath: "/data", KubernetesSizeMB: 1024},
				WeightCache: &recipes.WeightCache{MountPath: "/cache", KubernetesSizeMB: 20480},
			}
			cfg := &planning.ServiceConfig{Provider: target.provider, Component: &planning.ComponentConfig{
				Kind: "inference", Resource: "llm", Component: recipes.ResolvedComponent{Component: component},
				GPU: &provider.GPUPlacement{GPURequest: provider.GPURequest{Kind: "nvidia-l4", Count: 2}},
			}}
			sp, labels := ownedSpec(gpuJob(cfg), "registry.example/engine@sha256:abc", ownedState{})
			if labels[owned.LabelGPU] != `{"kind":"nvidia-l4","count":2}` || labels[owned.LabelShmMB] != "2048" || labels[owned.LabelStateful] != "true" {
				t.Fatalf("component labels: %v", labels)
			}
			if len(sp.Volumes) != 2 || sp.Volumes[0].Name != "data" || sp.Volumes[1].Name != "weights" || sp.Volumes[1].MountPath != "/cache" || sp.Volumes[1].SizeMB != target.size {
				t.Fatalf("component volumes: %+v", sp.Volumes)
			}
			if sp.Resources.GPU != "nvidia-l4:2" || sp.Resources.MemoryMB != 16384 {
				t.Fatalf("component resources: %+v", sp.Resources)
			}
		})
	}
}
