// Package registry is Loomplane's catalog of self-hostable models. Each
// model family lives in one YAML file under data/, embedded in the binary,
// and every entry is pinned to an exact Hugging Face revision.
package registry

import "errors"

// Task is something a model can do. An entry's first task is its primary
// one.
type Task string

// The tasks a registry entry can list.
const (
	TaskChat      Task = "chat"
	TaskReasoning Task = "reasoning"
	TaskCode      Task = "code"
	TaskVision    Task = "vision"
	TaskEmbedding Task = "embedding"
	TaskRerank    Task = "rerank"
	TaskSpeech    Task = "speech-to-text"
)

// Engine is the inference server that runs a quantization.
type Engine string

// The engines Loomplane deploys models with.
const (
	EngineVLLM     Engine = "vllm"
	EngineTEI      Engine = "tei"
	EngineLlamaCPP Engine = "llamacpp"
)

// ErrInvalid marks a registry file or entry that breaks a rule.
var ErrInvalid = errors.New("invalid registry entry")

// License is the license an entry's weights ship under.
type License struct {
	ID         string `yaml:"id" json:"id"`                 // one of Licenses()
	Commercial bool   `yaml:"commercial" json:"commercial"` // false: research or non-production only
}

// LicenseInfo describes one license the registry knows.
type LicenseInfo struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	Commercial bool   `json:"commercial"`
	Note       string `json:"note,omitempty"`
}

// Each Llama and Qwen license caps commercial use by monthly active users.
const (
	llamaNote = "Free for commercial use under 700 million monthly users"
	qwenNote  = "Free for commercial use under 100 million monthly users"
)

var licenses = []LicenseInfo{
	{ID: "apache-2.0", Title: "Apache License 2.0", URL: "https://www.apache.org/licenses/LICENSE-2.0", Commercial: true},
	{ID: "mit", Title: "MIT License", URL: "https://opensource.org/license/mit", Commercial: true},
	{ID: "llama3.1", Title: "Llama 3.1 Community License", URL: "https://www.llama.com/llama3_1/license/", Commercial: true, Note: llamaNote},
	{ID: "llama3.2", Title: "Llama 3.2 Community License", URL: "https://www.llama.com/llama3_2/license/", Commercial: true, Note: llamaNote},
	{ID: "llama3.3", Title: "Llama 3.3 Community License", URL: "https://www.llama.com/llama3_3/license/", Commercial: true, Note: llamaNote},
	{ID: "llama4", Title: "Llama 4 Community License", URL: "https://www.llama.com/llama4/license/", Commercial: true, Note: llamaNote},
	{ID: "gemma", Title: "Gemma Terms of Use", URL: "https://ai.google.dev/gemma/terms", Commercial: true, Note: "Allowed under Google's Gemma terms and prohibited-use policy"},
	{ID: "qwen", Title: "Qwen License", URL: "https://huggingface.co/Qwen/Qwen2.5-72B-Instruct/blob/main/LICENSE", Commercial: true, Note: qwenNote},
	{ID: "qwen-research", Title: "Qwen Research License", URL: "https://huggingface.co/Qwen/Qwen2.5-3B-Instruct/blob/main/LICENSE", Commercial: false, Note: "Research and evaluation only"},
	{ID: "deepseek", Title: "DeepSeek Model License", URL: "https://github.com/deepseek-ai/DeepSeek-LLM/blob/HEAD/LICENSE-MODEL", Commercial: true},
	{ID: "mistral-mrl", Title: "Mistral AI Research License", URL: "https://mistral.ai/licenses/MRL-0.1.md", Commercial: false, Note: "Research use only"},
	{ID: "mistral-mnpl", Title: "Mistral AI Non-Production License", URL: "https://mistral.ai/licenses/MNPL-0.1.md", Commercial: false, Note: "Non-production use only"},
}

// Licenses returns every license an entry may use, in a fixed order.
func Licenses() []LicenseInfo {
	out := make([]LicenseInfo, len(licenses))
	copy(out, licenses)
	return out
}

func licenseInfo(id string) (LicenseInfo, bool) {
	for _, l := range licenses {
		if l.ID == id {
			return l, true
		}
	}
	return LicenseInfo{}, false
}

// Architecture holds the config.json facts the memory math needs.
type Architecture struct {
	Name            string `yaml:"name" json:"name"` // config architectures[0]
	Layers          int    `yaml:"layers" json:"layers"`
	AttentionLayers int    `yaml:"attention_layers,omitempty" json:"attention_layers,omitempty"` // hybrid models; 0 means Layers
	AttentionHeads  int    `yaml:"attention_heads" json:"attention_heads"`
	KVHeads         int    `yaml:"kv_heads" json:"kv_heads"`
	HeadDim         int    `yaml:"head_dim" json:"head_dim"`
	KVLoraRank      int    `yaml:"kv_lora_rank,omitempty" json:"kv_lora_rank,omitempty"`
	RopeHeadDim     int    `yaml:"rope_head_dim,omitempty" json:"rope_head_dim,omitempty"`
}

// Embedding describes what an embedding model returns and how to prompt it.
type Embedding struct {
	Dimensions     int    `yaml:"dimensions" json:"dimensions"`
	MaxInputTokens int    `yaml:"max_input_tokens" json:"max_input_tokens"`
	QueryPrefix    string `yaml:"query_prefix,omitempty" json:"query_prefix,omitempty"`
	DocumentPrefix string `yaml:"document_prefix,omitempty" json:"document_prefix,omitempty"`
}

// Quantization is one way to run an entry: a weight format on one engine.
type Quantization struct {
	ID              string   `yaml:"id" json:"id"` // bf16, fp8, awq-int4, mxfp4, native, gguf-q4_0, gguf-q4_k_m, gguf-q8_0, gguf-mxfp4
	Engine          Engine   `yaml:"engine" json:"engine"`
	Repo            string   `yaml:"repo,omitempty" json:"repo,omitempty"` // weights elsewhere; Revision required with it
	Revision        string   `yaml:"revision,omitempty" json:"revision,omitempty"`
	File            string   `yaml:"file,omitempty" json:"file,omitempty"` // GGUF only
	WeightBytes     int64    `yaml:"weight_bytes" json:"weight_bytes"`
	Args            []string `yaml:"args,omitempty" json:"args,omitempty"` // extra engine flags, "--flag" or "--flag=value"
	MinGPUMemoryGiB int      `yaml:"min_gpu_memory_gib,omitempty" json:"min_gpu_memory_gib,omitempty"`
	MinMemoryGiB    int      `yaml:"min_memory_gib,omitempty" json:"min_memory_gib,omitempty"` // CPU engines
}

// WeightsRepo returns where q's weights live: q's own repo and revision when
// it has one, else the entry's.
func (q Quantization) WeightsRepo(e Entry) (repo, revision string) {
	if q.Repo != "" {
		return q.Repo, q.Revision
	}
	return e.ID, e.Revision
}

// Entry is one model in the registry.
type Entry struct {
	ID               string         `yaml:"id" json:"id"`    // Hugging Face repo; the served model name
	Family           string         `yaml:"-" json:"family"` // file name, set by the loader
	Title            string         `yaml:"title" json:"title"`
	Publisher        string         `yaml:"publisher" json:"publisher"`
	Summary          string         `yaml:"summary" json:"summary"` // one sentence, no em dash
	Revision         string         `yaml:"revision" json:"revision"`
	License          License        `yaml:"license" json:"license"`
	Gated            bool           `yaml:"gated" json:"gated"`
	Tasks            []Task         `yaml:"tasks" json:"tasks"` // Tasks[0] is the primary task
	Parameters       int64          `yaml:"parameters" json:"parameters"`
	ActiveParameters int64          `yaml:"active_parameters,omitempty" json:"active_parameters,omitempty"`
	ContextLength    int            `yaml:"context_length" json:"context_length"`
	Architecture     Architecture   `yaml:"architecture" json:"architecture"`
	Embedding        *Embedding     `yaml:"embedding,omitempty" json:"embedding,omitempty"`
	ToolParser       string         `yaml:"tool_parser,omitempty" json:"tool_parser,omitempty"`
	ReasoningParser  string         `yaml:"reasoning_parser,omitempty" json:"reasoning_parser,omitempty"`
	TrustRemoteCode  bool           `yaml:"trust_remote_code,omitempty" json:"trust_remote_code,omitempty"`
	Core             bool           `yaml:"core,omitempty" json:"core"`
	Quantizations    []Quantization `yaml:"quantizations" json:"quantizations"` // best quality first
}

// Quantization returns the entry's quantization with that ID.
func (e Entry) Quantization(id string) (Quantization, bool) {
	for _, q := range e.Quantizations {
		if q.ID == id {
			return q, true
		}
	}
	return Quantization{}, false
}

// clone returns a copy of e that shares no slices or pointers with it, so a
// caller can't change the registry through an entry it was handed.
func (e Entry) clone() Entry {
	e.Tasks = append([]Task(nil), e.Tasks...)
	if e.Embedding != nil {
		emb := *e.Embedding
		e.Embedding = &emb
	}
	qs := make([]Quantization, len(e.Quantizations))
	for i, q := range e.Quantizations {
		q.Args = append([]string(nil), q.Args...)
		qs[i] = q
	}
	e.Quantizations = qs
	return e
}

// File is one family's YAML file.
type File struct {
	Family  string  `yaml:"family"` // must equal the file name without .yaml
	Entries []Entry `yaml:"entries"`
}
