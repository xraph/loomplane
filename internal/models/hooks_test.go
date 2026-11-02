package models

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/provider"
)

// hfToken looks like a Hugging Face token and is nobody's.
const hfToken = "hf_modelsTESTtokenVALUE4242"

// Revisions of the fixture's Llama entry and its GGUF repo. Re-pinning the
// registry changes the first to llamaRepin.
const (
	llamaRevision = "0e9e39f249a16976918f6564b8830bc894c89659"
	llamaRepin    = "1f0f40f350b27087029f7675c9941cd905d9a760"
	ggufRevision  = "bf5b95e96dac0462e2a09145ec66cae9a3f12067"
)

// llamaYAML is a registry family with one gated entry, Llama 3.1 8B
// Instruct, pinned to revision, with extra vLLM arguments args. The real
// entry waits for a token to be pinned, so the tests carry their own.
func llamaYAML(revision, args string) string {
	return `family: llama
entries:
  - id: meta-llama/Llama-3.1-8B-Instruct
    title: Llama 3.1 8B Instruct
    publisher: Meta
    summary: Meta's general 8B chat model with a 128K context and tool calling.
    revision: ` + revision + `
    license:
      id: llama3.1
      commercial: true
    gated: true
    tasks: [chat, code]
    parameters: 8030261248
    context_length: 131072
    architecture:
      name: LlamaForCausalLM
      layers: 32
      attention_heads: 32
      kv_heads: 8
      head_dim: 128
    tool_parser: llama3_json
    quantizations:
      - id: bf16
        engine: vllm
        weight_bytes: 16060522496
        min_gpu_memory_gib: 24
        args: [` + args + `]
      - id: gguf-q4_k_m
        engine: llamacpp
        repo: bartowski/Meta-Llama-3.1-8B-Instruct-GGUF
        revision: ` + ggufRevision + `
        file: Meta-Llama-3.1-8B-Instruct-Q4_K_M.gguf
        weight_bytes: 4920739232
        min_memory_gib: 8
`
}

// fixture loads a registry holding llamaYAML(revision, args).
func fixture(t *testing.T, revision, args string) *registry.Registry {
	t.Helper()
	r, err := registry.Load(fstest.MapFS{"llama.yaml": {Data: []byte(llamaYAML(revision, args))}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const llamaID = "meta-llama/Llama-3.1-8B-Instruct"

// settingsFor are the kind settings of entry id's quantization q in r, as
// a deploy writes them.
func settingsFor(t *testing.T, r *registry.Registry, id, q string) map[string]string {
	t.Helper()
	e, ok := r.Entry(id)
	if !ok {
		t.Fatalf("no entry %s", id)
	}
	quant, ok := e.Quantization(q)
	if !ok {
		t.Fatalf("%s has no %s", id, q)
	}
	return engines.Settings(engines.Spec{Entry: e, Quantization: quant, ServedName: "llama-8b", MaxModelLen: 8192})
}

// fakeTokens is the workspace's Hugging Face token. It counts reads of the
// value.
type fakeTokens struct {
	mu       sync.Mutex
	token    string
	revision int64
	err      error // from reading the value
	reads    int
}

func (f *fakeTokens) HuggingFaceTokenValue(_ context.Context, orgID string) (string, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.err != nil {
		return "", 0, f.err
	}
	if f.token == "" || orgID != "org" {
		return "", 0, aisettings.ErrNotFound
	}
	return f.token, f.revision, nil
}

func (f *fakeTokens) HuggingFaceTokenRevision(_ context.Context, orgID string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.token == "" || orgID != "org" {
		return 0, nil
	}
	return f.revision, nil
}

// component is one component of a model kind, as the runner resolves it.
func component(t *testing.T, kind, name string) recipes.ResolvedComponent {
	t.Helper()
	k, ok := recipes.Default().Kind(kind)
	if !ok {
		t.Fatalf("no kind %s", kind)
	}
	for _, c := range k.Components {
		if c.Name == name {
			return recipes.ResolvedComponent{Component: c}
		}
	}
	t.Fatalf("%s has no component %s", kind, name)
	return recipes.ResolvedComponent{}
}

// run is a model resource's component as the runner applies it, on gpus
// GPUs (none when 0).
func run(t *testing.T, kind, name string, settings map[string]string, gpus int) deployments.ComponentRun {
	t.Helper()
	r := deployments.ComponentRun{
		OrgID: "org", ProjectID: "shop", EnvironmentID: "env_staging", Environment: "staging",
		Resource: "llama-8b", Kind: kind, Provider: "server", Component: component(t, kind, name),
		Settings: maps.Clone(settings),
	}
	if gpus > 0 {
		r.GPU = &provider.GPUPlacement{GPURequest: provider.GPURequest{Kind: "nvidia-l40s", Count: gpus}}
	}
	return r
}

func TestEngineFilesForAGatedModel(t *testing.T) {
	ctx := context.Background()
	reg := fixture(t, llamaRevision, "--enforce-eager")
	tokens := &fakeTokens{token: hfToken, revision: 3}
	h := NewHooks(HooksDeps{Registry: reg, Settings: tokens})
	settings := settingsFor(t, reg, llamaID, "bf16")

	for _, gpus := range []int{1, 2} {
		files, err := h.Files(ctx, run(t, recipes.ModelVLLMKind, recipes.ModelEngine, settings, gpus))
		if err != nil {
			t.Fatal(err)
		}
		spec, err := engines.FromSettings(ctx, reg, nil, "org", settings, gpus, false)
		if err != nil {
			t.Fatal(err)
		}
		args, err := engines.Args(spec)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := string(files[recipes.ModelArgsPath]), strings.Join(args, "\n")+"\n"; got != want {
			t.Errorf("%d GPUs: args file\n%s\nwant\n%s", gpus, got, want)
		}
		if !strings.Contains(string(files[recipes.ModelArgsPath]), fmt.Sprintf("--tensor-parallel-size=%d\n", gpus)) {
			t.Errorf("%d GPUs: args file doesn't split across them:\n%s", gpus, files[recipes.ModelArgsPath])
		}
		if got := string(files[recipes.ModelTokenPath]); got != hfToken {
			t.Errorf("token file = %q", got)
		}
		if len(files) != 3 {
			t.Errorf("files: %v, want the args, the token and the endpoint", slicesOfKeys(files))
		}
		if endpoint, ok := files[recipes.ModelEndpointPath]; !ok || len(endpoint) != 0 {
			t.Errorf("endpoint file = %q (present %v), want present and empty for huggingface.co", endpoint, ok)
		}
		// The token goes only into its own file.
		if strings.Contains(string(files[recipes.ModelArgsPath]), hfToken) {
			t.Error("the args file holds the token")
		}
	}

	// llama.cpp on CPUs gets the GGUF from the other repo, and the token
	// too, since the entry is gated.
	gguf := settingsFor(t, reg, llamaID, "gguf-q4_k_m")
	files, err := h.Files(ctx, run(t, recipes.ModelLlamaCPPKind, recipes.ModelEngine, gguf, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(files[recipes.ModelArgsPath]), "--model-url=https://huggingface.co/bartowski/Meta-Llama-3.1-8B-Instruct-GGUF/resolve/"+ggufRevision+"/Meta-Llama-3.1-8B-Instruct-Q4_K_M.gguf\n") ||
		!strings.Contains(string(files[recipes.ModelArgsPath]), "--n-gpu-layers=0\n") {
		t.Errorf("llama.cpp args:\n%s", files[recipes.ModelArgsPath])
	}
	if string(files[recipes.ModelTokenPath]) != hfToken {
		t.Errorf("llama.cpp token file = %q", files[recipes.ModelTokenPath])
	}
	files, err = h.Files(ctx, run(t, recipes.ModelLlamaCPPGPUKind, recipes.ModelEngine, gguf, 1))
	if err != nil || !strings.Contains(string(files[recipes.ModelArgsPath]), "--n-gpu-layers=999\n") {
		t.Errorf("llama.cpp on a GPU: %v\n%s", err, files[recipes.ModelArgsPath])
	}
}

func slicesOfKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestEngineFilesWithoutAToken(t *testing.T) {
	ctx := context.Background()
	reg := fixture(t, llamaRevision, "--enforce-eager")
	settings := settingsFor(t, reg, llamaID, "bf16")
	const sentence = "Llama 3.1 8B Instruct is gated on Hugging Face. Add a Hugging Face token that has accepted its license in AI settings"

	for name, tokens := range map[string]Tokens{"no token": &fakeTokens{}, "no settings": nil} {
		h := NewHooks(HooksDeps{Registry: reg, Settings: tokens})
		_, err := h.Files(ctx, run(t, recipes.ModelVLLMKind, recipes.ModelEngine, settings, 1))
		if err == nil || !strings.Contains(err.Error(), sentence) {
			t.Errorf("%s: %v, want %q", name, err, sentence)
		}
	}

	// A token that can't be read fails the step without quoting what the
	// read said, which could be the token.
	h := NewHooks(HooksDeps{Registry: reg, Settings: &fakeTokens{token: hfToken, err: fmt.Errorf("vault returned %s twice", hfToken)}})
	_, err := h.Files(ctx, run(t, recipes.ModelVLLMKind, recipes.ModelEngine, settings, 1))
	if err == nil || strings.Contains(err.Error(), hfToken) || strings.Contains(err.Error(), "hf_") {
		t.Errorf("unreadable token: %v", err)
	}
}

func TestUngatedModelGetsNoToken(t *testing.T) {
	ctx := context.Background()
	reg := registry.Default()
	tokens := &fakeTokens{token: hfToken, revision: 1}
	h := NewHooks(HooksDeps{Registry: reg, Settings: tokens})
	settings := settingsFor(t, reg, "Qwen/Qwen2.5-7B-Instruct", "bf16")
	files, err := h.Files(ctx, run(t, recipes.ModelVLLMKind, recipes.ModelEngine, settings, 1))
	if err != nil {
		t.Fatal(err)
	}
	token, ok := files[recipes.ModelTokenPath]
	if !ok || len(token) != 0 {
		t.Errorf("token file = %q (present %v), want present and empty", token, ok)
	}
	if tokens.reads != 0 {
		t.Errorf("the token was read %d times for an ungated model", tokens.reads)
	}
	if strings.Contains(string(files[recipes.ModelArgsPath]), hfToken) {
		t.Error("the args file holds the token")
	}
}

func TestGuardFiles(t *testing.T) {
	h := NewHooks(HooksDeps{Registry: registry.Default()})
	for _, kind := range recipes.ModelKinds() {
		// The guard's file doesn't depend on the model, so it needs no settings.
		files, err := h.Files(context.Background(), run(t, kind, recipes.ModelGuard, nil, 0))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 || string(files[recipes.ModelGuardConfig]) != string(engines.GuardConfig()) {
			t.Errorf("%s guard files: %v", kind, slicesOfKeys(files))
		}
	}
	if _, err := h.Files(context.Background(), deployments.ComponentRun{Kind: recipes.ModelVLLMKind, Component: recipes.ResolvedComponent{Component: recipes.Component{Name: "sidecar"}}}); err == nil {
		t.Error("a component the kinds don't have got files")
	}
}

// recordingGateways records each ModelsChanged.
type recordingGateways struct {
	mu    sync.Mutex
	calls []string
}

func (g *recordingGateways) ModelsChanged(_ context.Context, orgID, projectID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, orgID+"/"+projectID)
}

func TestReadyTellsGatewaysButNeverFails(t *testing.T) {
	ctx := context.Background()
	gateways := &recordingGateways{}
	h := NewHooks(HooksDeps{Registry: registry.Default(), Gateways: gateways})

	if err := h.Ready(ctx, run(t, recipes.ModelVLLMKind, recipes.ModelEngine, nil, 1)); err != nil {
		t.Fatal(err)
	}
	if len(gateways.calls) != 0 {
		t.Errorf("the engine alone told the gateways: %v", gateways.calls)
	}
	if err := h.Ready(ctx, run(t, recipes.ModelVLLMKind, recipes.ModelGuard, nil, 0)); err != nil {
		t.Fatal(err)
	}
	if err := h.Removed(ctx, deployments.ComposedResource{OrgID: "org", ProjectID: "shop", EnvironmentID: "env_staging", Resource: "llama-8b", Kind: recipes.ModelVLLMKind}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"org/shop", "org/shop"}; fmt.Sprint(gateways.calls) != fmt.Sprint(want) {
		t.Errorf("gateways told %v, want %v", gateways.calls, want)
	}

	// Without a gateway, nothing is told and nothing fails.
	for _, h := range []*Hooks{NewHooks(HooksDeps{}), func() *Hooks { h := NewHooks(HooksDeps{Gateways: gateways}); h.SetGateways(nil); return h }()} {
		if err := h.Ready(ctx, run(t, recipes.ModelTEIKind, recipes.ModelGuard, nil, 0)); err != nil {
			t.Error(err)
		}
		if err := h.Removed(ctx, deployments.ComposedResource{OrgID: "org", ProjectID: "shop"}); err != nil {
			t.Error(err)
		}
	}
	if len(gateways.calls) != 2 {
		t.Errorf("an unbound gateway was told: %v", gateways.calls)
	}
}

// Ruling M22 item 4: FromSettings checks the engine only for llama.cpp on a
// GPU, so the hooks check every kind's.
func TestHooksCheckTheQuantizationsEngine(t *testing.T) {
	ctx := context.Background()
	reg := registry.Default()
	h := NewHooks(HooksDeps{Registry: reg, Settings: &fakeTokens{}})
	vllm := settingsFor(t, reg, "Qwen/Qwen2.5-7B-Instruct", "bf16")
	gguf := settingsFor(t, reg, "Qwen/Qwen2.5-7B-Instruct", "gguf-q4_k_m")
	tei := settingsFor(t, reg, "BAAI/bge-small-en-v1.5", "native")

	for _, c := range []struct {
		kind     string
		settings map[string]string
		gpus     int
		want     string // "" when it runs
	}{
		{recipes.ModelVLLMKind, vllm, 1, ""},
		{recipes.ModelLlamaCPPKind, gguf, 0, ""},
		{recipes.ModelLlamaCPPGPUKind, gguf, 1, ""},
		{recipes.ModelTEIKind, tei, 0, ""},
		{recipes.ModelVLLMKind, gguf, 1, "runs on llamacpp, which model-vllm doesn't run. Deploy it as model-llamacpp-gpu"},
		{recipes.ModelVLLMKind, tei, 1, "runs on tei, which model-vllm doesn't run. Deploy it as model-tei"},
		{recipes.ModelTEIKind, vllm, 0, "runs on vllm, which model-tei doesn't run. Deploy it as model-vllm"},
		{recipes.ModelTEIKind, gguf, 0, "runs on llamacpp, which model-tei doesn't run. Deploy it as model-llamacpp"},
		{recipes.ModelLlamaCPPKind, vllm, 0, "runs on vllm, which model-llamacpp doesn't run. Deploy it as model-vllm"},
		{recipes.ModelLlamaCPPKind, tei, 0, "runs on tei, which model-llamacpp doesn't run. Deploy it as model-tei"},
		{recipes.ModelLlamaCPPGPUKind, vllm, 1, "runs on vllm, which model-llamacpp-gpu doesn't run. Deploy it as model-vllm"},
		{recipes.ModelLlamaCPPGPUKind, tei, 1, "runs on tei, which model-llamacpp-gpu doesn't run. Deploy it as model-tei"},
	} {
		_, err := h.Files(ctx, run(t, c.kind, recipes.ModelEngine, c.settings, c.gpus))
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s with %s %s: %v", c.kind, c.settings["entry"], c.settings["quantization"], err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s with %s %s: %v, want %q", c.kind, c.settings["entry"], c.settings["quantization"], err, c.want)
		}
	}
}

// Ruling M21: a resource deployed before a registry re-pin still names the
// old weights. The engine can't start on them, and the error says what to
// set.
func TestRepinnedSettingsSayWhatToSet(t *testing.T) {
	ctx := context.Background()
	before := fixture(t, llamaRevision, "--enforce-eager")
	after := fixture(t, llamaRepin, "--enforce-eager")
	h := NewHooks(HooksDeps{Registry: after, Settings: &fakeTokens{token: hfToken, revision: 1}})

	_, err := h.Files(ctx, run(t, recipes.ModelVLLMKind, recipes.ModelEngine, settingsFor(t, before, llamaID, "bf16"), 1))
	var repinned *RepinnedError
	if !errors.As(err, &repinned) {
		t.Fatalf("Files: %v, want a RepinnedError", err)
	}
	want := "llama-8b asks for meta-llama/Llama-3.1-8B-Instruct@" + llamaRevision + ", but the model registry now pins meta-llama/Llama-3.1-8B-Instruct bf16 to meta-llama/Llama-3.1-8B-Instruct@" + llamaRepin +
		". Set llama-8b's settings to model: meta-llama/Llama-3.1-8B-Instruct, revision: " + llamaRepin + ", then plan again"
	if err.Error() != want {
		t.Errorf("Files:\n%v\nwant\n%s", err, want)
	}
	if strings.Contains(err.Error(), hfToken) {
		t.Error("the error holds the token")
	}

	// A GGUF names its file too; one edited to another file is refused the
	// same way.
	gguf := settingsFor(t, after, llamaID, "gguf-q4_k_m")
	gguf["file"] = "Meta-Llama-3.1-8B-Instruct-Q8_0.gguf"
	_, err = h.Files(ctx, run(t, recipes.ModelLlamaCPPKind, recipes.ModelEngine, gguf, 0))
	if !errors.As(err, &repinned) || !strings.Contains(err.Error(), "file: Meta-Llama-3.1-8B-Instruct-Q4_K_M.gguf") {
		t.Errorf("edited GGUF file: %v", err)
	}

	// Settings that match the pin run.
	if _, err := h.Files(ctx, run(t, recipes.ModelVLLMKind, recipes.ModelEngine, settingsFor(t, after, llamaID, "bf16"), 1)); err != nil {
		t.Errorf("settings at the pin: %v", err)
	}
}

// customEntries holds one workspace's entries built from pasted repos.
type customEntries map[string]registry.Entry

func (c customEntries) Custom(_ context.Context, orgID, id string) (registry.Entry, error) {
	if e, ok := c[id]; ok && orgID == "org" {
		return e, nil
	}
	return registry.Entry{}, errors.New("no such custom entry")
}

// A custom entry's engine is checked after the store returns it.
func TestHooksCheckACustomEntrysEngine(t *testing.T) {
	ctx := context.Background()
	const revision = "abcdef0123456789abcdef0123456789abcdef01"
	e := registry.Entry{
		ID: engines.CustomEntryID("acme/embed-x", revision), Title: "acme/embed-x", Revision: revision,
		Tasks: []registry.Task{registry.TaskEmbedding}, ContextLength: 512,
		Quantizations: []registry.Quantization{{ID: "native", Engine: registry.EngineTEI, WeightBytes: 1 << 20}},
	}
	settings := engines.Settings(engines.Spec{Entry: e, Quantization: e.Quantizations[0], ServedName: "embed-x", MaxModelLen: 512})
	h := NewHooks(HooksDeps{Registry: registry.Default(), Custom: customEntries{e.ID: e}})

	if _, err := h.Files(ctx, run(t, recipes.ModelTEIKind, recipes.ModelEngine, settings, 0)); err != nil {
		t.Fatalf("on its own engine: %v", err)
	}
	_, err := h.Files(ctx, run(t, recipes.ModelVLLMKind, recipes.ModelEngine, settings, 1))
	if err == nil || !strings.Contains(err.Error(), "runs on tei, which model-vllm doesn't run. Deploy it as model-tei") {
		t.Errorf("on vLLM: %v", err)
	}
}

// Ruling M42: with ai.models.huggingface_url on a mirror, vLLM and Text
// Embeddings Inference get it as HF_ENDPOINT through the endpoint file,
// and llama.cpp downloads its GGUF from it. huggingface.co, with or
// without a trailing slash, is no mirror.
func TestEnginesDownloadFromTheMirror(t *testing.T) {
	ctx := context.Background()
	reg := fixture(t, llamaRevision, "--enforce-eager")
	tokens := &fakeTokens{token: hfToken, revision: 1}
	const mirror = "https://hf-mirror.example.com"

	for _, url := range []string{"", "https://huggingface.co", "https://huggingface.co/"} {
		h := NewHooks(HooksDeps{Registry: reg, Settings: tokens, HuggingFaceURL: url})
		files, err := h.Files(ctx, run(t, recipes.ModelVLLMKind, recipes.ModelEngine, settingsFor(t, reg, llamaID, "bf16"), 1))
		if err != nil || len(files[recipes.ModelEndpointPath]) != 0 {
			t.Errorf("%q: endpoint %q, %v", url, files[recipes.ModelEndpointPath], err)
		}
	}

	h := NewHooks(HooksDeps{Registry: reg, Settings: tokens, HuggingFaceURL: mirror + "/"})
	files, err := h.Files(ctx, run(t, recipes.ModelVLLMKind, recipes.ModelEngine, settingsFor(t, reg, llamaID, "bf16"), 1))
	if err != nil || string(files[recipes.ModelEndpointPath]) != mirror {
		t.Errorf("vLLM endpoint %q, %v", files[recipes.ModelEndpointPath], err)
	}
	if strings.Contains(string(files[recipes.ModelArgsPath]), "huggingface.co") {
		t.Errorf("vLLM args name huggingface.co:\n%s", files[recipes.ModelArgsPath])
	}

	bge := NewHooks(HooksDeps{Registry: registry.Default(), HuggingFaceURL: mirror})
	files, err = bge.Files(ctx, run(t, recipes.ModelTEIKind, recipes.ModelEngine, settingsFor(t, registry.Default(), "BAAI/bge-small-en-v1.5", "native"), 0))
	if err != nil || string(files[recipes.ModelEndpointPath]) != mirror {
		t.Errorf("TEI endpoint %q, %v", files[recipes.ModelEndpointPath], err)
	}

	gguf := settingsFor(t, reg, llamaID, "gguf-q4_k_m")
	for _, kind := range []string{recipes.ModelLlamaCPPKind, recipes.ModelLlamaCPPGPUKind} {
		gpus := 0
		if kind == recipes.ModelLlamaCPPGPUKind {
			gpus = 1
		}
		files, err = h.Files(ctx, run(t, kind, recipes.ModelEngine, gguf, gpus))
		if err != nil {
			t.Fatal(err)
		}
		want := "--model-url=" + mirror + "/bartowski/Meta-Llama-3.1-8B-Instruct-GGUF/resolve/" + ggufRevision + "/Meta-Llama-3.1-8B-Instruct-Q4_K_M.gguf\n"
		if !strings.Contains(string(files[recipes.ModelArgsPath]), want) || len(files[recipes.ModelEndpointPath]) != 0 {
			t.Errorf("%s: endpoint %q, args:\n%s", kind, files[recipes.ModelEndpointPath], files[recipes.ModelArgsPath])
		}
	}
}
