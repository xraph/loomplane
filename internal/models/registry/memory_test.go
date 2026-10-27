package registry

import (
	"bytes"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/pkg/provider"
)

// fixtureEntry is a valid entry with one bf16 quantization on vLLM.
func fixtureEntry(weightBytes int64, arch Architecture, contextLength int, task Task) Entry {
	if arch.Name == "" {
		arch.Name = "LlamaForCausalLM"
	}
	e := Entry{
		ID:            "test/Model-8B",
		Title:         "Test Model 8B",
		Publisher:     "Test",
		Summary:       "A model for the memory math tests.",
		Revision:      "0123456789abcdef0123456789abcdef01234567",
		License:       License{ID: "apache-2.0", Commercial: true},
		Tasks:         []Task{task},
		Parameters:    8_000_000_000,
		ContextLength: contextLength,
		Architecture:  arch,
		Quantizations: []Quantization{{ID: "bf16", Engine: EngineVLLM, WeightBytes: weightBytes}},
	}
	if task == TaskEmbedding {
		e.Embedding = &Embedding{Dimensions: 1024, MaxInputTokens: contextLength}
	}
	return e
}

func llama8B() Entry {
	return fixtureEntry(16<<30, Architecture{Layers: 32, AttentionHeads: 32, KVHeads: 8, HeadDim: 128}, 131072, TaskChat)
}

func TestFixtureEntryValidates(t *testing.T) {
	if err := Validate(llama8B()); err != nil {
		t.Fatal(err)
	}
}

func TestEstimateWorkedExample(t *testing.T) {
	e := fixtureEntry(16<<30, Architecture{Layers: 32, AttentionHeads: 32, KVHeads: 8, HeadDim: 128}, 131072, TaskChat)
	q := e.Quantizations[0]
	need, _, ok := Estimate(e, q, "nvidia-l4", 1)
	if !ok || need.MaxModelLen != 22528 || math.Abs(need.PerGPUGiB-21.53) > 0.01 {
		t.Fatalf("l4: %+v %v", need, ok)
	}
	if _, reason, fits := Estimate(e, q, "nvidia-rtx-4000-ada", 1); fits || !strings.Contains(reason, "18.0 GiB usable") {
		t.Fatalf("rtx 4000 ada fits: %s", reason)
	}
	if got := MinGPUMemoryGiB(e, q); got != 22 {
		t.Fatalf("min gpu memory %d", got)
	}
	need, _, ok = Smallest(e, q, "nvidia-t4", 8)
	if !ok || need.GPUs != 2 {
		t.Fatalf("t4 needs two: %+v", need)
	}
}

func TestEstimateWorkedExampleNumbers(t *testing.T) {
	e := llama8B()
	q := e.Quantizations[0]
	s := shapeOf(e, q)
	if s.kvPerToken != 131072 {
		t.Errorf("kvpt = %v, want 131072", s.kvPerToken)
	}
	if o := s.overhead(1); math.Abs(o-2.78) > 1e-9 {
		t.Errorf("O = %v, want 2.78", o)
	}
	if n := s.vllmNeed(1, 4096); math.Abs(n-19.28) > 1e-9 {
		t.Errorf("need(1, 4096) = %v, want 19.28", n)
	}
	// Before rounding down to 1024, the served context is 23101 tokens.
	if most := math.Floor((GPUMemoryUtilization*24 - s.overhead(1) - s.weights) * gibBytes / s.kvPerToken); most != 23101 {
		t.Errorf("unrounded context = %v, want 23101", most)
	}
	need, reason, ok := Estimate(e, q, "nvidia-l4", 1)
	if !ok || reason != "" || need.GPUs != 1 || need.RAMGiB != 0 {
		t.Errorf("l4: %+v %q %v", need, reason, ok)
	}
	if want := 16 + 22528*131072.0/gibBytes + 2.78; math.Abs(need.PerGPUGiB-want) > 1e-9 {
		t.Errorf("per GPU = %v, want %v", need.PerGPUGiB, want)
	}

	need, reason, ok = Estimate(e, q, "nvidia-rtx-4000-ada", 1)
	want := "Needs 19.3 GiB per GPU at a 4096 token context; NVIDIA RTX 4000 Ada has 18.0 GiB usable."
	if ok || reason != want {
		t.Errorf("rtx 4000 ada reason = %q, want %q", reason, want)
	}
	if need.MaxModelLen != 4096 || math.Abs(need.PerGPUGiB-19.28) > 1e-9 {
		t.Errorf("a refused need holds the need at the least context: %+v", need)
	}

	// Two T4s: 16.5 / 2 + 1.5 + 0.64 per GPU, under 14.4.
	need, _, _ = Smallest(e, q, "nvidia-t4", 8)
	if math.Abs(need.PerGPUGiB-(16+float64(need.MaxModelLen)*131072/gibBytes)/2-2.14) > 1e-9 {
		t.Errorf("t4 x2 need = %+v", need)
	}
	if need.MaxModelLen%1024 != 0 || need.MaxModelLen < 4096 || need.MaxModelLen > 32768 {
		t.Errorf("t4 x2 context %d", need.MaxModelLen)
	}

	// An H100 has room for far more than 32768 tokens; the cap holds.
	need, _, ok = Estimate(e, q, "nvidia-h100-80gb", 1)
	if !ok || need.MaxModelLen != 32768 {
		t.Errorf("h100: %+v %v", need, ok)
	}
}

func TestEstimateRefusesBadCounts(t *testing.T) {
	e := llama8B()
	q := e.Quantizations[0]
	for _, n := range []int{0, 3, 16} {
		if _, reason, ok := Estimate(e, q, "nvidia-h100-80gb", n); ok || reason == "" {
			t.Errorf("count %d: fits %v, reason %q", n, ok, reason)
		}
	}
	if _, reason, ok := Smallest(e, q, "nvidia-l4", 0); ok || reason == "" {
		t.Errorf("no GPUs: fits %v, reason %q", ok, reason)
	}
	// Two T4s fit, so one T4 alone gives the memory reason.
	if _, reason, ok := Smallest(e, q, "nvidia-t4", 1); ok || !strings.Contains(reason, "NVIDIA T4 has 14.4 GiB usable") {
		t.Errorf("one T4: fits %v, reason %q", ok, reason)
	}
}

func TestSmallestGivesTheClosestReason(t *testing.T) {
	// 400 GiB of weights fit nowhere on L4s; the reason names 8 GPUs.
	e := fixtureEntry(400<<30, Architecture{Layers: 80, AttentionHeads: 64, KVHeads: 8, HeadDim: 128}, 131072, TaskChat)
	need, reason, ok := Smallest(e, e.Quantizations[0], "nvidia-l4", 8)
	if ok || need.GPUs != 8 || !strings.Contains(reason, "across 8 GPUs") || !strings.Contains(reason, "each NVIDIA L4 has 21.6 GiB usable") {
		t.Fatalf("%+v %q %v", need, reason, ok)
	}
}

func TestTensorParallelRule(t *testing.T) {
	e := fixtureEntry(14<<30, Architecture{Layers: 28, AttentionHeads: 28, KVHeads: 4, HeadDim: 128}, 32768, TaskChat)
	q := e.Quantizations[0]
	if _, reason, ok := Estimate(e, q, "nvidia-l4", 8); ok || !strings.Contains(reason, "28 attention heads don't split over 8 GPUs") {
		t.Fatalf("N=8: fits %v, reason %q", ok, reason)
	}
	if need, reason, ok := Estimate(e, q, "nvidia-l4", 4); !ok || need.GPUs != 4 {
		t.Fatalf("N=4: %+v %q %v", need, reason, ok)
	}

	cases := []struct {
		heads, kv, n int
		want         string
	}{
		{32, 8, 8, ""},
		{32, 8, 4, ""},
		{32, 2, 8, ""}, // each KV head copied four times
		{28, 4, 8, "28 attention heads don't split over 8 GPUs."},
		{64, 6, 4, "6 KV heads don't split over 4 GPUs."},
		{12, 0, 4, ""}, // pooling models keep no KV heads
	}
	for _, c := range cases {
		got := tensorParallelProblem(Architecture{AttentionHeads: c.heads, KVHeads: c.kv}, c.n)
		if got != c.want {
			t.Errorf("heads %d, kv %d, N=%d: %q, want %q", c.heads, c.kv, c.n, got, c.want)
		}
	}
}

func TestMLAUsesLatentKV(t *testing.T) {
	arch := Architecture{Layers: 27, AttentionHeads: 16, KVHeads: 16, HeadDim: 192, KVLoraRank: 512, RopeHeadDim: 64}
	e := fixtureEntry(31412968448, arch, 163840, TaskChat)
	q := e.Quantizations[0]
	if got := shapeOf(e, q).kvPerToken; got != 27*576*2 {
		t.Fatalf("kvpt = %v, want %d", got, 27*576*2)
	}
	need, _, ok := Estimate(e, q, "nvidia-h100-80gb", 1)
	if !ok {
		t.Fatal("doesn't fit an H100")
	}
	w := 31412968448 / gibBytes
	want := w + float64(need.MaxModelLen)*27*576*2/gibBytes + 1.5 + 0.08*w
	if math.Abs(need.PerGPUGiB-want) > 1e-9 {
		t.Fatalf("per GPU = %v, want %v", need.PerGPUGiB, want)
	}
}

func TestPoolingHasNoKV(t *testing.T) {
	arch := Architecture{Name: "XLMRobertaModel", Layers: 24, AttentionHeads: 16, KVHeads: 16, HeadDim: 64}
	for _, ctx := range []int{32768, 512} {
		e := fixtureEntry(2<<30, arch, ctx, TaskEmbedding)
		if err := Validate(e); err != nil {
			t.Fatal(err)
		}
		q := e.Quantizations[0]
		if got := shapeOf(e, q).kvPerToken; got != 0 {
			t.Errorf("kvpt = %v", got)
		}
		need, _, ok := Estimate(e, q, "nvidia-t4", 1)
		if !ok || need.MaxModelLen != min(ctx, 8192) {
			t.Errorf("ctx %d: %+v %v", ctx, need, ok)
		}
		if want := 2 + 1.0 + 0.08*2; math.Abs(need.PerGPUGiB-want) > 1e-9 {
			t.Errorf("ctx %d: per GPU = %v, want %v", ctx, need.PerGPUGiB, want)
		}
	}
}

func TestHybridCountsAttentionLayersOnly(t *testing.T) {
	arch := Architecture{Layers: 40, AttentionLayers: 4, AttentionHeads: 32, KVHeads: 8, HeadDim: 128}
	e := fixtureEntry(16<<30, arch, 131072, TaskChat)
	if got := shapeOf(e, e.Quantizations[0]).kvPerToken; got != 2*4*8*128*2 {
		t.Fatalf("kvpt = %v, want %d", got, 2*4*8*128*2)
	}
}

func TestVisionAndSpeechCarryAnEncoder(t *testing.T) {
	e := llama8B()
	e.Tasks = []Task{TaskChat, TaskVision}
	if o := shapeOf(e, e.Quantizations[0]).overhead(1); math.Abs(o-3.78) > 1e-9 {
		t.Errorf("vision O = %v, want 3.78", o)
	}
	e.Tasks = []Task{TaskSpeech}
	if o := shapeOf(e, e.Quantizations[0]).overhead(2); math.Abs(o-(1.5+0.64+1.0)) > 1e-9 {
		t.Errorf("speech O on 2 GPUs = %v", o)
	}
}

func TestComputeCapabilityGates(t *testing.T) {
	e := llama8B()
	fp8 := Quantization{ID: "fp8", Engine: EngineVLLM, WeightBytes: 8 << 30}
	e.Quantizations = append(e.Quantizations, fp8)
	_, reason, ok := Estimate(e, fp8, "nvidia-t4", 1)
	if ok || reason != "FP8 needs compute capability 8.0; NVIDIA T4 has 7.5." {
		t.Errorf("fp8 on t4: %v %q", ok, reason)
	}
	if _, _, ok := Estimate(e, fp8, "nvidia-l4", 1); !ok {
		t.Error("fp8 doesn't fit an L4")
	}
	mxfp4 := Quantization{ID: "mxfp4", Engine: EngineVLLM, WeightBytes: 8 << 30}
	if _, reason, ok := Estimate(e, mxfp4, "nvidia-t4", 2); ok || !strings.HasPrefix(reason, "MXFP4 needs compute capability 8.0") {
		t.Errorf("mxfp4 on t4: %v %q", ok, reason)
	}
	awq := Quantization{ID: "awq-int4", Engine: EngineVLLM, WeightBytes: 5 << 30}
	if _, reason, ok := Estimate(e, awq, "nvidia-t4", 1); !ok {
		t.Errorf("awq on t4: %q", reason)
	}

	for _, kind := range []string{"nvidia-v100-16gb", "nvidia-v100-32gb"} {
		for _, q := range []Quantization{e.Quantizations[0], awq} {
			_, reason, ok := Smallest(e, q, kind, 8)
			if ok || !strings.HasPrefix(reason, "vLLM needs compute capability 7.5; NVIDIA V100") {
				t.Errorf("%s on %s: %v %q", q.ID, kind, ok, reason)
			}
		}
	}

	for _, kind := range []string{"nvidia-geforce-rtx-4090", "amd-mi300x", "cpu", ""} {
		_, reason, ok := Smallest(e, awq, kind, 8)
		if ok || !strings.Contains(reason, "Loomplane doesn't know this GPU's compute capability") {
			t.Errorf("%q: fits %v, reason %q", kind, ok, reason)
		}
	}
	if _, _, ok := Estimate(e, awq, "amd-mi300x", 1); ok {
		t.Error("amd-mi300x fits")
	}
	if _, ok := ComputeCapability("amd-mi300x"); ok {
		t.Error("amd-mi300x has a compute capability")
	}
}

func TestComputeCapabilityTable(t *testing.T) {
	want := map[string]float64{
		"nvidia-t4": 7.5, "nvidia-a10": 8.6, "nvidia-a10g": 8.6, "nvidia-l4": 8.9, "nvidia-l40s": 8.9,
		"nvidia-rtx-4000-ada": 8.9, "nvidia-rtx-6000-ada": 8.9, "nvidia-v100-16gb": 7.0, "nvidia-v100-32gb": 7.0,
		"nvidia-a100-40gb": 8.0, "nvidia-a100-80gb": 8.0, "nvidia-h100-80gb": 9.0, "nvidia-h100-nvl-94gb": 9.0,
		"nvidia-h200-141gb": 9.0,
	}
	for kind, cc := range want {
		if got, ok := ComputeCapability(kind); !ok || got != cc {
			t.Errorf("ComputeCapability(%s) = %v, %v; want %v", kind, got, ok, cc)
		}
		if _, ok := provider.LookupAcceleratorKind(kind); !ok {
			t.Errorf("%s isn't in P1's kind table", kind)
		}
	}
	for _, k := range provider.AcceleratorKinds() {
		if _, ok := ComputeCapability(k.ID); !ok && k.ID != "amd-mi300x" {
			t.Errorf("P1 kind %s has no compute capability", k.ID)
		}
	}
	if len(computeCapabilities) != len(want) {
		t.Errorf("table has %d kinds, want %d", len(computeCapabilities), len(want))
	}
}

func cpuEntry(engine Engine, weightBytes int64) (Entry, Quantization) {
	if engine == EngineTEI {
		e := fixtureEntry(weightBytes, Architecture{Name: "BertModel", Layers: 12, AttentionHeads: 12, HeadDim: 32}, 512, TaskEmbedding)
		e.Quantizations = []Quantization{{ID: "native", Engine: EngineTEI, WeightBytes: weightBytes}}
		return e, e.Quantizations[0]
	}
	e := llama8B()
	e.Quantizations = []Quantization{{ID: "gguf-q4_k_m", Engine: EngineLlamaCPP, File: "model-Q4_K_M.gguf", WeightBytes: weightBytes}}
	return e, e.Quantizations[0]
}

func TestCPUFits(t *testing.T) {
	e, q := cpuEntry(EngineTEI, 1<<30)
	if err := Validate(e); err != nil {
		t.Fatal(err)
	}
	need, reason, ok := CPUFits(e, q)
	if !ok || math.Abs(need.RAMGiB-1.6) > 1e-9 || need.GPUs != 0 || need.MaxModelLen != 512 {
		t.Errorf("tei 1 GiB: %+v %q %v", need, reason, ok)
	}
	if got := MinMemoryGiB(e, q); got != 2 {
		t.Errorf("tei 1 GiB min memory %d, want 2", got)
	}

	e, q = cpuEntry(EngineTEI, 3584<<20) // 3.5 GiB
	need, reason, ok = CPUFits(e, q)
	if ok || !strings.Contains(reason, "3.5 GiB") {
		t.Errorf("tei 3.5 GiB: %+v %q %v", need, reason, ok)
	}
	if want := "Needs 4.4 GiB of memory; Text Embeddings Inference on CPU gets 3.5 GiB."; reason != want {
		t.Errorf("reason %q, want %q", reason, want)
	}

	e, q = cpuEntry(EngineLlamaCPP, q4Bytes())
	if err := Validate(e); err != nil {
		t.Fatal(err)
	}
	need, reason, ok = CPUFits(e, q)
	if !ok || need.MaxModelLen != 8192 || math.Abs(need.RAMGiB-6.538) > 1e-6 {
		t.Errorf("llama.cpp Q4: %+v %q %v", need, reason, ok)
	}
	if got := MinMemoryGiB(e, q); got != 7 {
		t.Errorf("llama.cpp Q4 min memory %d, want 7", got)
	}

	e, q = cpuEntry(EngineLlamaCPP, 6<<30)
	if _, reason, ok := CPUFits(e, q); ok || reason != "Needs 8.1 GiB of memory at an 8192 token context; llama.cpp on CPU gets 7.0 GiB." {
		t.Errorf("llama.cpp 6 GiB: %v %q", ok, reason)
	}

	vllm := llama8B()
	if _, reason, ok := CPUFits(vllm, vllm.Quantizations[0]); ok || !strings.Contains(reason, "vLLM needs a GPU") {
		t.Errorf("vllm on CPU: %v %q", ok, reason)
	}
	if got := MinMemoryGiB(vllm, vllm.Quantizations[0]); got != 0 {
		t.Errorf("vllm min memory %d", got)
	}
}

func TestLlamaCPPOnAGPU(t *testing.T) {
	e, q := cpuEntry(EngineLlamaCPP, q4Bytes())
	need, reason, ok := Estimate(e, q, "nvidia-t4", 1)
	if !ok || need.GPUs != 1 {
		t.Fatalf("t4: %+v %q %v", need, reason, ok)
	}
	// U = 0.95 x 16 = 15.2; need = 4.58 + kv + 0.6, capped at 32768 tokens.
	if want := 4.58 + 32768*131072.0/gibBytes + 0.6; need.MaxModelLen != 32768 || math.Abs(need.PerGPUGiB-want) > 1e-6 {
		t.Errorf("t4: %+v, want per GPU %v", need, want)
	}
	if _, reason, ok := Estimate(e, q, "nvidia-t4", 2); ok || !strings.Contains(reason, "one GPU") {
		t.Errorf("two GPUs: %v %q", ok, reason)
	}
	if need, _, ok := Smallest(e, q, "nvidia-t4", 8); !ok || need.GPUs != 1 {
		t.Errorf("smallest: %+v %v", need, ok)
	}
	if IsGPU(q) || !IsGPU(llama8B().Quantizations[0]) {
		t.Error("IsGPU is only true for vLLM")
	}
	if MinGPUMemoryGiB(e, q) != 0 {
		t.Error("llama.cpp has a min GPU memory")
	}
	tei, teiQ := cpuEntry(EngineTEI, 1<<30)
	if _, reason, ok := Estimate(tei, teiQ, "nvidia-l4", 1); ok || !strings.Contains(reason, "runs on CPU") {
		t.Errorf("tei on a GPU: %v %q", ok, reason)
	}
}

func TestTokenContextArticle(t *testing.T) {
	for n, want := range map[int]string{
		4096: "a 4096", 8192: "an 8192", 11000: "an 11000", 18432: "an 18432", 80000: "an 80000",
		32768: "a 32768", 131072: "a 131072", 512: "a 512", 800: "an 800", 1024: "a 1024",
	} {
		if got := tokenContext(n); got != want+" token context" {
			t.Errorf("tokenContext(%d) = %q", n, got)
		}
	}
}

// fitRegistry is a registry holding e and the given results.
func fitRegistry(e Entry, results ...Result) *Registry {
	return &Registry{entries: []Entry{e}, byID: map[string]int{e.ID: 0}, results: results}
}

func l4Result(e Entry, version, outcome, detail string, ranAt time.Time) Result {
	return Result{
		Entry: e.ID, Quantization: "bf16", Revision: e.Revision, GPUKind: "nvidia-l4", GPUs: 1, MaxModelLen: 22528,
		Engine: EngineVLLM, EngineVersion: version, Outcome: outcome, Detail: detail, RanAt: ranAt,
		Run: "https://github.com/xraph/loomplane/actions/runs/1",
	}
}

func TestFitOnUsesOnlyCurrentEngineResults(t *testing.T) {
	e := llama8B()
	q := e.Quantizations[0]
	current := map[Engine]string{EngineVLLM: "0.31.0"}
	day := time.Date(2026, 11, 3, 6, 0, 0, 0, time.UTC)

	fit := fitRegistry(e, l4Result(e, "0.30.0", OutcomePassed, "first token 210 ms, 61 tokens/s", day)).FitOn(e, q, "nvidia-l4", 1, current)
	if fit.Status != StatusEstimated || fit.Result != nil || !fit.Fits() || fit.Need.MaxModelLen != 22528 || fit.GPUKind != "nvidia-l4" {
		t.Errorf("old engine: %+v", fit)
	}

	passed := l4Result(e, "0.31.0", OutcomePassed, "first token 210 ms, 61 tokens/s", day)
	fit = fitRegistry(e, passed).FitOn(e, q, "nvidia-l4", 1, current)
	if fit.Status != StatusTested || fit.Result == nil || *fit.Result != passed || !fit.Fits() || fit.Reason != "" {
		t.Errorf("current engine: %+v", fit)
	}

	failed := l4Result(e, "0.31.0", OutcomeFailed, "CUDA out of memory while loading weights", day)
	fit = fitRegistry(e, failed).FitOn(e, q, "nvidia-l4", 1, current)
	if fit.Status != StatusFailed || fit.Fits() || fit.Result == nil || fit.Reason != failed.Detail {
		t.Errorf("failed: %+v", fit)
	}

	// The newest result counts, whichever way it went.
	later := passed
	later.RanAt = day.Add(24 * time.Hour)
	if fit := fitRegistry(e, failed, later).FitOn(e, q, "nvidia-l4", 1, current); fit.Status != StatusTested {
		t.Errorf("passed after failed: %+v", fit)
	}
	laterFail := failed
	laterFail.RanAt = day.Add(24 * time.Hour)
	if fit := fitRegistry(e, laterFail, passed).FitOn(e, q, "nvidia-l4", 1, current); fit.Status != StatusFailed {
		t.Errorf("failed after passed: %+v", fit)
	}

	// A run on the weights before a re-pin proves nothing about today's.
	repinned := e
	repinned.Revision = "89abcdef0123456789abcdef0123456789abcdef"
	if fit := fitRegistry(repinned, passed).FitOn(repinned, q, "nvidia-l4", 1, current); fit.Status != StatusEstimated || fit.Result != nil {
		t.Errorf("old revision: %+v", fit)
	}
	matching := passed
	matching.Revision = repinned.Revision
	if fit := fitRegistry(repinned, passed, matching).FitOn(repinned, q, "nvidia-l4", 1, current); fit.Status != StatusTested || fit.Result.Revision != repinned.Revision {
		t.Errorf("matching revision: %+v", fit)
	}

	// A failed run at or below the served context outranks a newer pass at
	// a shorter one, which proves nothing about the context served.
	failedShorter := failed
	failedShorter.MaxModelLen = 16384
	passedShort := passed
	passedShort.MaxModelLen = 8192
	passedShort.RanAt = day.Add(24 * time.Hour)
	if fit := fitRegistry(e, failedShorter, passedShort).FitOn(e, q, "nvidia-l4", 1, current); fit.Status != StatusFailed || fit.Result.MaxModelLen != 16384 {
		t.Errorf("failed below served context, newer short pass: %+v", fit)
	}
	// A newer pass that covers the served context still wins.
	if fit := fitRegistry(e, failedShorter, later).FitOn(e, q, "nvidia-l4", 1, current); fit.Status != StatusTested {
		t.Errorf("failed, then a covering pass: %+v", fit)
	}

	// No current version for the engine: nothing counts.
	if fit := fitRegistry(e, passed).FitOn(e, q, "nvidia-l4", 1, nil); fit.Status != StatusEstimated {
		t.Errorf("no current version: %+v", fit)
	}
}

func TestFitOnNeverCallsAnEstimateTested(t *testing.T) {
	e := llama8B()
	q := e.Quantizations[0]
	current := map[Engine]string{EngineVLLM: "0.31.0"}
	day := time.Date(2026, 11, 3, 6, 0, 0, 0, time.UTC)
	passed := l4Result(e, "0.31.0", OutcomePassed, "ok", day)

	// A run on another kind, quantization or GPU count proves nothing here.
	other := passed
	other.GPUKind = "nvidia-l40s"
	twoGPUs := passed
	twoGPUs.GPUs = 2
	shortContext := passed
	shortContext.MaxModelLen = 4096
	for name, res := range map[string]Result{"other kind": other, "two GPUs": twoGPUs, "shorter context": shortContext} {
		if fit := fitRegistry(e, res).FitOn(e, q, "nvidia-l4", 2, current); fit.Status != StatusEstimated || fit.Result != nil {
			t.Errorf("%s: %+v", name, fit)
		}
	}

	// The estimate says no and nothing failed: no-fit with the estimate's reason.
	fit := fitRegistry(e, passed).FitOn(e, q, "nvidia-rtx-4000-ada", 1, current)
	if fit.Status != StatusNoFit || fit.Fits() || !strings.Contains(fit.Reason, "18.0 GiB usable") {
		t.Errorf("rtx 4000 ada: %+v", fit)
	}

	// A failed run explains a no-fit better than the estimate.
	ada := l4Result(e, "0.31.0", OutcomeFailed, "the model didn't load", day)
	ada.GPUKind = "nvidia-rtx-4000-ada"
	fit = fitRegistry(e, ada).FitOn(e, q, "nvidia-rtx-4000-ada", 1, current)
	if fit.Status != StatusFailed || fit.Reason != "the model didn't load" {
		t.Errorf("rtx 4000 ada failed: %+v", fit)
	}

	// An entry outside the registry is untested when it fits.
	custom := e
	custom.ID = "hf:someone/Model@0123456789ab"
	if fit := fitRegistry(e, passed).FitOn(custom, q, "nvidia-l4", 1, current); fit.Status != StatusUntested || !fit.Fits() || fit.Result != nil {
		t.Errorf("custom: %+v", fit)
	}
	if fit := fitRegistry(e).FitOn(custom, q, "nvidia-rtx-4000-ada", 1, current); fit.Status != StatusNoFit {
		t.Errorf("custom no-fit: %+v", fit)
	}

	// Unknown GPUs never fit, whatever a result says.
	if fit := fitRegistry(e).FitOn(e, q, "amd-mi300x", 8, current); fit.Status != StatusNoFit {
		t.Errorf("amd: %+v", fit)
	}
}

func TestFitOnCPU(t *testing.T) {
	e, q := cpuEntry(EngineTEI, 1<<30)
	current := map[Engine]string{EngineTEI: "1.8.2"}
	res := Result{
		Entry: e.ID, Quantization: q.ID, Revision: e.Revision, GPUKind: CPUKind, MaxModelLen: 512, Engine: EngineTEI,
		EngineVersion: "1.8.2", Outcome: OutcomePassed, RanAt: time.Date(2026, 11, 3, 6, 0, 0, 0, time.UTC),
	}
	fit := fitRegistry(e, res).FitOn(e, q, CPUKind, 0, current)
	if fit.Status != StatusTested || fit.Need.RAMGiB == 0 || fit.Need.GPUs != 0 {
		t.Errorf("tei tested: %+v", fit)
	}
	if fit := fitRegistry(e).FitOn(e, q, CPUKind, 0, current); fit.Status != StatusEstimated {
		t.Errorf("tei estimated: %+v", fit)
	}
	vllm := llama8B()
	if fit := fitRegistry(vllm).FitOn(vllm, vllm.Quantizations[0], CPUKind, 0, current); fit.Status != StatusNoFit {
		t.Errorf("vllm on cpu: %+v", fit)
	}
}

func TestFitOnUsesTheQuantizationsOwnRevision(t *testing.T) {
	e, q := cpuEntry(EngineLlamaCPP, q4Bytes())
	q.Repo = "someone/Model-8B-GGUF"
	q.Revision = "1111111111222222222233333333334444444444"
	e.Quantizations = []Quantization{q}
	current := map[Engine]string{EngineLlamaCPP: "b6000"}
	res := Result{
		Entry: e.ID, Quantization: q.ID, Revision: e.Revision, GPUKind: CPUKind, MaxModelLen: 8192,
		Engine: EngineLlamaCPP, EngineVersion: "b6000", Outcome: OutcomePassed,
		RanAt: time.Date(2026, 11, 3, 6, 0, 0, 0, time.UTC),
	}
	if fit := fitRegistry(e, res).FitOn(e, q, CPUKind, 0, current); fit.Status != StatusEstimated {
		t.Errorf("entry revision on a GGUF repo: %+v", fit)
	}
	res.Revision = q.Revision
	if fit := fitRegistry(e, res).FitOn(e, q, CPUKind, 0, current); fit.Status != StatusTested {
		t.Errorf("GGUF repo revision: %+v", fit)
	}
}

func TestKVCacheSplitsOnlyOverKVHeads(t *testing.T) {
	// 2 KV heads on 8 GPUs: each GPU holds a copy of one head, so the KV
	// cache splits two ways, not eight. kvpt = 2 x 32 x 2 x 128 x 2 = 32768
	// bytes, half a GiB per 32768 tokens on each GPU.
	e := fixtureEntry(148<<30, Architecture{Layers: 32, AttentionHeads: 64, KVHeads: 2, HeadDim: 128}, 131072, TaskChat)
	q := e.Quantizations[0]
	need, reason, ok := Estimate(e, q, "nvidia-l4", 8)
	if !ok {
		t.Fatalf("doesn't fit 8 L4s: %s", reason)
	}
	// Fixed per GPU: 148/8 + 1.5 + 0.08 x 148/8 = 21.48. Per token: 2^15 / 2^30 / 2.
	// (21.6 - 21.48) x 65536 = 7864 tokens, rounded down to 7168.
	if need.MaxModelLen != 7168 {
		t.Errorf("context %d, want 7168", need.MaxModelLen)
	}
	if want := 21.48 + 7168.0/65536; math.Abs(need.PerGPUGiB-want) > 1e-9 {
		t.Errorf("per GPU = %v, want %v", need.PerGPUGiB, want)
	}

	// MLA keeps one latent cache, copied on all 4 GPUs. 30 GiB of weights:
	// 7.5 + 1.5 + 0.6 = 9.6, plus 32768 x 27 x 576 x 2 / 2^30 = 0.94921875.
	mla := fixtureEntry(30<<30, Architecture{Layers: 27, AttentionHeads: 16, KVHeads: 16, HeadDim: 192, KVLoraRank: 512, RopeHeadDim: 64}, 163840, TaskChat)
	need, reason, ok = Estimate(mla, mla.Quantizations[0], "nvidia-l4", 4)
	if !ok || need.MaxModelLen != 32768 {
		t.Fatalf("mla on 4 L4s: %+v %s", need, reason)
	}
	if want := 9.6 + 0.94921875; math.Abs(need.PerGPUGiB-want) > 1e-9 {
		t.Errorf("mla per GPU = %v, want %v", need.PerGPUGiB, want)
	}
}

func TestQualificationFileLoads(t *testing.T) {
	data, err := os.ReadFile("data/qualification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f qualification
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if f.Results == nil {
		t.Error(`want "results: []", not a missing list`)
	}

	r := Default()
	for _, res := range r.Results() {
		e, ok := r.Entry(res.Entry)
		if !ok {
			t.Errorf("result names entry %q, which isn't in the registry", res.Entry)
			continue
		}
		if _, ok := e.Quantization(res.Quantization); !ok {
			t.Errorf("result names quantization %s of %s, which doesn't exist", res.Quantization, res.Entry)
		}
		if _, ok := provider.LookupAcceleratorKind(res.GPUKind); !ok && res.GPUKind != CPUKind {
			t.Errorf("result names GPU kind %q", res.GPUKind)
		}
	}
}

func TestLoadReadsQualificationResults(t *testing.T) {
	r := mustLoadGood(t)
	got := r.Results()
	if len(got) != 2 || got[0].Entry != "acme/Small-1B-Instruct" || got[0].Outcome != OutcomePassed ||
		got[1].GPUKind != CPUKind || !got[0].RanAt.Equal(time.Date(2026, 11, 3, 6, 12, 0, 0, time.UTC)) {
		t.Fatalf("Results() = %+v", got)
	}
	got[0].Entry = "changed"
	if r.Results()[0].Entry != "acme/Small-1B-Instruct" {
		t.Error("changing a returned result changed the registry")
	}

	_, err := loadFixture(t, "bad-qualification")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad results: %v", err)
	}
	for _, want := range []string{
		"no registry entry has that ID",
		"gpu_kind isn't a known GPU kind",
		"gpus must be 1, 2, 4 or 8",
		"max_model_len must be positive",
		"engine_version is required",
		`outcome "maybe"`,
		"ran_at is required",
		`engine "llamacpp", but the quantization runs on vllm`,
		"the entry has no such quantization",
		"a CPU run has gpus 0",
		`revision "" isn't a 40-character commit SHA`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error doesn't say %q:\n%v", want, err)
		}
	}

	if _, err := loadFixture(t, "bad-qualification-field"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "tested") {
		t.Fatalf("unknown field: %v", err)
	}
}

// q4Bytes is a 4.58 GiB Q4 GGUF file, an 8B model's size.
func q4Bytes() int64 {
	size := 4.58
	return int64(size * gibBytes)
}
