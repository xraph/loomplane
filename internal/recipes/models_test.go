package recipes_test

import (
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/recipes"
)

// modelDigests reads the pins the capture script wrote. index_digest is
// part of the file, so the decoder must take it.
func modelDigests(t *testing.T) map[string]map[string]string {
	t.Helper()
	raw, err := os.ReadFile("testdata/model-engine-image-digests.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Images map[string]struct {
			Repository  string            `json:"repository"`
			IndexDigest string            `json:"index_digest"`
			Digests     map[string]string `json:"digests"`
		} `json:"images"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]string{}
	for key, image := range file.Images {
		if image.IndexDigest == "" {
			t.Errorf("%s has no index digest", key)
		}
		pinned := map[string]string{}
		for arch, digest := range image.Digests {
			pinned[arch] = image.Repository + "@" + digest
		}
		out[key] = pinned
	}
	return out
}

// modelImageKeys names each model kind's engine image in the digests file.
var modelImageKeys = map[string]string{
	recipes.ModelVLLMKind:        "vllm",
	recipes.ModelTEIKind:         "tei",
	recipes.ModelLlamaCPPKind:    "llamacpp",
	recipes.ModelLlamaCPPGPUKind: "llamacpp-gpu",
}

func modelKind(t *testing.T, name string) recipes.Kind {
	t.Helper()
	k, ok := recipes.Default().Kind(name)
	if !ok {
		t.Fatalf("%s is not registered", name)
	}
	return k
}

func component(t *testing.T, k recipes.Kind, name string) recipes.Component {
	t.Helper()
	i := slices.IndexFunc(k.Components, func(c recipes.Component) bool { return c.Name == name })
	if i < 0 {
		t.Fatalf("%s has no %s component", k.Name, name)
	}
	return k.Components[i]
}

func TestModelKindsResolve(t *testing.T) {
	digests := modelDigests(t)
	for _, name := range recipes.ModelKinds() {
		for _, target := range []string{"server", "kubernetes"} {
			c, err := recipes.Default().Resolve(name, "", "amd64", target)
			if err != nil {
				t.Fatalf("%s on amd64 %s: %v", name, target, err)
			}
			if len(c.Components) != 2 || c.Components[0].Name != recipes.ModelEngine || c.Components[1].Name != recipes.ModelGuard {
				t.Fatalf("%s components: %+v", name, c.Components)
			}
			if c.Components[0].Step != 0 || c.Components[1].Step != 1 {
				t.Errorf("%s steps: engine %d, guard %d", name, c.Components[0].Step, c.Components[1].Step)
			}
			if got, want := c.Components[0].Image, digests[modelImageKeys[name]]["amd64"]; got != want || want == "" {
				t.Errorf("%s engine image %q, want %q", name, got, want)
			}
			if got, want := c.Components[1].Image, digests["guard"]["amd64"]; got != want || want == "" {
				t.Errorf("%s guard image %q, want %q", name, got, want)
			}
		}
	}
}

func TestGPUModelKindsAreAmd64Only(t *testing.T) {
	digests := modelDigests(t)
	for _, name := range []string{recipes.ModelVLLMKind, recipes.ModelLlamaCPPGPUKind} {
		k := modelKind(t, name)
		if component(t, k, recipes.ModelEngine).Resources.GPU == nil {
			t.Errorf("%s engine requests no GPU", name)
		}
		if _, ok := k.Releases[0].Images[recipes.ModelEngine].Digests["arm64"]; ok {
			t.Errorf("%s pins an arm64 engine image", name)
		}
		for _, target := range []string{"server", "kubernetes"} {
			_, err := recipes.Default().Resolve(name, "", "arm64", target)
			if err == nil || !strings.Contains(err.Error(), "runs on NVIDIA GPUs and needs an amd64 target") {
				t.Errorf("%s on arm64 %s: %v", name, target, err)
			}
		}
	}
	for _, name := range []string{recipes.ModelTEIKind, recipes.ModelLlamaCPPKind} {
		if component(t, modelKind(t, name), recipes.ModelEngine).Resources.GPU != nil {
			t.Errorf("%s engine requests a GPU", name)
		}
		c, err := recipes.Default().Resolve(name, "", "arm64", "server")
		if err != nil {
			t.Fatalf("%s on arm64: %v", name, err)
		}
		if got, want := c.Components[0].Image, digests[modelImageKeys[name]]["arm64"]; got != want || want == "" {
			t.Errorf("%s arm64 engine image %q, want %q", name, got, want)
		}
		if got, want := c.Components[1].Image, digests["guard"]["arm64"]; got != want || want == "" {
			t.Errorf("%s arm64 guard image %q, want %q", name, got, want)
		}
	}
}

func TestModelKindShape(t *testing.T) {
	const repo = `[A-Za-z0-9][A-Za-z0-9._-]{0,95}/[A-Za-z0-9][A-Za-z0-9._-]{0,95}`
	settings := func(gguf bool) []recipes.Setting {
		out := []recipes.Setting{
			{Name: "entry", Pattern: `^(` + repo + `|hf:` + repo + `@[0-9a-f]{12})$`, Required: true},
			{Name: "model", Pattern: `^` + repo + `$`, Required: true},
			{Name: "revision", Pattern: `^[0-9a-f]{40}$`, Required: true},
			{Name: "quantization", Pattern: `^[a-z0-9_-]{1,32}$`, Required: true},
			{Name: "served_name", Pattern: `^[A-Za-z0-9._:/-]{1,128}$`, Required: true},
			{Name: "max_model_len", Pattern: `^[1-9][0-9]{2,6}$`, Default: "4096"},
		}
		if gguf {
			out = append(out, recipes.Setting{Name: "file", Pattern: `^[A-Za-z0-9._-]{1,200}\.gguf$`, Required: true})
		}
		return append(out, recipes.Setting{Name: "cache_mb", Pattern: `^[1-9][0-9]{3,7}$`, Default: "20480"})
	}
	llamaEnv := []recipes.EnvVar{{Name: "LLAMA_CACHE", Value: "/cache"}, {Name: "LLAMA_API_KEY", Secret: "api-key"}}
	for _, tc := range []struct {
		kind, version, product string
		args                   []string
		gguf, gpu              bool
		resources              recipes.Resources
		start                  time.Duration
		env                    []recipes.EnvVar
	}{
		{
			kind: recipes.ModelVLLMKind, version: "0.31", product: "0.31.0", args: []string{"vllm", "serve"}, gpu: true,
			resources: recipes.Resources{
				CPUMillis: 1000, MemoryMB: 6144, ShmMB: 2048, GPU: &recipes.GPU{Count: 1},
				PerGPU: &recipes.ResourceStep{CPUMillis: 1500, MemoryMB: 6144, ShmMB: 2048},
			},
			start: 2 * time.Hour,
			env: []recipes.EnvVar{
				{Name: "HF_HOME", Value: "/cache/huggingface"}, {Name: "VLLM_API_KEY", Secret: "api-key"},
				{Name: "VLLM_NO_USAGE_STATS", Value: "1"}, {Name: "DO_NOT_TRACK", Value: "1"},
			},
		},
		{
			kind: recipes.ModelTEIKind, version: "1.9", product: "1.9.4", args: []string{"text-embeddings-router"},
			resources: recipes.Resources{CPUMillis: 2000, MemoryMB: 4096}, start: 30 * time.Minute,
			env: []recipes.EnvVar{{Name: "HUGGINGFACE_HUB_CACHE", Value: "/cache"}},
		},
		{
			kind: recipes.ModelLlamaCPPKind, version: "11580", product: "b11580", args: []string{"/app/llama-server"}, gguf: true,
			resources: recipes.Resources{CPUMillis: 4000, MemoryMB: 8192}, start: time.Hour, env: llamaEnv,
		},
		{
			kind: recipes.ModelLlamaCPPGPUKind, version: "11580", product: "b11580", args: []string{"/app/llama-server"}, gguf: true, gpu: true,
			resources: recipes.Resources{
				CPUMillis: 1000, MemoryMB: 4096, GPU: &recipes.GPU{Count: 1},
				PerGPU: &recipes.ResourceStep{CPUMillis: 500, MemoryMB: 2048},
			},
			start: time.Hour, env: llamaEnv,
		},
	} {
		k := modelKind(t, tc.kind)
		if k.Placement != recipes.PlacementAppProject || !slices.Equal(k.Targets, []string{"server", "kubernetes"}) {
			t.Errorf("%s placement %s targets %v", tc.kind, k.Placement, k.Targets)
		}
		if k.Feature != recipes.FeatureAI || !k.EnvironmentOptIn {
			t.Errorf("%s feature %q, opt-in %v", tc.kind, k.Feature, k.EnvironmentOptIn)
		}
		if !recipes.Default().EnvironmentOptIn(tc.kind) {
			t.Errorf("the registry doesn't say %s opts in to environments", tc.kind)
		}
		if want := []recipes.GeneratedSecret{{Name: "api-key", Bytes: 32, Encoding: "base64url", Prefix: "lpm_"}}; !reflect.DeepEqual(k.Secrets, want) {
			t.Errorf("%s secrets %+v", tc.kind, k.Secrets)
		}
		if len(k.Dependencies) != 0 || len(k.ConsumerOptions) != 0 {
			t.Errorf("%s has dependencies %+v or consumer options %+v", tc.kind, k.Dependencies, k.ConsumerOptions)
		}
		if len(k.Releases) != 1 || k.Releases[0].Version != tc.version || k.Releases[0].Product != tc.product || k.Releases[0].Revision != "1" {
			t.Errorf("%s releases %+v", tc.kind, k.Releases)
		}

		got := make([]recipes.Setting, len(k.Settings))
		for i, s := range k.Settings {
			if s.Title == "" {
				t.Errorf("%s setting %s has no title", tc.kind, s.Name)
			}
			s.Title = ""
			got[i] = s
		}
		if want := settings(tc.gguf); !reflect.DeepEqual(got, want) {
			t.Errorf("%s settings\n got %+v\nwant %+v", tc.kind, got, want)
		}

		wantOutputs := []recipes.Output{
			{Name: "url", EnvKey: "MODEL_URL", From: &recipes.Ref{Component: "guard", Field: "url:http"}},
			{Name: "api-key", EnvKey: "MODEL_API_KEY", Secret: true, Template: "${secret:api-key}"},
		}
		if !reflect.DeepEqual(k.Outputs, wantOutputs) {
			t.Errorf("%s outputs %+v", tc.kind, k.Outputs)
		}

		engine := component(t, k, recipes.ModelEngine)
		if engine.Role != recipes.RoleProcess || !slices.Equal(engine.Args, tc.args) {
			t.Errorf("%s engine role %s args %v", tc.kind, engine.Role, engine.Args)
		}
		if !reflect.DeepEqual(engine.Readiness, &recipes.HTTPProbe{Port: "http", Path: "/health"}) {
			t.Errorf("%s engine readiness %+v", tc.kind, engine.Readiness)
		}
		if want := []recipes.File{{Path: recipes.ModelArgsPath, Mode: 0o444}, {Path: recipes.ModelTokenPath, Mode: 0o400}}; !reflect.DeepEqual(engine.Files, want) {
			t.Errorf("%s engine files %+v", tc.kind, engine.Files)
		}
		if !reflect.DeepEqual(engine.Resources, tc.resources) || engine.StartTimeout != tc.start {
			t.Errorf("%s engine resources %+v start %s", tc.kind, engine.Resources, engine.StartTimeout)
		}
		if !reflect.DeepEqual(engine.Env, tc.env) {
			t.Errorf("%s engine env %+v", tc.kind, engine.Env)
		}
		if tc.gpu {
			want := &recipes.WeightCache{MountPath: "/cache", KubernetesSizeMB: 20480, SizeSetting: "cache_mb"}
			if !reflect.DeepEqual(engine.WeightCache, want) || engine.Volume != nil || engine.Protection != recipes.ProtectionNone {
				t.Errorf("%s engine cache %+v volume %+v protection %s", tc.kind, engine.WeightCache, engine.Volume, engine.Protection)
			}
		} else {
			want := &recipes.Volume{MountPath: "/cache", KubernetesSizeMB: 20480}
			if !reflect.DeepEqual(engine.Volume, want) || engine.WeightCache != nil || engine.Protection != recipes.ProtectionRebuildable {
				t.Errorf("%s engine volume %+v cache %+v protection %s", tc.kind, engine.Volume, engine.WeightCache, engine.Protection)
			}
		}

		guard := component(t, k, recipes.ModelGuard)
		wantEnv := []recipes.EnvVar{
			{Name: "LOOMPLANE_ENGINE_ADDR", From: &recipes.Ref{Component: "engine", Field: "address:http"}},
			{Name: "LOOMPLANE_MODEL_KEY", Secret: "api-key"},
		}
		if !reflect.DeepEqual(guard.Env, wantEnv) {
			t.Errorf("%s guard env %+v", tc.kind, guard.Env)
		}
		if len(guard.Command) != 0 || len(guard.Args) != 0 {
			t.Errorf("%s guard overrides the image's command: %v %v", tc.kind, guard.Command, guard.Args)
		}
		if !reflect.DeepEqual(guard.Files, []recipes.File{{Path: "/etc/caddy/Caddyfile", Mode: 0o444}}) {
			t.Errorf("%s guard files %+v", tc.kind, guard.Files)
		}
		if !reflect.DeepEqual(guard.Readiness, &recipes.HTTPProbe{Port: "http", Path: "/health"}) {
			t.Errorf("%s guard readiness %+v", tc.kind, guard.Readiness)
		}
		if guard.Role != recipes.RoleProcess || guard.Protection != recipes.ProtectionNone || guard.Volume != nil || guard.WeightCache != nil {
			t.Errorf("%s guard %+v", tc.kind, guard)
		}
		if guard.Resources != (recipes.Resources{CPUMillis: 100, MemoryMB: 64}) || guard.StartTimeout != 0 {
			t.Errorf("%s guard resources %+v start %s", tc.kind, guard.Resources, guard.StartTimeout)
		}
	}
}

func TestEngineIsNeverRoutable(t *testing.T) {
	for _, name := range recipes.ModelKinds() {
		k := modelKind(t, name)
		engine := component(t, k, recipes.ModelEngine)
		if want := []recipes.Port{{Name: "http", Container: 8000, Protocol: "http"}}; !reflect.DeepEqual(engine.Ports, want) {
			t.Errorf("%s engine ports %+v, want only 8000, neither routable nor control", name, engine.Ports)
		}
		guard := component(t, k, recipes.ModelGuard)
		if want := []recipes.Port{{Name: "http", Container: 8080, Protocol: "http", Routable: true}}; !reflect.DeepEqual(guard.Ports, want) {
			t.Errorf("%s guard ports %+v, want only a routable 8080", name, guard.Ports)
		}
		if !slices.Equal(guard.After, []string{recipes.ModelEngine}) || len(engine.After) != 0 {
			t.Errorf("%s guard after %v, engine after %v", name, guard.After, engine.After)
		}
		// The outputs point consumers at the guard, never the engine.
		for _, o := range k.Outputs {
			if o.From != nil && o.From.Component != recipes.ModelGuard {
				t.Errorf("%s output %s reads %+v", name, o.Name, o.From)
			}
		}
	}
}

func TestEngineWrapperIsTheConstant(t *testing.T) {
	for _, name := range recipes.ModelKinds() {
		engine := component(t, modelKind(t, name), recipes.ModelEngine)
		if want := []string{"/bin/sh", "-c", recipes.ModelEngineWrapper, "loomplane-engine"}; !slices.Equal(engine.Command, want) {
			t.Errorf("%s engine command %q", name, engine.Command)
		}
	}
	w := recipes.ModelEngineWrapper
	if strings.ContainsAny(w, "\u2013\u2014") {
		t.Error("the wrapper has an en or em dash")
	}
	for _, path := range []string{recipes.ModelArgsPath, recipes.ModelTokenPath} {
		if !strings.Contains(w, path) {
			t.Errorf("the wrapper doesn't read %s", path)
		}
	}
	if strings.Contains(w, "set -x") {
		t.Error("the wrapper traces, which would print the token")
	}
	out, err := exec.CommandContext(t.Context(), "sh", "-n", "-c", w).CombinedOutput()
	if err != nil {
		t.Fatalf("sh -n: %v: %s", err, out)
	}
}

func TestModelKindFor(t *testing.T) {
	for _, tc := range []struct {
		engine string
		gpu    bool
		want   string
	}{
		{"vllm", true, recipes.ModelVLLMKind},
		{"vllm", false, ""},
		{"tei", false, recipes.ModelTEIKind},
		{"tei", true, ""},
		{"llamacpp", false, recipes.ModelLlamaCPPKind},
		{"llamacpp", true, recipes.ModelLlamaCPPGPUKind},
		{"ollama", false, ""},
		{"", true, ""},
	} {
		got, ok := recipes.ModelKindFor(tc.engine, tc.gpu)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("ModelKindFor(%q, %v) = %q, %v; want %q", tc.engine, tc.gpu, got, ok, tc.want)
		}
	}
	// The engine names are the model registry's.
	for _, e := range []registry.Engine{registry.EngineVLLM, registry.EngineTEI, registry.EngineLlamaCPP} {
		if _, ok := recipes.ModelKindFor(string(e), e != registry.EngineTEI); !ok {
			t.Errorf("registry engine %s has no model kind", e)
		}
	}
}

func TestModelKindsAreListed(t *testing.T) {
	kinds := recipes.ModelKinds()
	if !slices.Equal(kinds, []string{recipes.ModelVLLMKind, recipes.ModelTEIKind, recipes.ModelLlamaCPPKind, recipes.ModelLlamaCPPGPUKind}) {
		t.Fatalf("ModelKinds() = %v", kinds)
	}
	kinds[0] = "changed"
	if recipes.ModelKinds()[0] != recipes.ModelVLLMKind {
		t.Error("ModelKinds() returns the package's own slice")
	}
	for _, name := range recipes.ModelKinds() {
		if !recipes.IsModelKind(name) {
			t.Errorf("IsModelKind(%q) is false", name)
		}
	}
	for _, name := range []string{"octopus", "model", "", "model-vllm-gpu"} {
		if recipes.IsModelKind(name) {
			t.Errorf("IsModelKind(%q) is true", name)
		}
	}
}

// TestModelReleasesFollowEngines keeps each kind's release on the engine
// version internal/models/engines captured flags and fits for.
func TestModelReleasesFollowEngines(t *testing.T) {
	releases := map[string]engines.Release{}
	for _, r := range engines.Releases() {
		releases[string(r.Engine)] = r
	}
	for _, tc := range []struct {
		engine string
		kinds  []string
	}{
		{"vllm", []string{recipes.ModelVLLMKind}},
		{"tei", []string{recipes.ModelTEIKind}},
		{"llamacpp", []string{recipes.ModelLlamaCPPKind, recipes.ModelLlamaCPPGPUKind}},
	} {
		want, ok := releases[tc.engine]
		if !ok {
			t.Fatalf("engines has no %s release", tc.engine)
		}
		for _, name := range tc.kinds {
			got := modelKind(t, name).Releases[0]
			if got.Version != want.Version || got.Product != want.Product {
				t.Errorf("%s release %s (%s), engines has %s (%s)", name, got.Version, got.Product, want.Version, want.Product)
			}
		}
	}
	if got := slices.Sorted(maps.Keys(releases)); !slices.Equal(got, []string{"llamacpp", "tei", "vllm"}) {
		t.Errorf("engines releases %v", got)
	}
}

// TestModelEntrySettingTakesEveryEntryID: the entry setting takes every
// registry ID and a custom entry's ID exactly as engines.CustomEntryID
// writes it, and nothing in between.
func TestModelEntrySettingTakesEveryEntryID(t *testing.T) {
	const revision = "4f6a1c2b9d8e7f60a1b2c3d4e5f60718293a4b5c"
	entries := registry.Default().Entries()
	good := make([]string, 0, len(entries)+2)
	good = append(good, engines.CustomEntryID("acme/chat-7b", revision), engines.CustomEntryID("Qwen/Qwen2.5-7B-Instruct", revision))
	for _, e := range entries {
		good = append(good, e.ID)
	}
	bad := []string{
		"",
		"hf:acme/chat-7b",                   // no revision
		"hf:acme/chat-7b@4f6a1c2b9d8",       // 11 hex
		"hf:acme/chat-7b@4f6a1c2b9d8e7",     // 13 hex
		"hf:acme/chat-7b@" + revision,       // the whole SHA
		"hf:acme/chat-7b@4F6A1C2B9D8E",      // upper case
		"acme/chat-7b@4f6a1c2b9d8e",         // a revision without hf:
		"hf:acme@4f6a1c2b9d8e",              // no repo name
		"hf:hf:acme/chat-7b@4f6a1c2b9d8e",   // prefix twice
		"hf:acme/chat-7b@4f6a1c2b9d8e\nx/y", // a second line
	}
	for _, name := range recipes.ModelKinds() {
		kind, ok := recipes.Default().Kind(name)
		if !ok {
			t.Fatalf("no kind %s", name)
		}
		var entry recipes.Setting
		for _, s := range kind.Settings {
			if s.Name == "entry" {
				entry = s
			}
		}
		for _, id := range good {
			if !entry.Matches(id) {
				t.Errorf("%s entry setting refuses %q", name, id)
			}
		}
		for _, id := range bad {
			if entry.Matches(id) {
				t.Errorf("%s entry setting takes %q", name, id)
			}
		}
	}
}
