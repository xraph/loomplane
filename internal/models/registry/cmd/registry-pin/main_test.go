package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/models/registry"
)

const (
	shaSmall = "1111111111111111111111111111111111111111"
	shaGGUF  = "2222222222222222222222222222222222222222"
	shaGated = "3333333333333333333333333333333333333333"
)

// fakeHub serves listings and configs the way huggingface.co does, and
// refuses the gated repo's files to a request without a token.
func fakeHub(t *testing.T) *httptest.Server {
	t.Helper()
	listings := map[string]any{
		"acme/Small-1B-Instruct": map[string]any{
			"id": "acme/Small-1B-Instruct", "sha": shaSmall, "gated": false,
			"cardData":    map[string]any{"license": "apache-2.0"},
			"safetensors": map[string]any{"total": 1_000_000_000},
			"siblings": []map[string]any{
				{"rfilename": "config.json", "size": 700},
				{"rfilename": "model-00001-of-00002.safetensors", "size": 1_200_000_000},
				{"rfilename": "model-00002-of-00002.safetensors", "size": 800_000_000},
				{"rfilename": "consolidated.safetensors", "size": 2_000_000_000},
				{"rfilename": "model.fp32-00001-of-00001.safetensors", "size": 4_000_000_000},
				{"rfilename": "original/model.safetensors", "size": 2_000_000_000},
			},
		},
		"acme/Small-1B-Instruct-GGUF": map[string]any{
			"id": "acme/Small-1B-Instruct-GGUF", "sha": shaGGUF, "gated": false,
			"siblings": []map[string]any{{"rfilename": "Small-1B-Instruct-Q4_K_M.gguf", "size": 600_000_000}},
		},
		"acme/Gated-8B": map[string]any{
			"id": "acme/Gated-8B", "sha": shaGated, "gated": "manual",
			"cardData": map[string]any{"license": "llama3.1"},
			"siblings": []map[string]any{{"rfilename": "model.safetensors", "size": 16_000_000_000}},
		},
	}
	configs := map[string]any{
		"/acme/Small-1B-Instruct/resolve/" + shaSmall + "/config.json": map[string]any{
			"architectures": []string{"LlamaForCausalLM"}, "num_hidden_layers": 16,
			"num_attention_heads": 32, "num_key_value_heads": 8, "hidden_size": 2048,
			"max_position_embeddings": 131072,
		},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if repo, ok := strings.CutPrefix(r.URL.Path, "/api/models/"); ok {
			if l, found := listings[repo]; found {
				_ = json.NewEncoder(w).Encode(l)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/acme/Gated-8B/") && r.Header.Get("Authorization") == "" {
			http.Error(w, "gated", http.StatusUnauthorized)
			return
		}
		if c, found := configs[r.URL.Path]; found {
			_ = json.NewEncoder(w).Encode(c)
			return
		}
		http.NotFound(w, r)
	}))
}

const smallEntry = `family: acme
entries:
  - id: acme/Small-1B-Instruct
    title: Small 1B Instruct
    publisher: Acme
    summary: A small chat model.
    license:
      id: apache-2.0
      commercial: true
    tasks: [chat]
    quantizations:
      - id: bf16
        engine: vllm
      - id: gguf-q4_k_m
        engine: llamacpp
        repo: acme/Small-1B-Instruct-GGUF
        file: Small-1B-Instruct-Q4_K_M.gguf
`

const gatedEntry = `family: gated
entries:
  - id: acme/Gated-8B
    title: Gated 8B
    publisher: Acme
    summary: A gated chat model.
    license:
      id: llama3.1
      commercial: true
    gated: true
    tasks: [chat]
    quantizations:
      - id: bf16
        engine: vllm
`

func setup(t *testing.T, unpinned map[string]string) options {
	t.Helper()
	root := t.TempDir()
	o := options{dataDir: filepath.Join(root, "data"), unpinnedDir: filepath.Join(root, "unpinned")}
	for _, dir := range []string{o.dataDir, o.unpinnedDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range unpinned {
		if err := os.WriteFile(filepath.Join(o.unpinnedDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return o
}

func TestWritePinsAndMovesEntries(t *testing.T) {
	srv := fakeHub(t)
	defer srv.Close()
	o := setup(t, map[string]string{"acme.yaml": smallEntry})
	o.hubURL = srv.URL

	var out bytes.Buffer
	if code, err := run(context.Background(), o, &out); err != nil || code != 1 {
		t.Fatalf("check before pinning: code %d, err %v, want 1\n%s", code, err, out.String())
	}
	if _, err := os.Stat(filepath.Join(o.dataDir, "acme.yaml")); !os.IsNotExist(err) {
		t.Fatal("a run without -write wrote data/acme.yaml")
	}

	o.write = true
	out.Reset()
	if code, err := run(context.Background(), o, &out); err != nil || code != 0 {
		t.Fatalf("-write: code %d, err %v\n%s", code, err, out.String())
	}
	if _, err := os.Stat(filepath.Join(o.unpinnedDir, "acme.yaml")); !os.IsNotExist(err) {
		t.Error("unpinned/acme.yaml is still there with no entries left in it")
	}
	r, err := registry.Load(os.DirFS(o.dataDir))
	if err != nil {
		t.Fatalf("the pinned file doesn't load: %v", err)
	}
	e, ok := r.Entry("acme/Small-1B-Instruct")
	if !ok {
		t.Fatal("acme/Small-1B-Instruct isn't in data/")
	}
	if e.Revision != shaSmall || e.Parameters != 1_000_000_000 || e.ContextLength != 131072 {
		t.Errorf("revision %s, parameters %d, context %d", e.Revision, e.Parameters, e.ContextLength)
	}
	want := registry.Architecture{Name: "LlamaForCausalLM", Layers: 16, AttentionHeads: 32, KVHeads: 8, HeadDim: 64}
	if e.Architecture != want {
		t.Errorf("architecture %+v, want %+v", e.Architecture, want)
	}
	bf16, _ := e.Quantization("bf16")
	if bf16.WeightBytes != 2_000_000_000 {
		t.Errorf("bf16 weight_bytes %d, want 2000000000: consolidated, fp32 and original/ files don't count", bf16.WeightBytes)
	}
	if bf16.MinGPUMemoryGiB == 0 || bf16.MinGPUMemoryGiB != registry.MinGPUMemoryGiB(e, bf16) {
		t.Errorf("bf16 min_gpu_memory_gib %d", bf16.MinGPUMemoryGiB)
	}
	gguf, _ := e.Quantization("gguf-q4_k_m")
	if gguf.Revision != shaGGUF || gguf.WeightBytes != 600_000_000 || gguf.MinMemoryGiB != registry.MinMemoryGiB(e, gguf) {
		t.Errorf("gguf %+v", gguf)
	}

	o.write = false
	out.Reset()
	if code, err := run(context.Background(), o, &out); err != nil || code != 0 {
		t.Fatalf("check after pinning: code %d, err %v, want 0\n%s", code, err, out.String())
	}
}

func TestGatedEntriesWaitForAToken(t *testing.T) {
	srv := fakeHub(t)
	defer srv.Close()
	o := setup(t, map[string]string{"gated.yaml": gatedEntry})
	o.hubURL = srv.URL
	o.write = true

	var out bytes.Buffer
	if code, err := run(context.Background(), o, &out); err != nil || code != 0 {
		t.Fatalf("code %d, err %v, want 0\n%s", code, err, out.String())
	}
	if !strings.Contains(out.String(), "acme/Gated-8B: gated, waiting for a Hugging Face token") {
		t.Errorf("output doesn't say the gated entry waits:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(o.unpinnedDir, "gated.yaml")); err != nil {
		t.Errorf("the gated entry left unpinned/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(o.dataDir, "gated.yaml")); !os.IsNotExist(err) {
		t.Error("the gated entry reached data/ without a token")
	}
}

func TestMissingGGUFFileCantPin(t *testing.T) {
	srv := fakeHub(t)
	defer srv.Close()
	o := setup(t, map[string]string{"acme.yaml": strings.Replace(smallEntry, "Q4_K_M.gguf", "Q5_K_M.gguf", 1)})
	o.hubURL = srv.URL
	o.write = true

	var out bytes.Buffer
	if code, err := run(context.Background(), o, &out); err != nil || code != 1 {
		t.Fatalf("code %d, err %v, want 1\n%s", code, err, out.String())
	}
	want := "Small-1B-Instruct-Q5_K_M.gguf isn't in acme/Small-1B-Instruct-GGUF at " + shaGGUF
	if !strings.Contains(out.String(), want) {
		t.Errorf("output doesn't say %q:\n%s", want, out.String())
	}
}

func TestLicenseTagMustMatch(t *testing.T) {
	srv := fakeHub(t)
	defer srv.Close()
	o := setup(t, map[string]string{"acme.yaml": strings.Replace(smallEntry, "id: apache-2.0", "id: mit", 1)})
	o.hubURL = srv.URL

	var out bytes.Buffer
	if code, err := run(context.Background(), o, &out); err != nil || code != 1 {
		t.Fatalf("code %d, err %v, want 1\n%s", code, err, out.String())
	}
	if !strings.Contains(out.String(), "the repo's license tag is apache-2.0, but the entry says mit") {
		t.Errorf("output:\n%s", out.String())
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
			got, ctx, err := architectureOf(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want || ctx != tt.context {
				t.Errorf("got %+v, %d; want %+v, %d", got, ctx, tt.want, tt.context)
			}
		})
	}
	if _, _, err := architectureOf(map[string]any{"num_hidden_layers": 2.0}); err == nil {
		t.Error("a config with no architectures was accepted")
	}
}

// refusingHub answers the way huggingface.co answers an anonymous client:
// 401 for a repo that doesn't exist and for a gated repo's files, told apart
// only by X-Error-Code.
func refusingHub(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/models/acme/Gated-8B":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "acme/Gated-8B", "sha": shaGated, "gated": false,
				"siblings": []map[string]any{{"rfilename": "model.safetensors", "size": 16_000_000_000}},
			})
		case strings.HasPrefix(r.URL.Path, "/acme/Gated-8B/"):
			w.Header().Set("X-Error-Code", "GatedRepo")
			http.Error(w, "Access to model acme/Gated-8B is restricted.", http.StatusUnauthorized)
		default:
			w.Header().Set("X-Error-Code", "RepoNotFound")
			http.Error(w, "Repository not found", http.StatusUnauthorized)
		}
	}))
}

func TestHubTellsAMissingRepoFromAGatedOne(t *testing.T) {
	srv := refusingHub(t)
	defer srv.Close()
	h := newHub(srv.URL, "", 0)

	_, err := h.info(context.Background(), "acme/Mistyped-8B")
	if !errors.Is(err, errRepoNotFound) || errors.Is(err, errNeedsToken) {
		t.Errorf("missing repo: %v, want errRepoNotFound only", err)
	}
	_, err = h.config(context.Background(), "acme/Gated-8B", shaGated)
	if !errors.Is(err, errNeedsToken) || errors.Is(err, errRepoNotFound) {
		t.Errorf("gated repo: %v, want errNeedsToken only", err)
	}
}

func TestMissingRepoCantPin(t *testing.T) {
	srv := refusingHub(t)
	defer srv.Close()
	mistyped := strings.ReplaceAll(gatedEntry, "acme/Gated-8B", "acme/Mistyped-8B")
	o := setup(t, map[string]string{"gated.yaml": gatedEntry, "mistyped.yaml": strings.Replace(mistyped, "family: gated", "family: mistyped", 1)})
	o.hubURL = srv.URL
	o.write = true

	var out bytes.Buffer
	if code, err := run(context.Background(), o, &out); err != nil || code != 1 {
		t.Fatalf("code %d, err %v, want 1\n%s", code, err, out.String())
	}
	got := out.String()
	_, cantPin, found := strings.Cut(got, "Can't pin")
	if !found || !strings.Contains(cantPin, "unpinned/mistyped.yaml acme/Mistyped-8B: ") || !strings.Contains(cantPin, errRepoNotFound.Error()) {
		t.Errorf("the missing repo isn't under Can't pin:\n%s", got)
	}
	if !strings.Contains(got, "acme/Gated-8B: gated, waiting for a Hugging Face token") {
		t.Errorf("the gated repo doesn't wait for a token:\n%s", got)
	}
	if strings.Contains(got, "acme/Mistyped-8B: gated") {
		t.Errorf("the missing repo reads as gated:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(o.unpinnedDir, "mistyped.yaml")); err != nil {
		t.Errorf("the missing repo's entry left unpinned/: %v", err)
	}
}
