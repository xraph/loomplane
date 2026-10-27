package registry_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/registry"
)

// launchFamilies are the seventeen family files of the launch registry.
var launchFamilies = []string{
	"bge", "deepseek", "e5", "gemma", "glm", "gpt-oss", "granite", "gte", "jina",
	"llama", "mistral", "nomic", "olmo", "phi", "qwen", "rerankers", "whisper",
}

// launchSize is the launch registry's 155 entries less
// meta-llama/Llama-3.2-11B-Vision-Instruct, whose Mllama architecture vLLM
// 0.31.0 no longer serves.
const launchSize = 154

// coreIDs is the qualification core set.
var coreIDs = []string{
	"meta-llama/Llama-3.2-3B-Instruct",
	"meta-llama/Llama-3.1-8B-Instruct",
	"meta-llama/Llama-3.3-70B-Instruct",
	"Qwen/Qwen2.5-7B-Instruct",
	"Qwen/Qwen2.5-Coder-7B-Instruct",
	"Qwen/Qwen2.5-VL-7B-Instruct",
	"Qwen/Qwen3-8B",
	"Qwen/Qwen3-30B-A3B-Instruct-2507",
	"Qwen/Qwen3-Embedding-0.6B",
	"mistralai/Mistral-7B-Instruct-v0.3",
	"mistralai/Mistral-Small-3.2-24B-Instruct-2506",
	"google/gemma-3-12b-it",
	"deepseek-ai/DeepSeek-R1-Distill-Qwen-7B",
	"microsoft/Phi-4-mini-instruct",
	"openai/gpt-oss-20b",
	"openai/gpt-oss-120b",
	"BAAI/bge-small-en-v1.5",
	"BAAI/bge-m3",
	"nomic-ai/nomic-embed-text-v1.5",
	"BAAI/bge-reranker-v2-m3",
	"openai/whisper-large-v3-turbo",
}

// nonCommercial are the entries whose license rules out production use.
var nonCommercial = []string{
	"Qwen/Qwen2.5-3B-Instruct",
	"Qwen/Qwen2.5-VL-3B-Instruct",
	"mistralai/Ministral-8B-Instruct-2410",
	"mistralai/Mistral-Large-Instruct-2411",
	"mistralai/Codestral-22B-v0.1",
}

// testedKinds are the GPU kinds the qualification job runs on.
var testedKinds = []string{"nvidia-rtx-4000-ada", "nvidia-l40s", "nvidia-h100-80gb"}

// fp32Checkpoints ship their weights in float32, which vLLM loads as 16-bit,
// so their bf16 files are twice the usual size.
var fp32Checkpoints = map[string]bool{
	"BAAI/bge-m3":                         true,
	"Alibaba-NLP/gte-Qwen2-1.5B-instruct": true,
	"Alibaba-NLP/gte-Qwen2-7B-instruct":   true,
	"openai/whisper-tiny":                 true,
	"openai/whisper-base":                 true,
	"openai/whisper-small":                true,
	"openai/whisper-small.en":             true,
	"openai/whisper-medium":               true,
}

// unpinnedEntries reads unpinned/, where entries wait for registry-pin. In
// the launch registry they're the gated ones: pinning them needs a Hugging
// Face token whose account accepted their licenses.
func unpinnedEntries(t *testing.T) []registry.Entry {
	t.Helper()
	paths, err := filepath.Glob("unpinned/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	out := make([]registry.Entry, 0, launchSize)
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		var f registry.File
		if err := dec.Decode(&f); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if want := strings.TrimSuffix(filepath.Base(path), ".yaml"); f.Family != want {
			t.Fatalf("%s: family %q, want %q", path, f.Family, want)
		}
		for _, e := range f.Entries {
			e.Family = f.Family
			out = append(out, e)
		}
	}
	return out
}

func TestLaunchRegistrySize(t *testing.T) {
	pinned := registry.Default().Entries()
	if len(pinned) < 100 {
		t.Errorf("the registry holds %d entries, want at least 100", len(pinned))
	}
	unpinned := unpinnedEntries(t)
	if got := len(pinned) + len(unpinned); got != launchSize {
		t.Errorf("%d pinned and %d unpinned entries make %d, want %d", len(pinned), len(unpinned), got, launchSize)
	}
	var families []string
	for _, e := range append(pinned, unpinned...) {
		if !slices.Contains(families, e.Family) {
			families = append(families, e.Family)
		}
	}
	slices.Sort(families)
	if !slices.Equal(families, launchFamilies) {
		t.Errorf("families = %v, want %v", families, launchFamilies)
	}
}

func TestEveryEntryValidates(t *testing.T) {
	for _, e := range registry.Default().Entries() {
		if err := registry.Validate(e); err != nil {
			t.Error(err)
		}
	}
}

// TestUnpinnedEntriesAreReadyToPin checks the hand-written half of each
// unpinned entry, so registry-pin -write with a token only has to measure.
func TestUnpinnedEntriesAreReadyToPin(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	r := registry.Default()
	for _, e := range unpinnedEntries(t) {
		if _, dup := r.Entry(e.ID); dup {
			t.Errorf("%s: in unpinned/ and in data/", e.ID)
		}
		if e.Revision != "" || !e.Gated {
			t.Errorf("%s: revision %q, gated %v; an unpinned launch entry is a gated one waiting for a token", e.ID, e.Revision, e.Gated)
		}
		e.Revision, e.Parameters, e.ContextLength = sha, 1e12, 8192
		e.Architecture = registry.Architecture{Name: "Measured", Layers: 1, AttentionHeads: 1, KVHeads: 1, HeadDim: 64}
		for i := range e.Quantizations {
			e.Quantizations[i].WeightBytes = 1
			if e.Quantizations[i].Repo != "" {
				e.Quantizations[i].Revision = sha
			}
		}
		if err := registry.Validate(e); err != nil {
			t.Error(err)
		}
	}
}

func TestLicensesMatchTheirTags(t *testing.T) {
	var gotNonCommercial []string
	check := func(e registry.Entry) {
		gatedLicense := strings.HasPrefix(e.License.ID, "llama") || e.License.ID == "gemma"
		if e.Gated != gatedLicense {
			t.Errorf("%s: gated %v with license %s; Llama and Gemma repos are the gated ones", e.ID, e.Gated, e.License.ID)
		}
		if !e.License.Commercial {
			gotNonCommercial = append(gotNonCommercial, e.ID)
		}
	}
	for _, e := range registry.Default().Entries() {
		check(e)
	}
	for _, e := range unpinnedEntries(t) {
		check(e)
	}
	slices.Sort(gotNonCommercial)
	want := slices.Sorted(slices.Values(nonCommercial))
	if !slices.Equal(gotNonCommercial, want) {
		t.Errorf("non-commercial entries = %v, want %v", gotNonCommercial, want)
	}
}

func TestEngineArgsAreKnownFlags(t *testing.T) {
	tools, reasoning := engines.ToolParsers(), engines.ReasoningParsers()
	for _, e := range append(registry.Default().Entries(), unpinnedEntries(t)...) {
		for _, q := range e.Quantizations {
			for _, arg := range q.Args {
				flag, _, _ := strings.Cut(arg, "=")
				if !engines.KnownFlag(q.Engine, flag) {
					t.Errorf("%s %s: %s doesn't take %s", e.ID, q.ID, q.Engine, flag)
				}
			}
		}
		if e.ToolParser != "" && !slices.Contains(tools, e.ToolParser) {
			t.Errorf("%s: vLLM has no tool parser %q", e.ID, e.ToolParser)
		}
		if e.ReasoningParser != "" && !slices.Contains(reasoning, e.ReasoningParser) {
			t.Errorf("%s: vLLM has no reasoning parser %q", e.ID, e.ReasoningParser)
		}
	}
}

func TestArchitecturesAreServable(t *testing.T) {
	for _, e := range registry.Default().Entries() {
		for _, q := range e.Quantizations {
			if strings.HasPrefix(q.ID, "gguf-") {
				continue // llama.cpp reads the architecture from the GGUF file
			}
			if !engines.SupportsArchitecture(q.Engine, e.Architecture.Name) {
				t.Errorf("%s %s: %s doesn't serve %s", e.ID, q.ID, q.Engine, e.Architecture.Name)
			}
		}
	}
}

func TestMemoryMathIsCurrent(t *testing.T) {
	for _, e := range registry.Default().Entries() {
		for _, q := range e.Quantizations {
			if want := registry.MinGPUMemoryGiB(e, q); q.MinGPUMemoryGiB != want {
				t.Errorf("%s %s: min_gpu_memory_gib %d, the memory math says %d; run registry-pin -write", e.ID, q.ID, q.MinGPUMemoryGiB, want)
			}
			if want := registry.MinMemoryGiB(e, q); q.MinMemoryGiB != want {
				t.Errorf("%s %s: min_memory_gib %d, the memory math says %d; run registry-pin -write", e.ID, q.ID, q.MinMemoryGiB, want)
			}
		}
	}
}

// TestWeightBytesArePlausible compares each quantization's weights with
// two bytes per parameter. A quantization that counted the wrong files, or
// none, falls outside its range.
func TestWeightBytesArePlausible(t *testing.T) {
	ranges := map[string][2]float64{
		"bf16":     {0.85, 1.25},
		"fp8":      {0.5, 0.7}, // 1 byte a weight, plus embeddings left in 16-bit
		"awq-int4": {0.25, 0.45},
		"mxfp4":    {0.25, 0.45},
		"native":   {0.85, 2.3}, // TEI loads float32 checkpoints as they are
		// Small models keep their large embedding tables near 8 bits, which
		// lifts Qwen2.5 0.5B to 0.5 at Q4_K_M and 0.68 at Q8_0.
		"gguf-q4_0":   {0.25, 0.55},
		"gguf-q4_k_m": {0.25, 0.55},
		"gguf-mxfp4":  {0.25, 0.45},
		"gguf-q8_0":   {0.5, 0.7},
	}
	for _, e := range registry.Default().Entries() {
		for _, q := range e.Quantizations {
			bounds, ok := ranges[q.ID]
			if !ok {
				t.Errorf("%s: no range for quantization %s", e.ID, q.ID)
				continue
			}
			if q.ID == "bf16" && fp32Checkpoints[e.ID] {
				bounds = [2]float64{1.85, 2.3}
			}
			ratio := float64(q.WeightBytes) / (float64(e.Parameters) * 2)
			if ratio < bounds[0] || ratio > bounds[1] {
				t.Errorf("%s %s: weight_bytes %d is %.2f x parameters*2, want %.2f to %.2f", e.ID, q.ID, q.WeightBytes, ratio, bounds[0], bounds[1])
			}
		}
	}
}

func TestCoreSet(t *testing.T) {
	var got []string
	for _, e := range unpinnedEntries(t) {
		if e.Core {
			got = append(got, e.ID)
		}
	}
	for _, e := range registry.Default().Entries() {
		if !e.Core {
			continue
		}
		got = append(got, e.ID)
		if !fitsATestedKind(e) {
			t.Errorf("%s: core, but no quantization fits %v or TEI on CPU", e.ID, testedKinds)
		}
	}
	slices.Sort(got)
	if want := slices.Sorted(slices.Values(coreIDs)); !slices.Equal(got, want) {
		t.Errorf("core = %v, want %v", got, want)
	}
}

func fitsATestedKind(e registry.Entry) bool {
	for _, q := range e.Quantizations {
		if q.Engine == registry.EngineTEI {
			if _, _, ok := registry.CPUFits(e, q); ok {
				return true
			}
			continue
		}
		for _, kind := range testedKinds {
			if _, _, ok := registry.Smallest(e, q, kind, 8); ok {
				return true
			}
		}
	}
	return false
}

func TestNoTrustRemoteCodeOutsideTheAllowlist(t *testing.T) {
	allowed := []string{"microsoft/Phi-3.5-vision-instruct"}
	for _, e := range append(registry.Default().Entries(), unpinnedEntries(t)...) {
		if e.TrustRemoteCode && !slices.Contains(allowed, e.ID) {
			t.Errorf("%s: trust_remote_code outside the reviewed list", e.ID)
		}
	}
}

func TestEmbeddingsHaveDimensions(t *testing.T) {
	for _, e := range append(registry.Default().Entries(), unpinnedEntries(t)...) {
		if !slices.Contains(e.Tasks, registry.TaskEmbedding) {
			continue
		}
		if e.Embedding == nil {
			t.Errorf("%s: an embedding entry without an embedding block", e.ID)
			continue
		}
		if d := e.Embedding.Dimensions; d < 64 || d > 8192 {
			t.Errorf("%s: dimensions %d, want 64 to 8192", e.ID, d)
		}
		if e.Embedding.MaxInputTokens <= 0 {
			t.Errorf("%s: max_input_tokens %d, want more than 0", e.ID, e.Embedding.MaxInputTokens)
		}
		// Unpinned entries have no context_length yet.
		if e.ContextLength > 0 && e.Embedding.MaxInputTokens > e.ContextLength {
			t.Errorf("%s: max_input_tokens %d is more than context_length %d", e.ID, e.Embedding.MaxInputTokens, e.ContextLength)
		}
	}
}
