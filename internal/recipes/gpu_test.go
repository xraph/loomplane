package recipes_test

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

func gpuSample(t *testing.T) recipes.Kind {
	t.Helper()
	k, ok := recipestest.Registry(t).Kind("gpusample")
	if !ok {
		t.Fatal("recipestest has no gpusample kind")
	}
	return k
}

func TestGPUComponentMayBeAmd64Only(t *testing.T) {
	r := recipestest.Registry(t)
	k, ok := r.Kind("gpusample")
	if !ok || k.Components[0].Resources.GPU == nil {
		t.Fatal("gpusample needs a GPU component")
	}
	if _, has := k.Releases[0].Images["server"].Digests["arm64"]; has {
		t.Fatal("gpusample's GPU image must be amd64 only")
	}
	if _, err := r.Resolve("gpusample", "", "amd64", "server"); err != nil {
		t.Fatal(err)
	}
}

func TestResolveGPUComponentOnArm64(t *testing.T) {
	_, err := recipestest.Registry(t).Resolve("gpusample", "", "arm64", "server")
	if err == nil || !strings.Contains(err.Error(), "gpusample runs on NVIDIA GPUs and needs an amd64 target") {
		t.Fatalf("arm64: %v", err)
	}
}

func TestGPUKindCloneIsDeep(t *testing.T) {
	r := recipestest.Registry(t)
	k, _ := r.Kind("gpusample")
	k.Components[0].Resources.GPU.Count = 7
	k.Components[0].WeightCache.MountPath = "/changed"
	again, _ := r.Kind("gpusample")
	if again.Components[0].Resources.GPU.Count != 1 || again.Components[0].WeightCache.MountPath != "/cache" {
		t.Fatalf("registry kind changed through a returned copy: %+v", again.Components[0])
	}
}

func TestRegisterRejectsInvalidGPUComponents(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(k *recipes.Kind)
	}{
		{"cpu component amd64 only", "no arm64 digest", func(k *recipes.Kind) { delete(k.Releases[0].Images["sidecar"].Digests, "arm64") }},
		{"gpu job", "isn't a process", func(k *recipes.Kind) {
			k.Components[1].Role = recipes.RoleJob
			k.Components[1].Resources.GPU = &recipes.GPU{Count: 1}
		}},
		{"nine GPUs", "outside 1 to 8", func(k *recipes.Kind) { k.Components[0].Resources.GPU.Count = 9 }},
		{"unknown kind", "isn't a known NVIDIA kind", func(k *recipes.Kind) { k.Components[0].Resources.GPU.Kind = "nvidia-x9000" }},
		{"amd kind", "isn't a known NVIDIA kind", func(k *recipes.Kind) { k.Components[0].Resources.GPU.Kind = "amd-mi300x" }},
		{"memory above kind", "needs 48 GiB per GPU but nvidia-l4 has 24", func(k *recipes.Kind) {
			k.Components[0].Resources.GPU.Kind, k.Components[0].Resources.GPU.MinMemoryGiB = "nvidia-l4", 48
		}},
		{"negative memory floor", "GiB", func(k *recipes.Kind) { k.Components[0].Resources.GPU.MinMemoryGiB = -1 }},
		{"shm too big", "shared memory must be smaller", func(k *recipes.Kind) { k.Components[0].Resources.ShmMB = k.Components[0].Resources.MemoryMB }},
		{"shm huge", "shared memory", func(k *recipes.Kind) { k.Components[0].Resources.ShmMB = 70000 }},
		{"cache without gpu", "weight cache", func(k *recipes.Kind) {
			k.Components[1].WeightCache = &recipes.WeightCache{MountPath: "/cache", KubernetesSizeMB: 2048}
		}},
		{"cache under dev", "weight cache", func(k *recipes.Kind) { k.Components[0].WeightCache.MountPath = "/dev/cache" }},
		{"cache relative", "weight cache mount path", func(k *recipes.Kind) { k.Components[0].WeightCache.MountPath = "cache" }},
		{"cache overlaps volume", "weight cache mount path", func(k *recipes.Kind) {
			k.Components[0].Volume = &recipes.Volume{MountPath: "/cache/data", KubernetesSizeMB: 1024}
			k.Components[0].Protection = recipes.ProtectionRebuildable
		}},
		{"cache too small", "at least 1024 MB on Kubernetes", func(k *recipes.Kind) { k.Components[0].WeightCache.KubernetesSizeMB = 512 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := gpuSample(t)
			tc.change(&k)
			err := recipes.NewRegistry().Register(k)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
