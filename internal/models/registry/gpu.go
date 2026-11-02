package registry

import (
	"fmt"

	"github.com/xraph/loomplane/pkg/provider"
)

// computeCapabilities is the CUDA compute capability of every GPU kind the
// memory math knows. A kind missing here never fits, even when P1's kind
// table lists it.
var computeCapabilities = map[string]float64{
	"nvidia-t4":            7.5,
	"nvidia-a10":           8.6,
	"nvidia-a10g":          8.6,
	"nvidia-l4":            8.9,
	"nvidia-l40s":          8.9,
	"nvidia-rtx-4000-ada":  8.9,
	"nvidia-rtx-6000-ada":  8.9,
	"nvidia-v100-16gb":     7.0,
	"nvidia-v100-32gb":     7.0,
	"nvidia-a100-40gb":     8.0,
	"nvidia-a100-80gb":     8.0,
	"nvidia-h100-80gb":     9.0,
	"nvidia-h100-nvl-94gb": 9.0,
	"nvidia-h200-141gb":    9.0,
}

// ComputeCapability returns the CUDA compute capability of a P1 GPU kind.
// It returns false for any kind outside its table, AMD ones included.
func ComputeCapability(kind string) (float64, bool) {
	cc, ok := computeCapabilities[kind]
	return cc, ok
}

// engineComputeCapability is the least compute capability each engine runs
// on. TEI runs on CPU only, so it has none.
var engineComputeCapability = map[Engine]float64{
	EngineVLLM:     7.5,
	EngineLlamaCPP: 6.1,
}

// quantizationComputeCapability is the least compute capability each GPU
// weight format needs. bf16 is served as fp16 below 8.0.
var quantizationComputeCapability = map[string]struct {
	label string
	cc    float64
}{
	"fp8":      {"FP8", 8.0},
	"mxfp4":    {"MXFP4", 8.0},
	"awq-int4": {"AWQ", 7.5},
	"bf16":     {"BF16", 7.5},
}

func engineTitle(en Engine) string {
	switch en {
	case EngineVLLM:
		return "vLLM"
	case EngineTEI:
		return "Text Embeddings Inference"
	case EngineLlamaCPP:
		return "llama.cpp"
	}
	return string(en)
}

// gpuKind is what the memory math needs to know about one GPU kind.
type gpuKind struct {
	title     string
	memoryGiB float64
	cc        float64
}

// lookupGPU returns the kind, or why the memory math can't place anything
// on it. A kind needs both a row in P1's table and a compute capability.
func lookupGPU(kind string) (gpuKind, string, bool) {
	k, known := provider.LookupAcceleratorKind(kind)
	cc, ccKnown := ComputeCapability(kind)
	if !known || !ccKnown || k.MemoryGiB <= 0 {
		name := kind
		if known {
			name = k.Title
		}
		return gpuKind{}, fmt.Sprintf("Loomplane doesn't know this GPU's compute capability (%s), so it can't say what fits on it.", name), false
	}
	return gpuKind{title: k.Title, memoryGiB: float64(k.MemoryGiB), cc: cc}, "", true
}

// computeCapabilityProblem says why q can't run on g, or "" when it can.
func computeCapabilityProblem(q Quantization, g gpuKind) string {
	if need, ok := engineComputeCapability[q.Engine]; ok && g.cc < need {
		return fmt.Sprintf("%s needs compute capability %.1f; %s has %.1f.", engineTitle(q.Engine), need, g.title, g.cc)
	}
	if need, ok := quantizationComputeCapability[q.ID]; ok && g.cc < need.cc {
		return fmt.Sprintf("%s needs compute capability %.1f; %s has %.1f.", need.label, need.cc, g.title, g.cc)
	}
	return ""
}
