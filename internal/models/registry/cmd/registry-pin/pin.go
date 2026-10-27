package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/xraph/loomplane/internal/models/registry"
)

var (
	revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

	// variantShard matches a second copy of the weights in another dtype,
	// like model.fp32-00001-of-00002.safetensors next to model.safetensors.
	// It holds the same tensors, so counting it would double the weights.
	variantShard = regexp.MustCompile(`^[^/]+\.(fp16|fp32|bf16)(-\d+-of-\d+)?\.safetensors$`)
)

// checkedLicenses are the license tags that name exactly one registry
// license. Tags like "other" can't be checked.
var checkedLicenses = map[string]bool{
	"apache-2.0": true, "mit": true, "gemma": true,
	"llama3.1": true, "llama3.2": true, "llama3.3": true, "llama4": true,
}

// attentionLayerTypes are the layer_types and layers_block_type entries that
// keep a KV cache per token. Sliding-window layers count as full ones, so
// the estimate never undercounts the cache; mamba and linear attention
// layers keep none per token.
var attentionLayerTypes = map[string]bool{
	"attention": true, "full_attention": true, "sliding_attention": true, "chunked_attention": true,
}

// pinner fills the measured fields of entries from the Hub.
type pinner struct {
	hub *hub
}

// pin returns e with every measured field filled from the repos' current
// commits. It never touches the fields written by hand.
func (p *pinner) pin(ctx context.Context, e registry.Entry) (registry.Entry, error) {
	info, err := p.hub.info(ctx, e.ID)
	if err != nil {
		return e, err
	}
	if tag := info.license(); checkedLicenses[tag] && tag != e.License.ID {
		return e, fmt.Errorf("the repo's license tag is %s, but the entry says %s", tag, e.License.ID)
	}
	if info.gated() && p.hub.token == "" {
		return e, errNeedsToken
	}
	cfg, err := p.hub.config(ctx, e.ID, info.SHA)
	if err != nil {
		return e, err
	}
	arch, contextLength, err := architectureOf(cfg)
	if err != nil {
		return e, fmt.Errorf("config.json at %s: %w", info.SHA, err)
	}

	out := e
	out.Revision = info.SHA
	out.Gated = info.gated()
	out.Architecture = arch
	out.ContextLength = contextLength
	out.Quantizations = make([]registry.Quantization, len(e.Quantizations))
	for i, q := range e.Quantizations {
		pinned, qErr := p.pinQuantization(ctx, info, q)
		if qErr != nil {
			return e, fmt.Errorf("quantization %s: %w", q.ID, qErr)
		}
		out.Quantizations[i] = pinned
	}
	out.Parameters = parametersOf(info, cfg, out.Quantizations)
	if out.Parameters <= 0 {
		return e, errors.New("no parameter count: Hugging Face lists none, and config.json names no dtype to work it out from")
	}
	// The memory minimums read the architecture and context just filled.
	for i, q := range out.Quantizations {
		q.MinGPUMemoryGiB = registry.MinGPUMemoryGiB(out, q)
		q.MinMemoryGiB = registry.MinMemoryGiB(out, q)
		out.Quantizations[i] = q
	}
	return out, nil
}

func (p *pinner) pinQuantization(ctx context.Context, entry *modelInfo, q registry.Quantization) (registry.Quantization, error) {
	files := entry.Siblings
	if q.Repo != "" {
		info, err := p.hub.info(ctx, q.Repo)
		if err != nil {
			return q, err
		}
		if info.gated() && p.hub.token == "" {
			return q, fmt.Errorf("%s: %w", q.Repo, errNeedsToken)
		}
		q.Revision = info.SHA
		files = info.Siblings
	}
	if strings.HasPrefix(q.ID, "gguf-") {
		for _, f := range files {
			if f.Name == q.File {
				q.WeightBytes = f.size()
				return q, nil
			}
		}
		repo, revision := q.Repo, q.Revision
		if repo == "" {
			repo, revision = entry.ID, entry.SHA
		}
		return q, fmt.Errorf("%s isn't in %s at %s", q.File, repo, revision)
	}
	q.WeightBytes = weightBytes(files)
	if q.WeightBytes <= 0 {
		return q, errors.New("the repo has no .safetensors or pytorch_model .bin weights at its top level")
	}
	return q, nil
}

// weightBytes is the size of the weights an engine loads: every top-level
// .safetensors file except Mistral's consolidated copies and second dtype
// variants, or the pytorch_model .bin files when there are no safetensors.
func weightBytes(files []sibling) int64 {
	var safetensors, bin int64
	for _, f := range files {
		name := f.Name
		switch {
		case strings.Contains(name, "/"):
		case strings.HasSuffix(name, ".safetensors"):
			if !strings.HasPrefix(name, "consolidated") && !variantShard.MatchString(name) {
				safetensors += f.size()
			}
		case strings.HasPrefix(name, "pytorch_model") && strings.HasSuffix(name, ".bin"):
			bin += f.size()
		}
	}
	if safetensors > 0 {
		return safetensors
	}
	return bin
}

// parametersOf is the safetensors parameter count Hugging Face lists, or,
// for a repo with only .bin weights, their size over the size of one weight
// in the dtype config.json names.
func parametersOf(info *modelInfo, cfg map[string]any, qs []registry.Quantization) int64 {
	if info.Safetensors != nil && info.Safetensors.Total > 0 {
		return info.Safetensors.Total
	}
	dtype := asString(cfg["torch_dtype"])
	if dtype == "" {
		dtype = asString(cfg["dtype"])
	}
	size := map[string]int64{"float32": 4, "float16": 2, "bfloat16": 2}[dtype]
	if size == 0 {
		return 0
	}
	for _, q := range qs {
		if q.Repo == "" && !strings.HasPrefix(q.ID, "gguf-") {
			return q.WeightBytes / size
		}
	}
	return 0
}

// architectureOf reads the facts the memory math needs from config.json,
// and the context length. Multimodal configs keep the language model's
// facts under text_config, and Whisper's decoder is what keeps a KV cache.
func architectureOf(cfg map[string]any) (registry.Architecture, int, error) {
	var a registry.Architecture
	if names, ok := cfg["architectures"].([]any); ok && len(names) > 0 {
		a.Name = asString(names[0])
	}
	if a.Name == "" {
		return a, 0, errors.New("it names no architecture")
	}

	c := lookup{cfg}
	if text, ok := cfg["text_config"].(map[string]any); ok {
		c = lookup{text, cfg}
	}
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
		return a, 0, errors.New("it gives no layer count")
	case a.AttentionHeads <= 0:
		return a, 0, errors.New("it gives no attention head count")
	case a.HeadDim <= 0:
		return a, 0, errors.New("it gives no head_dim or hidden_size")
	case contextLength <= 0:
		return a, 0, errors.New("it gives no context length")
	}
	return a, contextLength, nil
}

// lookup reads keys from the first map that has them.
type lookup []map[string]any

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
