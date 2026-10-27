package registry

import (
	"fmt"
	"math"
	"strconv"
)

// GPUMemoryUtilization is the share of each GPU's memory vLLM may use
// (--gpu-memory-utilization).
const GPUMemoryUtilization = 0.90

const (
	gibBytes = float64(1 << 30)

	// kvDtypeBytes is two because Loomplane never turns on an FP8 KV cache
	// by default.
	kvDtypeBytes = 2

	contextFloor   = 4096  // the least context a fit must serve
	contextCeiling = 32768 // the most context Loomplane serves by default
	contextStep    = 1024  // served contexts are multiples of this
	poolingContext = 8192  // what embedding and rerank models serve
	cpuContext     = 8192  // what llama.cpp serves on CPU

	llamaCPPGPUUtilization = 0.95
	llamaCPPGPUOverheadGiB = 0.6

	// The CPU kinds reserve 4096 and 8192 MB; these leave room for the OS.
	teiRAMGiB      = 3.5
	llamaCPPRAMGiB = 7.0
)

// tensorParallelSizes are the GPU counts Loomplane splits a model over.
var tensorParallelSizes = []int{1, 2, 4, 8}

// Need is what one quantization of an entry asks of a target.
type Need struct {
	PerGPUGiB   float64 `json:"per_gpu_gib"` // at the served context
	MaxModelLen int     `json:"max_model_len"`
	GPUs        int     `json:"gpus"`              // tensor parallel size; 0 on CPU
	RAMGiB      float64 `json:"ram_gib,omitempty"` // CPU engines
}

// shape is the part of an entry and quantization the memory math reads.
// Sizes are GiB, except kvPerToken, which is bytes.
type shape struct {
	weights       float64
	kvPerToken    float64
	pooling       bool
	extraOverhead float64 // vision and speech-to-text entries carry an encoder
	contextLength int
	ctxMin        int
	ctxCap        int
}

func shapeOf(e Entry, q Quantization) shape {
	s := shape{
		weights:       float64(q.WeightBytes) / gibBytes,
		pooling:       poolingOnly(e.Tasks),
		contextLength: e.ContextLength,
		ctxMin:        min(e.ContextLength, contextFloor),
		ctxCap:        min(e.ContextLength, contextCeiling),
	}
	if !s.pooling {
		s.kvPerToken = kvBytesPerToken(e.Architecture)
	}
	for _, t := range e.Tasks {
		if t == TaskVision || t == TaskSpeech {
			s.extraOverhead = 1.0
		}
	}
	return s
}

// kvBytesPerToken is the KV cache one token takes across every attention
// layer. Multi-head latent attention caches the compressed latent and the
// rope key instead of full keys and values.
func kvBytesPerToken(a Architecture) float64 {
	layers := a.AttentionLayers
	if layers == 0 {
		layers = a.Layers
	}
	if a.KVLoraRank > 0 {
		return float64(layers) * float64(a.KVLoraRank+a.RopeHeadDim) * kvDtypeBytes
	}
	return 2 * float64(layers) * float64(a.KVHeads) * float64(a.HeadDim) * kvDtypeBytes
}

func (s shape) kvGiB(ctx int) float64 { return s.kvPerToken * float64(ctx) / gibBytes }

// overhead is the per-GPU memory vLLM takes beyond weights and KV cache:
// activations, CUDA graphs and the CUDA context.
func (s shape) overhead(n int) float64 {
	base := 1.5
	if s.pooling {
		base = 1.0
	}
	return base + 0.08*s.weights/float64(n) + s.extraOverhead
}

// vllmNeed is the memory one of n GPUs needs to serve ctx tokens on vLLM.
func (s shape) vllmNeed(n, ctx int) float64 {
	return (s.weights+s.kvGiB(ctx))/float64(n) + s.overhead(n)
}

// llamaCPPGPUNeed is the memory llama.cpp needs on one GPU to serve ctx tokens.
func (s shape) llamaCPPGPUNeed(ctx int) float64 {
	return s.weights + s.kvGiB(ctx) + llamaCPPGPUOverheadGiB
}

// cpuRAM is the memory a CPU engine needs to serve ctx tokens.
func (s shape) cpuRAM(ctx int) float64 {
	return 1.1*s.weights + s.kvGiB(ctx) + 0.5
}

// servedContext is the largest context whose need stays within usable,
// where the need is fixed plus perToken for every token. It's rounded down
// to a multiple of 1024 and kept between ctxMin and ctxCap. Pooling models
// keep no KV cache and serve up to 8192 tokens.
func (s shape) servedContext(fixed, perToken, usable float64) int {
	if s.pooling {
		return min(s.contextLength, poolingContext)
	}
	if perToken <= 0 {
		return s.ctxCap
	}
	most := math.Floor((usable - fixed) / perToken)
	// Anything past 2^30 tokens clamps to ctxCap anyway; this keeps the
	// conversion to int in range.
	most = math.Min(most, 1<<30)
	ctx := int(most) / contextStep * contextStep
	return min(max(ctx, s.ctxMin), s.ctxCap)
}

// attempt is one estimate, with whether it failed on memory alone, which
// Smallest uses to pick the most useful reason.
type attempt struct {
	need   Need
	reason string
	ok     bool
	memory bool
}

// Estimate returns where q of e fits on count GPUs of kind, or why not
// (Reason in plain words). The Need holds the served context and the
// per-GPU memory at it; when q doesn't fit, it holds the need at the least
// context Loomplane serves.
func Estimate(e Entry, q Quantization, kind string, count int) (Need, string, bool) {
	a := estimate(e, q, kind, count)
	return a.need, a.reason, a.ok
}

func estimate(e Entry, q Quantization, kind string, count int) attempt {
	switch q.Engine {
	case EngineVLLM:
		return estimateVLLM(e, q, kind, count)
	case EngineLlamaCPP:
		return estimateLlamaCPPGPU(e, q, kind, count)
	}
	return attempt{reason: fmt.Sprintf("%s runs on CPU here, not on a GPU.", engineTitle(q.Engine))}
}

func estimateVLLM(e Entry, q Quantization, kind string, count int) attempt {
	if count < 1 {
		return attempt{reason: "A model on a GPU needs at least one GPU."}
	}
	if !validTensorParallel(count) {
		return attempt{reason: fmt.Sprintf("Loomplane splits a model over 1, 2, 4 or 8 GPUs, not %d.", count)}
	}
	s := shapeOf(e, q)
	a := attempt{need: Need{PerGPUGiB: s.vllmNeed(count, s.ctxMin), MaxModelLen: s.ctxMin, GPUs: count}}
	g, reason, ok := lookupGPU(kind)
	if !ok {
		a.reason = reason
		return a
	}
	if p := computeCapabilityProblem(q, g); p != "" {
		a.reason = p
		return a
	}
	if p := tensorParallelProblem(e.Architecture, count); p != "" {
		a.reason = p
		return a
	}
	usable := GPUMemoryUtilization * g.memoryGiB
	if a.need.PerGPUGiB > usable {
		a.reason = memoryReason(a.need.PerGPUGiB, count, s.ctxMin, g.title, usable)
		a.memory = true
		return a
	}
	perToken := s.kvPerToken / gibBytes / float64(count)
	ctx := s.servedContext(s.weights/float64(count)+s.overhead(count), perToken, usable)
	a.need = Need{PerGPUGiB: s.vllmNeed(count, ctx), MaxModelLen: ctx, GPUs: count}
	a.ok = true
	return a
}

func estimateLlamaCPPGPU(e Entry, q Quantization, kind string, count int) attempt {
	if count != 1 {
		return attempt{reason: fmt.Sprintf("llama.cpp runs a model on one GPU, not %d.", count)}
	}
	s := shapeOf(e, q)
	a := attempt{need: Need{PerGPUGiB: s.llamaCPPGPUNeed(s.ctxMin), MaxModelLen: s.ctxMin, GPUs: 1}}
	g, reason, ok := lookupGPU(kind)
	if !ok {
		a.reason = reason
		return a
	}
	if p := computeCapabilityProblem(q, g); p != "" {
		a.reason = p
		return a
	}
	usable := llamaCPPGPUUtilization * g.memoryGiB
	if a.need.PerGPUGiB > usable {
		a.reason = memoryReason(a.need.PerGPUGiB, 1, s.ctxMin, g.title, usable)
		a.memory = true
		return a
	}
	ctx := s.servedContext(s.weights+llamaCPPGPUOverheadGiB, s.kvPerToken/gibBytes, usable)
	a.need = Need{PerGPUGiB: s.llamaCPPGPUNeed(ctx), MaxModelLen: ctx, GPUs: 1}
	a.ok = true
	return a
}

func validTensorParallel(n int) bool {
	for _, size := range tensorParallelSizes {
		if n == size {
			return true
		}
	}
	return false
}

// tensorParallelProblem says why the model's heads don't split evenly over
// n GPUs, or "" when they do. KV heads may also be fewer than the GPUs, as
// long as each one is copied a whole number of times.
func tensorParallelProblem(a Architecture, n int) string {
	if a.AttentionHeads%n != 0 {
		return fmt.Sprintf("%d attention heads don't split over %d GPUs.", a.AttentionHeads, n)
	}
	if a.KVHeads%n != 0 && n%a.KVHeads != 0 {
		return fmt.Sprintf("%d KV heads don't split over %d GPUs.", a.KVHeads, n)
	}
	return ""
}

// Smallest returns the smallest count (1, 2, 4, 8) up to maxCount that
// fits. When none does, the reason is the one from the largest count that
// failed on memory alone, since that's the closest Loomplane came, or else
// the first count's reason.
func Smallest(e Entry, q Quantization, kind string, maxCount int) (Need, string, bool) {
	counts := tensorParallelSizes
	if q.Engine == EngineLlamaCPP {
		counts = []int{1}
	}
	var first, closest *attempt
	for _, n := range counts {
		if n > maxCount {
			break
		}
		a := estimate(e, q, kind, n)
		if a.ok {
			return a.need, "", true
		}
		if first == nil {
			first = &a
		}
		if a.memory {
			closest = &a
		}
	}
	switch {
	case closest != nil:
		return closest.need, closest.reason, false
	case first != nil:
		return first.need, first.reason, false
	}
	return Need{}, "There are no GPUs here to run it on.", false
}

// MinGPUMemoryGiB is the least GPU memory, in whole GiB, one GPU needs to
// serve q of e at the least context. It's what min_gpu_memory_gib stores.
// Quantizations that don't need a GPU return 0.
func MinGPUMemoryGiB(e Entry, q Quantization) int {
	if !IsGPU(q) {
		return 0
	}
	s := shapeOf(e, q)
	return ceilInt(s.vllmNeed(1, s.ctxMin) / GPUMemoryUtilization)
}

// CPUFits returns whether q of e fits the CPU kind its engine runs on: TEI,
// or llama.cpp on CPU. The Need holds RAMGiB and no GPUs.
func CPUFits(e Entry, q Quantization) (Need, string, bool) {
	s := shapeOf(e, q)
	var ctx, served int
	var limit float64
	switch q.Engine {
	case EngineTEI:
		ctx, served, limit = 0, min(e.ContextLength, poolingContext), teiRAMGiB
	case EngineLlamaCPP:
		ctx = min(e.ContextLength, cpuContext)
		served, limit = ctx, llamaCPPRAMGiB
	default:
		return Need{}, fmt.Sprintf("%s needs a GPU; it doesn't run on CPU.", engineTitle(q.Engine)), false
	}
	need := Need{RAMGiB: s.cpuRAM(ctx), MaxModelLen: served}
	if need.RAMGiB > limit {
		at := ""
		if ctx > 0 {
			at = " at " + tokenContext(ctx)
		}
		return need, fmt.Sprintf("Needs %s GiB of memory%s; %s on CPU gets %s GiB.",
			gibUp(need.RAMGiB), at, engineTitle(q.Engine), gibDown(limit)), false
	}
	return need, "", true
}

// MinMemoryGiB is the RAM, in whole GiB, a CPU engine needs for q of e. It's
// what min_memory_gib stores. vLLM quantizations return 0.
func MinMemoryGiB(e Entry, q Quantization) int {
	need, _, _ := CPUFits(e, q)
	return ceilInt(need.RAMGiB)
}

// IsGPU reports whether q always needs a GPU, which is true for vLLM. TEI
// runs on CPU. llama.cpp runs on either, so the caller decides where it goes.
func IsGPU(q Quantization) bool { return q.Engine == EngineVLLM }

// ceilInt rounds up, ignoring float noise below a billionth so an exact
// 22.0 stays 22.
func ceilInt(x float64) int { return int(math.Ceil(x - 1e-9)) }

// gibUp shows a need to a tenth of a GiB, rounded up, so a need never looks
// smaller than it is.
func gibUp(x float64) string {
	return strconv.FormatFloat(math.Ceil(x*10-1e-9)/10, 'f', 1, 64)
}

// gibDown shows available memory to a tenth of a GiB, rounded down.
func gibDown(x float64) string {
	return strconv.FormatFloat(math.Floor(x*10+1e-9)/10, 'f', 1, 64)
}

func memoryReason(need float64, n, ctx int, title string, usable float64) string {
	if n == 1 {
		return fmt.Sprintf("Needs %s GiB per GPU at %s; %s has %s GiB usable.",
			gibUp(need), tokenContext(ctx), title, gibDown(usable))
	}
	return fmt.Sprintf("Needs %s GiB per GPU across %d GPUs at %s; each %s has %s GiB usable.",
		gibUp(need), n, tokenContext(ctx), title, gibDown(usable))
}

// tokenContext reads "a 4096 token context" or "an 8192 token context".
func tokenContext(n int) string {
	return article(n) + " " + strconv.Itoa(n) + " token context"
}

// article picks "a" or "an" by how the number is said: eight, eleven,
// eighteen and eighty take "an".
func article(n int) string {
	lead := n
	for lead >= 1000 {
		lead /= 1000
	}
	if lead == 11 || lead == 18 {
		return "an"
	}
	for lead >= 10 {
		lead /= 10
	}
	if lead == 8 {
		return "an"
	}
	return "a"
}
