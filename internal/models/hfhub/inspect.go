package hfhub

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/registry"
)

// ErrRefused marks a repo Loomplane won't build an entry from. The error's
// text says why, in a sentence the dashboard shows as it is.
var ErrRefused = errors.New("refused: the model can't be served")

// refusedError is a refusal: its text is the sentence, and it matches
// ErrRefused.
type refusedError struct{ msg string }

func (e *refusedError) Error() string { return e.msg }
func (e *refusedError) Unwrap() error { return ErrRefused }

func refuse(format string, args ...any) error {
	return &refusedError{msg: fmt.Sprintf(format, args...)}
}

// CustomFamily is the family of every entry built from a pasted repo.
const CustomFamily = "custom"

// CustomSummary is the summary of every entry built from a pasted repo.
const CustomSummary = "Added from Hugging Face. Loomplane hasn't tested it."

// teiMaxWeightBytes is the most weight TEI is offered for on CPU: 1.5 GiB.
const teiMaxWeightBytes = 3 << 29

// ggufSuffixes maps the end of a single-file GGUF name to its
// quantization, in the order an entry lists them, best quality first.
var ggufSuffixes = []struct{ suffix, id string }{
	{"q8_0.gguf", "gguf-q8_0"},
	{"mxfp4.gguf", "gguf-mxfp4"},
	{"q4_k_m.gguf", "gguf-q4_k_m"},
}

// splitGGUF matches a GGUF file split into parts, which llama.cpp's
// --model-url can't download.
var splitGGUF = regexp.MustCompile(`(?i)-\d{5}-of-\d{5}\.gguf$`)

// quantMethods maps config.json's quantization_config.quant_method to the
// quantization an entry calls it.
var quantMethods = map[string]string{"fp8": "fp8", "awq": "awq-int4", "mxfp4": "mxfp4"}

// Build makes an untested entry from a repo's metadata and its config.json,
// both read at info.SHA. Its ID is engines.CustomEntryID(info.ID, info.SHA)
// and it never asks for the repo's own code, whatever the config says. A
// repo Loomplane can't serve is ErrRefused, with the reason.
//
// Single-file GGUF weights ending Q8_0, MXFP4 or Q4_K_M become llama.cpp
// quantizations. Without them the config gives one vLLM quantization when
// the pinned vLLM knows the architecture, and embedding and rerank models
// TEI knows also get "native" on TEI when their weights are under 1.5 GiB.
func Build(info *ModelInfo, config []byte) (registry.Entry, error) {
	if info == nil || !ValidRepo(info.ID) {
		return registry.Entry{}, errors.New("hfhub: Build needs a model's metadata")
	}
	repo := info.ID
	if !shaPattern.MatchString(info.SHA) {
		return registry.Entry{}, fmt.Errorf("hfhub: %s came back without a 40-character commit SHA", repo)
	}
	tasks, err := tasksOf(info)
	if err != nil {
		return registry.Entry{}, err
	}
	var cfg map[string]any
	if json.Unmarshal(config, &cfg) != nil || cfg == nil {
		return registry.Entry{}, refuse("%s's config.json isn't JSON Loomplane can read.", repo)
	}
	arch, contextLength, err := ArchitectureOf(cfg)
	if err != nil {
		return registry.Entry{}, refuse("Loomplane can't work out how much memory %s needs: its config.json %v.", repo, err)
	}

	e := registry.Entry{
		ID:            engines.CustomEntryID(repo, info.SHA),
		Family:        CustomFamily,
		Title:         repo,
		Publisher:     strings.SplitN(repo, "/", 2)[0],
		Summary:       CustomSummary,
		Revision:      info.SHA,
		License:       registry.License{ID: info.License, Commercial: commercial(info.License)},
		Gated:         info.Gated,
		Tasks:         tasks,
		ContextLength: contextLength,
		Architecture:  arch,
	}
	if hasTask(tasks, registry.TaskEmbedding) {
		dims := lookupOf(cfg).int("hidden_size", "d_model", "n_embd")
		if dims <= 0 {
			return registry.Entry{}, refuse("%s's config.json gives no hidden_size, so Loomplane can't say how long its vectors are.", repo)
		}
		e.Embedding = &registry.Embedding{Dimensions: dims, MaxInputTokens: contextLength}
	}

	e.Quantizations = ggufQuantizations(info.Files, tasks)
	if len(e.Quantizations) == 0 {
		weights := WeightBytes(info.Files)
		if hasTask(tasks, registry.TaskEmbedding) || hasTask(tasks, registry.TaskRerank) {
			if engines.SupportsArchitecture(registry.EngineTEI, arch.Name) && weights > 0 && weights < teiMaxWeightBytes {
				e.Quantizations = append(e.Quantizations, registry.Quantization{ID: "native", Engine: registry.EngineTEI, WeightBytes: weights})
			}
		}
		if !hasTask(tasks, registry.TaskRerank) && engines.SupportsArchitecture(registry.EngineVLLM, arch.Name) {
			e.Quantizations = append(e.Quantizations, registry.Quantization{ID: vllmQuantization(cfg), Engine: registry.EngineVLLM, WeightBytes: weights})
		}
		if len(e.Quantizations) > 0 && weights <= 0 {
			return registry.Entry{}, refuse("%s has no .safetensors or pytorch_model .bin weights at its top level.", repo)
		}
	}
	if len(e.Quantizations) == 0 {
		if needsRepositoryCode(cfg) {
			return registry.Entry{}, refuse("%s needs code from its repository to run, and Loomplane doesn't run repository code for pasted models.", repo)
		}
		return registry.Entry{}, refuse("No engine Loomplane ships can serve %s.", arch.Name)
	}

	e.Parameters = info.Params
	if e.Parameters <= 0 {
		e.Parameters = parametersFromWeights(cfg, e.Quantizations)
	}
	for i, q := range e.Quantizations {
		q.MinGPUMemoryGiB = registry.MinGPUMemoryGiB(e, q)
		q.MinMemoryGiB = registry.MinMemoryGiB(e, q)
		e.Quantizations[i] = q
	}
	if err := validate(e, repo); err != nil {
		return registry.Entry{}, err
	}
	return e, nil
}

// tasksOf reads the tasks from the repo's pipeline tag.
func tasksOf(info *ModelInfo) ([]registry.Task, error) {
	switch info.PipelineTag {
	case "text-generation":
		return []registry.Task{registry.TaskChat}, nil
	case "image-text-to-text":
		return []registry.Task{registry.TaskVision}, nil
	case "feature-extraction", "sentence-similarity":
		return []registry.Task{registry.TaskEmbedding}, nil
	case "automatic-speech-recognition":
		return []registry.Task{registry.TaskSpeech}, nil
	case "text-classification":
		for _, t := range info.Tags {
			if t = strings.ToLower(t); t == "reranker" || t == "cross-encoder" {
				return []registry.Task{registry.TaskRerank}, nil
			}
		}
	case "":
		return nil, refuse("Loomplane serves chat, vision, embedding, reranking and speech models. %s doesn't say on Hugging Face what it does.", info.ID)
	}
	return nil, refuse("Loomplane serves chat, vision, embedding, reranking and speech models. %s is %s.", info.ID, info.PipelineTag)
}

func hasTask(tasks []registry.Task, t registry.Task) bool {
	for _, have := range tasks {
		if have == t {
			return true
		}
	}
	return false
}

// commercial reports whether the registry knows the license and allows
// commercial use under it. A license it doesn't know never counts.
func commercial(id string) bool {
	for _, l := range registry.Licenses() {
		if l.ID == id {
			return l.Commercial
		}
	}
	return false
}

// ggufQuantizations finds the single-file GGUF weights at the top of the
// repo, one per quantization, when llama.cpp can serve every task.
func ggufQuantizations(files []FileInfo, tasks []registry.Task) []registry.Quantization {
	for _, t := range tasks {
		if t != registry.TaskChat && t != registry.TaskReasoning && t != registry.TaskCode {
			return nil
		}
	}
	var out []registry.Quantization
	for _, s := range ggufSuffixes {
		for _, f := range files {
			name := strings.ToLower(f.Name)
			if strings.Contains(f.Name, "/") || f.Size <= 0 || splitGGUF.MatchString(name) || !strings.HasSuffix(name, s.suffix) {
				continue
			}
			out = append(out, registry.Quantization{ID: s.id, Engine: registry.EngineLlamaCPP, File: f.Name, WeightBytes: f.Size})
			break
		}
	}
	return out
}

// vllmQuantization names the weights the config describes.
func vllmQuantization(cfg map[string]any) string {
	if qc, ok := cfg["quantization_config"].(map[string]any); ok {
		if id, known := quantMethods[strings.ToLower(asString(qc["quant_method"]))]; known {
			return id
		}
	}
	return "bf16"
}

// needsRepositoryCode reports whether the config points at Python in the
// repo (auto_map), which Loomplane never runs for a pasted model.
func needsRepositoryCode(cfg map[string]any) bool {
	for _, m := range lookupOf(cfg) {
		if v, ok := m["auto_map"]; ok && v != nil {
			return true
		}
	}
	return false
}

// parametersFromWeights works the parameter count out from the weights'
// size and the dtype config.json names, two bytes a weight when it names
// none Loomplane knows.
func parametersFromWeights(cfg map[string]any, qs []registry.Quantization) int64 {
	dtype := asString(cfg["torch_dtype"])
	if dtype == "" {
		dtype = asString(cfg["dtype"])
	}
	size := map[string]int64{"float32": 4, "float16": 2, "bfloat16": 2}[dtype]
	if size == 0 {
		size = 2
	}
	return qs[0].WeightBytes / size
}

// validate runs the registry's own checks on e. A pasted repo's ID and
// license aren't a registry entry's, so those two are checked here and
// left out of the registry's.
func validate(e registry.Entry, repo string) error {
	if e.TrustRemoteCode {
		return refuse("%s asks to run code from its repository, and Loomplane doesn't for pasted models.", repo)
	}
	check := e
	check.ID = repo
	check.License = registry.License{ID: "apache-2.0", Commercial: true}
	if err := registry.Validate(check); err != nil {
		first := strings.SplitN(err.Error(), "\n", 2)[0]
		return refuse("Loomplane can't use %s: %s.", repo, strings.TrimPrefix(first, repo+": "))
	}
	return nil
}

// variantShard matches a second copy of the weights in another dtype,
// like model.fp32-00001-of-00002.safetensors next to model.safetensors.
// It holds the same tensors, so counting it would double the weights.
var variantShard = regexp.MustCompile(`^[^/]+\.(fp16|fp32|bf16)(-\d+-of-\d+)?\.safetensors$`)

// WeightBytes is the size of the weights an engine loads: every top-level
// .safetensors file except Mistral's consolidated copies and second dtype
// variants, or the pytorch_model .bin files when there are no safetensors.
func WeightBytes(files []FileInfo) int64 {
	var safetensors, bin int64
	for _, f := range files {
		name := f.Name
		switch {
		case strings.Contains(name, "/"):
		case strings.HasSuffix(name, ".safetensors"):
			if !strings.HasPrefix(name, "consolidated") && !variantShard.MatchString(name) {
				safetensors += f.Size
			}
		case strings.HasPrefix(name, "pytorch_model") && strings.HasSuffix(name, ".bin"):
			bin += f.Size
		}
	}
	if safetensors > 0 {
		return safetensors
	}
	return bin
}

// attentionLayerTypes are the layer_types and layers_block_type entries that
// keep a KV cache per token. Sliding-window layers count as full ones, so
// the estimate never undercounts the cache; mamba and linear attention
// layers keep none per token.
var attentionLayerTypes = map[string]bool{
	"attention": true, "full_attention": true, "sliding_attention": true, "chunked_attention": true,
}

// ArchitectureOf reads the facts the memory math needs from a parsed
// config.json, and the context length. Multimodal configs keep the
// language model's facts under text_config, and Whisper's decoder is what
// keeps a KV cache. The error finishes the sentence "its config.json ...".
func ArchitectureOf(cfg map[string]any) (registry.Architecture, int, error) {
	var a registry.Architecture
	if names, ok := cfg["architectures"].([]any); ok && len(names) > 0 {
		a.Name = asString(names[0])
	}
	if a.Name == "" {
		return a, 0, errors.New("names no architecture")
	}

	c := lookupOf(cfg)
	var hidden, contextLength int
	if c.has("decoder_layers") && c.has("d_model") {
		a.Layers = c.int("decoder_layers")
		a.AttentionHeads = c.int("decoder_attention_heads")
		hidden = c.int("d_model")
		contextLength = c.int("max_target_positions")
	} else {
		a.Layers = c.int("num_hidden_layers", "n_layer", "num_layers")
		a.AttentionHeads = c.int("num_attention_heads", "n_head")
		a.KVHeads = c.int("num_key_value_heads")
		a.HeadDim = c.int("head_dim")
		a.KVLoraRank = c.int("kv_lora_rank")
		a.RopeHeadDim = c.int("qk_rope_head_dim")
		hidden = c.int("hidden_size", "n_embd", "d_model")
		contextLength = c.int("max_position_embeddings", "n_positions", "seq_length", "max_sequence_length")
	}
	if a.KVHeads == 0 {
		a.KVHeads = a.AttentionHeads
	}
	if a.HeadDim == 0 && a.AttentionHeads > 0 {
		a.HeadDim = hidden / a.AttentionHeads
	}
	if n := c.attentionLayers(); n > 0 && n < a.Layers {
		a.AttentionLayers = n
	}
	switch {
	case a.Layers <= 0:
		return a, 0, errors.New("gives no layer count")
	case a.AttentionHeads <= 0:
		return a, 0, errors.New("gives no attention head count")
	case a.HeadDim <= 0:
		return a, 0, errors.New("gives no head_dim or hidden_size")
	case contextLength <= 0:
		return a, 0, errors.New("gives no context length")
	}
	return a, contextLength, nil
}

// lookup reads keys from the first map that has them.
type lookup []map[string]any

// lookupOf reads a config's text_config first, then the config itself.
func lookupOf(cfg map[string]any) lookup {
	if text, ok := cfg["text_config"].(map[string]any); ok {
		return lookup{text, cfg}
	}
	return lookup{cfg}
}

func (l lookup) has(key string) bool {
	for _, m := range l {
		if v, ok := m[key]; ok && v != nil {
			return true
		}
	}
	return false
}

// int returns the first of keys that holds a number, or 0.
func (l lookup) int(keys ...string) int {
	for _, key := range keys {
		for _, m := range l {
			if v, ok := m[key].(float64); ok {
				return int(v)
			}
		}
	}
	return 0
}

// attentionLayers counts the layers in layer_types or layers_block_type
// that keep a KV cache, or returns 0 when the config lists neither.
func (l lookup) attentionLayers() int {
	for _, key := range []string{"layer_types", "layers_block_type"} {
		for _, m := range l {
			types, ok := m[key].([]any)
			if !ok {
				continue
			}
			n := 0
			for _, t := range types {
				if attentionLayerTypes[asString(t)] {
					n++
				}
			}
			return n
		}
	}
	return 0
}

// asString returns v when it's a string, or "".
func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
