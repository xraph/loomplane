package hfhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/registry"
)

// qwenModel is the Qwen fixture as Model reads it, and its config.json.
func qwenModel(t *testing.T) (*ModelInfo, []byte) {
	t.Helper()
	fixture, err := os.ReadFile("testdata/qwen2.5-7b-instruct.json")
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile("testdata/qwen2.5-7b-instruct-config.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()
	info, err := New(srv.URL, srv.Client()).Model(context.Background(), "Qwen/Qwen2.5-7B-Instruct", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return info, config
}

func TestBuildMakesAnUntestedEntry(t *testing.T) {
	info, config := qwenModel(t)
	e, err := Build(info, config)
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != engines.CustomEntryID("Qwen/Qwen2.5-7B-Instruct", qwenSHA) || e.ID != "hf:Qwen/Qwen2.5-7B-Instruct@a09a35458c70" {
		t.Errorf("ID %q", e.ID)
	}
	if e.Revision != qwenSHA || e.Title != "Qwen/Qwen2.5-7B-Instruct" || e.Publisher != "Qwen" || e.Family != CustomFamily {
		t.Errorf("revision %q title %q publisher %q family %q", e.Revision, e.Title, e.Publisher, e.Family)
	}
	if e.TrustRemoteCode || e.ToolParser != "" || e.ReasoningParser != "" || e.Gated {
		t.Errorf("trust %v tool %q reasoning %q gated %v", e.TrustRemoteCode, e.ToolParser, e.ReasoningParser, e.Gated)
	}
	if len(e.Tasks) != 1 || e.Tasks[0] != registry.TaskChat || e.ContextLength != 32768 || e.Parameters != 7615616512 {
		t.Errorf("tasks %v context %d parameters %d", e.Tasks, e.ContextLength, e.Parameters)
	}
	if e.License != (registry.License{ID: "apache-2.0", Commercial: true}) {
		t.Errorf("license %+v", e.License)
	}
	// The registry's own facts for this entry, from the same config.
	want := registry.Architecture{Name: "Qwen2ForCausalLM", Layers: 28, AttentionHeads: 28, KVHeads: 4, HeadDim: 128}
	if e.Architecture != want {
		t.Errorf("architecture %+v", e.Architecture)
	}
	if len(e.Quantizations) != 1 {
		t.Fatalf("quantizations %+v", e.Quantizations)
	}
	q := e.Quantizations[0]
	if q.ID != "bf16" || q.Engine != registry.EngineVLLM || q.WeightBytes != 15231271888 || q.Repo != "" || q.MinGPUMemoryGiB != 19 {
		t.Errorf("quantization %+v", q)
	}
	// FitOn can't call a pasted model tested or estimated: no run will ever check it.
	if fit := registry.Default().FitOn(e, q, "nvidia-l40s", 1, engines.Current()); fit.Status != registry.StatusUntested {
		t.Errorf("fit on an L40S: %s", fit.Status)
	}
}

func TestBuildNeverTrustsRepositoryCode(t *testing.T) {
	info, _ := qwenModel(t)
	config := []byte(`{"architectures":["FooForCausalLM"],"auto_map":{"AutoModelForCausalLM":"modeling_foo.FooForCausalLM"},
		"num_hidden_layers":4,"num_attention_heads":8,"hidden_size":512,"max_position_embeddings":4096}`)
	_, err := Build(info, config)
	if !errors.Is(err, ErrRefused) || err.Error() != "Qwen/Qwen2.5-7B-Instruct needs code from its repository to run, and Loomplane doesn't run repository code for pasted models." {
		t.Errorf("auto_map: %v", err)
	}
	config = []byte(`{"architectures":["FooForCausalLM"],"num_hidden_layers":4,"num_attention_heads":8,"hidden_size":512,"max_position_embeddings":4096}`)
	if _, err = Build(info, config); !errors.Is(err, ErrRefused) || err.Error() != "No engine Loomplane ships can serve FooForCausalLM." {
		t.Errorf("unknown architecture: %v", err)
	}
	// A natively supported architecture with an auto_map still builds,
	// and never asks for the repo's code.
	config = []byte(`{"architectures":["Qwen2ForCausalLM"],"auto_map":{"AutoModel":"x.Y"},"num_hidden_layers":28,"num_attention_heads":28,
		"num_key_value_heads":4,"hidden_size":3584,"max_position_embeddings":32768}`)
	e, err := Build(info, config)
	if err != nil || e.TrustRemoteCode {
		t.Errorf("native architecture with auto_map: trust %v, %v", e.TrustRemoteCode, err)
	}
}

func TestBuildRefusesOtherTasks(t *testing.T) {
	info, config := qwenModel(t)
	for tag, want := range map[string]string{
		"text-to-image":       "Loomplane serves chat, vision, embedding, reranking and speech models. Qwen/Qwen2.5-7B-Instruct is text-to-image.",
		"text-classification": "Loomplane serves chat, vision, embedding, reranking and speech models. Qwen/Qwen2.5-7B-Instruct is text-classification.",
		"":                    "Loomplane serves chat, vision, embedding, reranking and speech models. Qwen/Qwen2.5-7B-Instruct doesn't say on Hugging Face what it does.",
	} {
		info.PipelineTag = tag
		if _, err := Build(info, config); !errors.Is(err, ErrRefused) || err.Error() != want {
			t.Errorf("%q: %v", tag, err)
		}
	}
}

func TestBuildReadsTasks(t *testing.T) {
	info, _ := qwenModel(t)
	for _, tt := range []struct {
		tag  string
		tags []string
		want registry.Task
	}{
		{"text-generation", nil, registry.TaskChat},
		{"image-text-to-text", nil, registry.TaskVision},
		{"feature-extraction", nil, registry.TaskEmbedding},
		{"sentence-similarity", nil, registry.TaskEmbedding},
		{"automatic-speech-recognition", nil, registry.TaskSpeech},
		{"text-classification", []string{"Reranker"}, registry.TaskRerank},
		{"text-classification", []string{"cross-encoder"}, registry.TaskRerank},
	} {
		info.PipelineTag, info.Tags = tt.tag, tt.tags
		got, err := tasksOf(info)
		if err != nil || len(got) != 1 || got[0] != tt.want {
			t.Errorf("%s %v: %v, %v", tt.tag, tt.tags, got, err)
		}
	}
}

// An embedding model TEI knows gets "native" on TEI first, and vLLM too.
func TestBuildAnEmbeddingModel(t *testing.T) {
	info := &ModelInfo{
		ID: "acme/embed-small", SHA: qwenSHA, PipelineTag: "feature-extraction", License: "mit",
		Files: []FileInfo{{Name: "config.json", Size: 600}, {Name: "model.safetensors", Size: 133_000_000}, {Name: "onnx/model.onnx", Size: 133_000_000}},
	}
	config := []byte(`{"architectures":["BertModel"],"num_hidden_layers":12,"num_attention_heads":12,"hidden_size":384,"max_position_embeddings":512,"torch_dtype":"float32"}`)
	e, err := Build(info, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Quantizations) != 2 || e.Quantizations[0].ID != "native" || e.Quantizations[0].Engine != registry.EngineTEI ||
		e.Quantizations[1].ID != "bf16" || e.Quantizations[1].WeightBytes != 133_000_000 {
		t.Errorf("quantizations %+v", e.Quantizations)
	}
	if e.Embedding == nil || e.Embedding.Dimensions != 384 || e.Embedding.MaxInputTokens != 512 {
		t.Errorf("embedding %+v", e.Embedding)
	}
	// No safetensors count on the Hub: the size over four bytes a float32.
	if e.Parameters != 33_250_000 || e.License != (registry.License{ID: "mit", Commercial: true}) {
		t.Errorf("parameters %d license %+v", e.Parameters, e.License)
	}
	// Over 1.5 GiB, TEI on CPU isn't offered.
	info.Files[1].Size = 2 << 30
	if e, err = Build(info, config); err != nil || len(e.Quantizations) != 1 || e.Quantizations[0].Engine != registry.EngineVLLM {
		t.Errorf("large embedding model: %+v, %v", e.Quantizations, err)
	}
}

func TestBuildFindsGGUFFiles(t *testing.T) {
	info, config := qwenModel(t)
	info.License = "other"
	info.Files = []FileInfo{
		{Name: "README.md", Size: 100},
		{Name: "Qwen2.5-7B-Instruct-Q4_K_M.gguf", Size: 4683074240},
		{Name: "Qwen2.5-7B-Instruct-Q8_0.gguf", Size: 8098525888},
		{Name: "Qwen2.5-7B-Instruct-Q5_K_M.gguf", Size: 5444831936},
		{Name: "big/Qwen2.5-7B-Instruct-Q8_0.gguf", Size: 8098525888},
		{Name: "Qwen2.5-7B-Instruct-f16-00001-of-00002.gguf", Size: 8_000_000_000},
	}
	e, err := Build(info, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Quantizations) != 2 {
		t.Fatalf("quantizations %+v", e.Quantizations)
	}
	q8, q4 := e.Quantizations[0], e.Quantizations[1]
	if q8.ID != "gguf-q8_0" || q8.File != "Qwen2.5-7B-Instruct-Q8_0.gguf" || q8.WeightBytes != 8098525888 || q8.Engine != registry.EngineLlamaCPP {
		t.Errorf("q8 %+v", q8)
	}
	if q4.ID != "gguf-q4_k_m" || q4.File != "Qwen2.5-7B-Instruct-Q4_K_M.gguf" || q4.MinMemoryGiB == 0 {
		t.Errorf("q4 %+v", q4)
	}
	// A license the registry doesn't know is never counted as commercial.
	if e.License != (registry.License{ID: "other"}) {
		t.Errorf("license %+v", e.License)
	}
}

// A GGUF name that isn't plain letters, digits, dots, underscores and
// hyphens ending ".gguf" is skipped, so nothing that needs escaping ever
// reaches --model-url, and a repo with one odd name still builds.
func TestBuildSkipsGGUFNamesItWontUse(t *testing.T) {
	info, config := qwenModel(t)
	info.Params = 0
	info.Files = []FileInfo{
		{Name: "Qwen2.5-7B-Instruct-Q8_0.GGUF", Size: 8098525888},
		{Name: "Qwen 2.5 7B Q8_0.gguf", Size: 8098525888},
		{Name: "x?y-q8_0.gguf", Size: 8098525888},
		{Name: "Qwen2.5-7B-Instruct-Q4_K_M.gguf", Size: 4683074240},
	}
	e, err := Build(info, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Quantizations) != 1 || e.Quantizations[0].File != "Qwen2.5-7B-Instruct-Q4_K_M.gguf" {
		t.Errorf("quantizations %+v", e.Quantizations)
	}
	// About 0.56 bytes a weight in Q4_K_M, not two.
	if e.Parameters != 8362632571 {
		t.Errorf("parameters %d", e.Parameters)
	}

	for name, want := range map[string]bool{
		"Qwen2.5-7B-Instruct-Q4_K_M.gguf": true, "m.gguf": true,
		"m.GGUF": false, "a/m.gguf": false, "m .gguf": false, "m%2F.gguf": false, "..gguf": false, "m.gguf\n": false, "": false,
	} {
		if got := GGUFName(name); got != want {
			t.Errorf("GGUFName(%q) = %v", name, got)
		}
	}
}

func TestBuildCountsGGUFParameters(t *testing.T) {
	info, config := qwenModel(t)
	info.Params = 0
	info.Files = []FileInfo{{Name: "Qwen2.5-7B-Instruct-Q8_0.gguf", Size: 8098525888}}
	e, err := Build(info, config)
	if err != nil {
		t.Fatal(err)
	}
	// About 1.06 bytes a weight in Q8_0.
	if e.Parameters != 7640118762 {
		t.Errorf("parameters %d", e.Parameters)
	}
}

func TestBuildNeedsWeights(t *testing.T) {
	info, config := qwenModel(t)
	info.Files = []FileInfo{{Name: "config.json", Size: 663}}
	if _, err := Build(info, config); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "has no .safetensors or pytorch_model .bin weights") {
		t.Errorf("no weights: %v", err)
	}
	info, _ = qwenModel(t)
	if _, err := Build(info, []byte("not json")); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "isn't JSON") {
		t.Errorf("bad config: %v", err)
	}
	info.SHA = "main"
	if _, err := Build(info, config); err == nil || errors.Is(err, ErrRefused) {
		t.Errorf("a branch name for a revision: %v", err)
	}
}

func TestWeightBytes(t *testing.T) {
	files := []FileInfo{
		{Name: "model-00001-of-00002.safetensors", Size: 10},
		{Name: "model-00002-of-00002.safetensors", Size: 5},
		{Name: "consolidated.safetensors", Size: 15},
		{Name: "model.fp32-00001-of-00002.safetensors", Size: 30},
		{Name: "sub/model.safetensors", Size: 99},
		{Name: "pytorch_model.bin", Size: 40},
	}
	if got := WeightBytes(files); got != 15 {
		t.Errorf("safetensors: %d", got)
	}
	if got := WeightBytes(files[5:]); got != 40 {
		t.Errorf(".bin only: %d", got)
	}
}

func TestArchitectureOf(t *testing.T) {
	tests := []struct {
		name    string
		cfg     string
		want    registry.Architecture
		context int
	}{
		{
			name:    "whisper decoder",
			cfg:     `{"architectures":["WhisperForConditionalGeneration"],"decoder_layers":4,"decoder_attention_heads":20,"d_model":1280,"max_target_positions":448,"encoder_layers":32}`,
			want:    registry.Architecture{Name: "WhisperForConditionalGeneration", Layers: 4, AttentionHeads: 20, KVHeads: 20, HeadDim: 64},
			context: 448,
		},
		{
			name:    "text_config first",
			cfg:     `{"architectures":["Mistral3ForConditionalGeneration"],"text_config":{"num_hidden_layers":40,"num_attention_heads":32,"num_key_value_heads":8,"head_dim":128,"hidden_size":5120,"max_position_embeddings":131072},"vision_config":{"num_hidden_layers":24}}`,
			want:    registry.Architecture{Name: "Mistral3ForConditionalGeneration", Layers: 40, AttentionHeads: 32, KVHeads: 8, HeadDim: 128},
			context: 131072,
		},
		{
			name:    "hybrid mamba",
			cfg:     `{"architectures":["GraniteMoeHybridForCausalLM"],"num_hidden_layers":4,"num_attention_heads":32,"num_key_value_heads":8,"hidden_size":4096,"max_position_embeddings":131072,"layer_types":["mamba","attention","mamba","mamba"]}`,
			want:    registry.Architecture{Name: "GraniteMoeHybridForCausalLM", Layers: 4, AttentionLayers: 1, AttentionHeads: 32, KVHeads: 8, HeadDim: 128},
			context: 131072,
		},
		{
			name:    "sliding window counts as attention",
			cfg:     `{"architectures":["Olmo3ForCausalLM"],"num_hidden_layers":4,"num_attention_heads":32,"hidden_size":4096,"max_position_embeddings":65536,"layer_types":["sliding_attention","sliding_attention","sliding_attention","full_attention"]}`,
			want:    registry.Architecture{Name: "Olmo3ForCausalLM", Layers: 4, AttentionHeads: 32, KVHeads: 32, HeadDim: 128},
			context: 65536,
		},
		{
			name:    "latent attention",
			cfg:     `{"architectures":["DeepseekV2ForCausalLM"],"num_hidden_layers":27,"num_attention_heads":16,"num_key_value_heads":16,"hidden_size":2048,"kv_lora_rank":512,"qk_rope_head_dim":64,"max_position_embeddings":163840}`,
			want:    registry.Architecture{Name: "DeepseekV2ForCausalLM", Layers: 27, AttentionHeads: 16, KVHeads: 16, HeadDim: 128, KVLoraRank: 512, RopeHeadDim: 64},
			context: 163840,
		},
		{
			name:    "GPT-2 style names",
			cfg:     `{"architectures":["NomicBertModel"],"n_layer":12,"n_head":12,"n_embd":768,"n_positions":8192}`,
			want:    registry.Architecture{Name: "NomicBertModel", Layers: 12, AttentionHeads: 12, KVHeads: 12, HeadDim: 64},
			context: 8192,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg map[string]any
			if err := json.Unmarshal([]byte(tt.cfg), &cfg); err != nil {
				t.Fatal(err)
			}
			got, ctx, err := ArchitectureOf(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want || ctx != tt.context {
				t.Errorf("got %+v, %d; want %+v, %d", got, ctx, tt.want, tt.context)
			}
		})
	}
	if _, _, err := ArchitectureOf(map[string]any{"num_hidden_layers": 2.0}); err == nil {
		t.Error("a config with no architectures was accepted")
	}
}
