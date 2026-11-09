package deployments

import (
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	cpprovider "github.com/xraph/ctrlplane/provider"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestComponentScaledPerGPUApplies(t *testing.T) {
	kinds := sampleKinds(t, func(k *recipes.Kind) {
		for i := range k.Components {
			if k.Components[i].Name == "api" {
				k.Components[i].Resources = recipes.Resources{CPUMillis: 2000, MemoryMB: 8192, GPU: &recipes.GPU{Count: 1}, PerGPU: &recipes.ResourceStep{CPUMillis: 1500, MemoryMB: 6144}}
			}
		}
	})
	f := newComponentFixture(t, kinds)
	f.secrets.m[ResourceKey("org", "env", "app--postgres", "url")] = []byte(postgresURL)
	placed := &provider.GPUPlacement{GPURequest: provider.GPURequest{Kind: "nvidia-l4", Count: 2}}

	job := f.componentJob(t, "api", newPostgres)
	cfg := job.Action.Resource.Component
	cfg.GPU = placed
	cfg.Component.Resources = planning.ComponentResources(cfg.Component.Resources, placed)
	if _, err := f.target.Apply(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	specs := slices.Collect(maps.Values(f.backend.specs))
	if len(specs) != 1 || specs[0].Resources.CPUMillis != 5000 || specs[0].Resources.MemoryMB != 20480 {
		t.Fatalf("applied %+v", specs)
	}

	// Resources the plan didn't scale for its GPUs aren't what the recipe makes.
	job = f.componentJob(t, "api", newPostgres)
	job.Action.Resource.Component.GPU = placed
	if _, err := f.target.Apply(t.Context(), job); !errors.Is(err, errRecipeChanged) {
		t.Fatalf("unscaled resources: %v", err)
	}
}

func TestWeightCacheSizeSetting(t *testing.T) {
	for value, want := range map[string]string{"153600": "150Gi", "1000": "20Gi"} {
		component := recipes.Component{
			Name: "engine", Role: recipes.RoleProcess,
			Resources:   recipes.Resources{CPUMillis: 4000, MemoryMB: 16384, GPU: &recipes.GPU{Kind: "nvidia-l4", Count: 1}},
			WeightCache: &recipes.WeightCache{MountPath: "/cache", KubernetesSizeMB: 20480, SizeSetting: "cache_mb"},
		}
		cfg := &planning.ComponentConfig{Kind: "inference", Resource: "llm", Component: recipes.ResolvedComponent{Component: component}, Settings: map[string]string{"cache_mb": value}}
		sp, labels := &cpprovider.ServiceSpec{}, map[string]string{}
		componentSpec(sp, labels, cfg, "kubernetes")
		if len(sp.Volumes) != 1 {
			t.Fatalf("volumes %+v", sp.Volumes)
		}
		// The PVC request the Kubernetes runtime makes of it.
		if got := resource.NewQuantity(int64(sp.Volumes[0].SizeMB)<<20, resource.BinarySI).String(); got != want {
			t.Errorf("cache_mb %s: PVC %s, want %s", value, got, want)
		}
		if component.WeightCache.KubernetesSizeMB != 20480 {
			t.Fatal("sizing changed the recipe")
		}
	}
}

func TestStartTimeoutReachesTheRuntimeAndTheStep(t *testing.T) {
	component := recipes.Component{Name: "engine", Role: recipes.RoleProcess, StartTimeout: 2 * time.Hour}
	sp, labels := &cpprovider.ServiceSpec{}, map[string]string{}
	componentSpec(sp, labels, &planning.ComponentConfig{Kind: "inference", Resource: "llm", Component: recipes.ResolvedComponent{Component: component}}, "server")
	if labels[owned.LabelStartTimeout] != "7200" {
		t.Fatalf("labels %v", labels)
	}
	labels = map[string]string{}
	componentSpec(sp, labels, &planning.ComponentConfig{Kind: "sample", Resource: "app", Component: recipes.ResolvedComponent{Component: recipes.Component{Name: "api", Role: recipes.RoleProcess}}}, "server")
	if _, ok := labels[owned.LabelStartTimeout]; ok {
		t.Fatalf("no start timeout, no label: %v", labels)
	}

	component.StartTimeout = 3 * time.Hour
	actions := []planning.Action{
		{ID: "resource:llm--engine", Resource: &planning.ResourceConfig{Component: &planning.ComponentConfig{Component: recipes.ResolvedComponent{Component: component}}}},
		{ID: "resource:app--api", Resource: &planning.ResourceConfig{Component: &planning.ComponentConfig{Component: recipes.ResolvedComponent{Component: recipes.Component{Name: "api"}}}}},
		{ID: "service:web", Service: &planning.ServiceConfig{}},
	}
	if got := actionTimeouts(actions); !maps.Equal(got, map[string]time.Duration{"resource:llm--engine": 3*time.Hour + 15*time.Minute}) {
		t.Fatalf("timeouts %v", got)
	}
	if got := actionTimeouts(actions[1:]); got != nil {
		t.Fatalf("nothing waits longer: %v", got)
	}
}
