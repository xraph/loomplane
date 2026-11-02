//go:build acceptance

package recipes_test

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
)

// TestAcceptanceGPUKind checks the qualification's GPU kind as the
// acceptance build registers it:
//
//	go test -tags=acceptance ./internal/recipes/ -run TestAcceptanceGPUKind -count=1
func TestAcceptanceGPUKind(t *testing.T) {
	k, ok := recipes.Default().Kind(recipes.AcceptanceGPUKind)
	if !ok {
		t.Fatal("the acceptance build doesn't register lpaccept-gpu")
	}
	c := k.Components[0]
	if c.Resources.GPU == nil || c.Resources.GPU.Count != 1 || c.Resources.ShmMB != 1024 {
		t.Fatalf("resources: %+v", c.Resources)
	}
	if c.WeightCache == nil || c.WeightCache.MountPath != "/cache" || c.WeightCache.KubernetesSizeMB != 1024 {
		t.Fatalf("weight cache: %+v", c.WeightCache)
	}
	if c.Readiness == nil || c.Readiness.Path != "/" || !strings.HasPrefix(c.Command[2], "nvidia-smi && ") {
		t.Fatalf("component: %+v", c)
	}
	for _, target := range []string{"server", "kubernetes"} {
		comp, err := recipes.Default().Resolve(recipes.AcceptanceGPUKind, "", "amd64", target)
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		if got := comp.Components[0].Image; got != "docker.io/nvidia/cuda@sha256:db91f04084e04eee8670cfb732fe6396010cad7493e45af3b6ad979be590a15d" {
			t.Fatalf("%s image: %s", target, got)
		}
	}
	if _, err := recipes.Default().Resolve(recipes.AcceptanceGPUKind, "", "arm64", "server"); err == nil || !strings.Contains(err.Error(), "needs an amd64 target") {
		t.Fatalf("arm64: %v", err)
	}
}
