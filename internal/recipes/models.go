package recipes

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"
)

// The model kinds serve one model each, with an inference engine behind a
// guard that checks the model's key. Only the guard is routed: the engine
// listens on the environment network alone.
const (
	ModelVLLMKind        = "model-vllm"
	ModelTEIKind         = "model-tei"
	ModelLlamaCPPKind    = "model-llamacpp"
	ModelLlamaCPPGPUKind = "model-llamacpp-gpu"

	// ModelEngine is the component that runs the inference engine.
	ModelEngine = "engine"
	// ModelGuard is the component that checks the key and proxies to the engine.
	ModelGuard = "guard"
	// ModelGuardPort is the guard's port, 8080, the only one ever routed.
	ModelGuardPort = "http"
	// ModelEnginePort is the engine's port, 8000, reached only by the guard.
	ModelEnginePort = "http"
	// ModelAPIKey is the generated secret a caller presents to the guard.
	ModelAPIKey = "api-key"

	// ModelArgsPath holds the engine's arguments, one per line.
	ModelArgsPath = "/etc/loomplane/engine.args"
	// ModelTokenPath holds the Hugging Face token for gated weights, or
	// nothing.
	ModelTokenPath = "/etc/loomplane/hf/token" //nolint:gosec // G101: a file path, not a credential
	// ModelGuardConfig is the guard's Caddyfile.
	ModelGuardConfig = "/etc/caddy/Caddyfile"
	// ModelCachePath is where the engine keeps downloaded weights.
	ModelCachePath = "/cache"

	// ModelEngineWrapper starts the engine. It exports HF_TOKEN only when
	// the token file has one, so the token never reaches argv, then
	// appends each line of the args file to the engine command it was
	// given and execs it. A line is one argument, spaces and all.
	ModelEngineWrapper = "set -eu\nif [ -s /etc/loomplane/hf/token ]; then HF_TOKEN=\"$(cat /etc/loomplane/hf/token)\"; export HF_TOKEN; fi\nwhile IFS= read -r arg || [ -n \"$arg\" ]; do set -- \"$@\" \"$arg\"; done < /etc/loomplane/engine.args\nexec \"$@\"\n"
)

// Engine names ModelKindFor takes, the same as the model registry's.
const (
	modelEngineVLLM     = "vllm"
	modelEngineTEI      = "tei"
	modelEngineLlamaCPP = "llamacpp"
)

// modelKinds lists the model kinds in the order they're registered.
var modelKinds = []string{ModelVLLMKind, ModelTEIKind, ModelLlamaCPPKind, ModelLlamaCPPGPUKind}

// ModelKinds returns the name of every model kind.
func ModelKinds() []string { return slices.Clone(modelKinds) }

// IsModelKind reports whether kind is a model kind.
func IsModelKind(kind string) bool { return slices.Contains(modelKinds, kind) }

// ModelKindFor returns the model kind that runs engine, on a GPU or not.
// vLLM runs only on a GPU and Text Embeddings Inference only on CPUs, so
// asking for either the other way finds nothing.
func ModelKindFor(engine string, gpu bool) (string, bool) {
	switch {
	case engine == modelEngineVLLM && gpu:
		return ModelVLLMKind, true
	case engine == modelEngineTEI && !gpu:
		return ModelTEIKind, true
	case engine == modelEngineLlamaCPP && gpu:
		return ModelLlamaCPPGPUKind, true
	case engine == modelEngineLlamaCPP:
		return ModelLlamaCPPKind, true
	}
	return "", false
}

// modelImageDigests is captured by scripts/capture-model-engines.sh. Its
// tests check every pin against the engine releases in
// internal/models/engines.
//
//go:embed testdata/model-engine-image-digests.json
var modelImageDigests []byte

// modelImages are the pinned images by key in the digests file: "vllm",
// "tei", "llamacpp", "llamacpp-gpu" and "guard".
func modelImages() map[string]Image {
	var file struct {
		Images map[string]struct {
			Repository  string            `json:"repository"`
			Tag         string            `json:"tag"`
			IndexDigest string            `json:"index_digest"`
			Digests     map[string]string `json:"digests"`
		} `json:"images"`
	}
	if err := json.Unmarshal(modelImageDigests, &file); err != nil {
		panic(fmt.Sprintf("model engine image digests: %v", err))
	}
	out := make(map[string]Image, len(file.Images))
	for key, image := range file.Images {
		out[key] = Image{Repository: image.Repository, Digests: maps.Clone(image.Digests)}
	}
	return out
}

// Patterns of the model settings. A Hugging Face repository is an owner
// and a name. A registry entry's ID is its repository; an entry built from
// a pasted repository is "hf:<repo>@<first 12 hex of its revision>".
const (
	modelRepoPattern  = `[A-Za-z0-9][A-Za-z0-9._-]{0,95}/[A-Za-z0-9][A-Za-z0-9._-]{0,95}`
	modelEntryPattern = `^(` + modelRepoPattern + `|hf:` + modelRepoPattern + `@[0-9a-f]{12})$`
	modelCacheMB      = 20480
)

// modelSettings are what a model resource sets. The llama.cpp kinds also
// name the GGUF file they load.
func modelSettings(gguf bool) []Setting {
	settings := []Setting{
		{Name: "entry", Title: "registry entry", Pattern: modelEntryPattern, Required: true},
		{Name: "model", Title: "Hugging Face repository", Pattern: `^` + modelRepoPattern + `$`, Required: true},
		{Name: "revision", Title: "revision", Pattern: `^[0-9a-f]{40}$`, Required: true},
		{Name: "quantization", Title: "quantization", Pattern: `^[a-z0-9_-]{1,32}$`, Required: true},
		{Name: "served_name", Title: "served model name", Pattern: `^[A-Za-z0-9._:/-]{1,128}$`, Required: true},
		{Name: "max_model_len", Title: "context length", Pattern: `^[1-9][0-9]{2,6}$`, Default: "4096"},
	}
	if gguf {
		settings = append(settings, Setting{Name: "file", Title: "GGUF file", Pattern: `^[A-Za-z0-9._-]{1,200}\.gguf$`, Required: true})
	}
	return append(settings, Setting{Name: "cache_mb", Title: "weight cache size", Pattern: `^[1-9][0-9]{3,7}$`, Default: "20480"})
}

// modelKind describes what differs between the model kinds.
type modelKind struct {
	name, title, description string
	version, product         string
	image                    string // key in the digests file
	binary                   []string
	gguf                     bool
	gpu                      bool
	resources                Resources
	startTimeout             time.Duration
	env                      []EnvVar
}

func (m modelKind) kind(images map[string]Image) Kind {
	engineImage, ok := images[m.image]
	if !ok {
		panic(fmt.Sprintf("model engine image digests have no %q image", m.image))
	}
	guardImage, ok := images["guard"]
	if !ok {
		panic("model engine image digests have no guard image")
	}
	engine := Component{
		Name: ModelEngine, Role: RoleProcess,
		Command:   []string{"/bin/sh", "-c", ModelEngineWrapper, "loomplane-engine"},
		Args:      slices.Clone(m.binary),
		Ports:     []Port{{Name: ModelEnginePort, Container: 8000, Protocol: "http"}},
		Readiness: &HTTPProbe{Port: ModelEnginePort, Path: "/health"},
		Files:     []File{{Path: ModelArgsPath, Mode: 0o444}, {Path: ModelTokenPath, Mode: 0o400}},
		Env:       slices.Clone(m.env),
		Resources: m.resources, StartTimeout: m.startTimeout,
	}
	if m.gpu {
		engine.Protection = ProtectionNone
		engine.WeightCache = &WeightCache{MountPath: ModelCachePath, KubernetesSizeMB: modelCacheMB, SizeSetting: "cache_mb"}
	} else {
		// A weight cache needs a GPU component, so the CPU kinds keep
		// their downloads on an ordinary volume.
		engine.Protection = ProtectionRebuildable
		engine.Volume = &Volume{MountPath: ModelCachePath, KubernetesSizeMB: modelCacheMB}
	}
	guard := Component{
		Name: ModelGuard, Role: RoleProcess, After: []string{ModelEngine},
		Ports:      []Port{{Name: ModelGuardPort, Container: 8080, Protocol: "http", Routable: true}},
		Readiness:  &HTTPProbe{Port: ModelGuardPort, Path: "/health"},
		Files:      []File{{Path: ModelGuardConfig, Mode: 0o444}},
		Protection: ProtectionNone,
		Resources:  Resources{CPUMillis: 100, MemoryMB: 64},
		Env: []EnvVar{
			{Name: "LOOMPLANE_ENGINE_ADDR", From: &Ref{Component: ModelEngine, Field: "address:" + ModelEnginePort}},
			{Name: "LOOMPLANE_MODEL_KEY", Secret: ModelAPIKey},
		},
	}
	return Kind{
		Name: m.name, Title: m.title, Description: m.description,
		Placement: PlacementAppProject, Targets: []string{"server", "kubernetes"},
		Feature: FeatureAI, EnvironmentOptIn: true,
		Releases: []Release{{Version: m.version, Product: m.product, Revision: "1", Images: map[string]Image{
			ModelEngine: engineImage, ModelGuard: guardImage,
		}}},
		Secrets:    []GeneratedSecret{{Name: ModelAPIKey, Bytes: 32, Encoding: "base64url", Prefix: "lpm_"}},
		Settings:   modelSettings(m.gguf),
		Components: []Component{engine, guard},
		Outputs: []Output{
			{Name: "url", EnvKey: "MODEL_URL", From: &Ref{Component: ModelGuard, Field: "url:" + ModelGuardPort}},
			{Name: ModelAPIKey, EnvKey: "MODEL_API_KEY", Secret: true, Template: "${secret:" + ModelAPIKey + "}"},
		},
	}
}

// modelKindSpecs are the four model kinds. Image pins come from the
// digests file; the GPU images are amd64 only, so those kinds resolve only
// on amd64 targets. Refreshing a pin: rerun the capture script, bump the
// release's Revision and, for a new engine version, its Version and
// Product with internal/models/engines.
func modelKindSpecs() []modelKind {
	llamaEnv := []EnvVar{
		{Name: "LLAMA_CACHE", Value: ModelCachePath},
		{Name: "LLAMA_API_KEY", Secret: ModelAPIKey},
	}
	return []modelKind{
		{
			name: ModelVLLMKind, title: "vLLM model",
			description: "Serves a language model on an NVIDIA GPU with vLLM, behind a guard that checks the model's key.",
			version:     "0.31", product: "0.31.0", image: "vllm", binary: []string{"vllm", "serve"}, gpu: true,
			resources: Resources{
				CPUMillis: 1000, MemoryMB: 6144, ShmMB: 2048, GPU: &GPU{Count: 1},
				PerGPU: &ResourceStep{CPUMillis: 1500, MemoryMB: 6144, ShmMB: 2048},
			},
			startTimeout: 2 * time.Hour,
			env: []EnvVar{
				{Name: "HF_HOME", Value: ModelCachePath + "/huggingface"},
				{Name: "VLLM_API_KEY", Secret: ModelAPIKey},
				{Name: "VLLM_NO_USAGE_STATS", Value: "1"},
				{Name: "DO_NOT_TRACK", Value: "1"},
			},
		},
		{
			name: ModelTEIKind, title: "Text Embeddings Inference model",
			description: "Serves an embedding or reranking model on CPUs with Text Embeddings Inference, behind a guard that checks the model's key.",
			version:     "1.9", product: "1.9.4", image: "tei", binary: []string{"text-embeddings-router"},
			resources:    Resources{CPUMillis: 2000, MemoryMB: 4096},
			startTimeout: 30 * time.Minute,
			env:          []EnvVar{{Name: "HUGGINGFACE_HUB_CACHE", Value: ModelCachePath}},
		},
		{
			name: ModelLlamaCPPKind, title: "llama.cpp model",
			description: "Serves a GGUF model on CPUs with llama.cpp, behind a guard that checks the model's key.",
			version:     "11580", product: "b11580", image: "llamacpp", binary: []string{"/app/llama-server"}, gguf: true,
			resources:    Resources{CPUMillis: 4000, MemoryMB: 8192},
			startTimeout: time.Hour,
			env:          llamaEnv,
		},
		{
			name: ModelLlamaCPPGPUKind, title: "llama.cpp model on GPU",
			description: "Serves a GGUF model on an NVIDIA GPU with llama.cpp, behind a guard that checks the model's key.",
			version:     "11580", product: "b11580", image: "llamacpp-gpu", binary: []string{"/app/llama-server"}, gguf: true, gpu: true,
			resources: Resources{
				CPUMillis: 1000, MemoryMB: 4096, GPU: &GPU{Count: 1},
				PerGPU: &ResourceStep{CPUMillis: 500, MemoryMB: 2048},
			},
			startTimeout: time.Hour,
			env:          llamaEnv,
		},
	}
}

func init() {
	images := modelImages()
	for _, m := range modelKindSpecs() {
		MustRegister(m.kind(images))
	}
}
