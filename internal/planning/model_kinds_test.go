package planning

import (
	"maps"
	"testing"

	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes"
)

// The model kinds are registered by internal/recipes; these plans use the
// default registry to check they plan like any other opt-in kind.

const modelSettings = "settings: {entry: BAAI/bge-small-en-v1.5, model: BAAI/bge-small-en-v1.5, revision: 5c38ec7c405ec4b44b94cc5a9bb96e735b38267a, quantization: native, served_name: bge-small}"

func modelDoc(kind, extra string) string {
	return "version: 1\nresources:\n  embed: {kind: " + kind + ", provider: server, target: " + targetA + ", " + modelSettings + ", environments: {staging: {enabled: true}}" + extra + "}\n"
}

func modelPlan(t *testing.T, environment, doc string, ai bool, connections []ConnectionOption) *Result {
	t.Helper()
	return Compute(Input{
		Environment: environment, Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: providers.RegistryFor(map[string]bool{recipes.FeatureAI: ai}),
		Connections: connections, Features: map[string]bool{recipes.FeatureAI: ai},
	})
}

func TestModelKindPlansWithAIOnWhereEnabled(t *testing.T) {
	doc := modelDoc(recipes.ModelTEIKind, "")
	off := modelPlan(t, "staging", doc, false, composedConnections(t))
	if off.Executable || !hasCode(off.Problems, "catalog-feature-off") || len(off.Actions) != 0 {
		t.Fatalf("AI off: actions %+v, problems %+v", off.Actions, off.Problems)
	}

	r := modelPlan(t, "staging", doc, true, composedConnections(t))
	if !r.Executable {
		t.Fatalf("AI on: %+v", r.Problems)
	}
	engine, guard := findAction(r, "resource:embed--engine"), findAction(r, "resource:embed--guard")
	if engine == nil || guard == nil || findAction(r, "resource:embed") == nil {
		t.Fatalf("actions: %+v", r.Actions)
	}
	want := map[string]string{
		"entry": "BAAI/bge-small-en-v1.5", "model": "BAAI/bge-small-en-v1.5", "revision": "5c38ec7c405ec4b44b94cc5a9bb96e735b38267a",
		"quantization": "native", "served_name": "bge-small", "max_model_len": "4096", "cache_mb": "20480",
	}
	if got := engine.Resource.Component.Settings; !maps.Equal(got, want) {
		t.Errorf("engine settings %v, want %v", got, want)
	}
	if engine.Step >= guard.Step {
		t.Errorf("engine step %d, guard step %d: the guard starts after the engine", engine.Step, guard.Step)
	}

	// Not enabled in production, so nothing runs there.
	production := modelPlan(t, "production", doc, true, composedConnections(t))
	if len(production.Actions) != 0 || len(production.Problems) != 0 {
		t.Fatalf("production: actions %+v, problems %+v", production.Actions, production.Problems)
	}
}

func TestModelKindNeedsItsSettings(t *testing.T) {
	doc := "version: 1\nresources:\n  embed: {kind: " + recipes.ModelLlamaCPPKind + ", provider: server, target: " + targetA + ", " + modelSettings + ", environments: {staging: {enabled: true}}}\n"
	r := modelPlan(t, "staging", doc, true, composedConnections(t))
	if r.Executable || !hasCode(r.Problems, "catalog-setting-required") {
		t.Fatalf("a llama.cpp model without its GGUF file planned: %+v", r.Problems)
	}
}

func TestGPUModelKindPlansOnAnAmd64GPUServer(t *testing.T) {
	doc := modelDoc(recipes.ModelVLLMKind, ", gpu: {kind: nvidia-l4, count: 1}")
	r := modelPlan(t, "staging", doc, true, gpuConnections(t, twoL4, "amd64"))
	if !r.Executable {
		t.Fatalf("problems: %+v", r.Problems)
	}
	if got := findAction(r, "resource:embed--engine").Resource.Component.GPU; got == nil || got.Kind != "nvidia-l4" || got.Count != 1 {
		t.Fatalf("engine placement: %+v", got)
	}
	if got := findAction(r, "resource:embed--guard").Resource.Component.GPU; got != nil {
		t.Fatalf("the guard got a GPU: %+v", got)
	}

	arm := modelPlan(t, "staging", doc, true, gpuConnections(t, twoL4, "arm64"))
	if arm.Executable || !hasCode(arm.Problems, "gpu-architecture") {
		t.Fatalf("arm64: %+v", arm.Problems)
	}
}
