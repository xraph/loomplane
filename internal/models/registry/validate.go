package registry

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	idPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}/[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)
	revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// trustRemoteCodeAllowed lists the entries that may run code from their
// Hugging Face repo. Each one was read before it went on the list.
var trustRemoteCodeAllowed = map[string]bool{
	"microsoft/Phi-3.5-vision-instruct": true,
}

// quantizationIDs lists every quantization ID an entry may use.
var quantizationIDs = map[string]bool{
	"bf16": true, "fp8": true, "awq-int4": true, "mxfp4": true, "native": true,
	"gguf-q4_0": true, "gguf-q4_k_m": true, "gguf-q8_0": true, "gguf-mxfp4": true,
}

var knownTasks = map[Task]bool{
	TaskChat: true, TaskReasoning: true, TaskCode: true, TaskVision: true,
	TaskEmbedding: true, TaskRerank: true, TaskSpeech: true,
}

// engineTasks lists the tasks each engine can serve.
var engineTasks = map[Engine]map[Task]bool{
	EngineVLLM: {
		TaskChat: true, TaskReasoning: true, TaskCode: true, TaskVision: true,
		TaskEmbedding: true, TaskSpeech: true,
	},
	EngineTEI:      {TaskEmbedding: true, TaskRerank: true},
	EngineLlamaCPP: {TaskChat: true, TaskReasoning: true, TaskCode: true},
}

// engineFor returns the engine a quantization ID runs on.
func engineFor(id string) Engine {
	switch {
	case id == "native":
		return EngineTEI
	case strings.HasPrefix(id, "gguf-"):
		return EngineLlamaCPP
	default:
		return EngineVLLM
	}
}

// invalidError is one problem with a registry file or entry. It matches
// ErrInvalid, and the decode or validation error under it when there is one.
type invalidError struct {
	msg   string
	cause error
}

func (e *invalidError) Error() string { return e.msg }

func (e *invalidError) Unwrap() []error {
	if e.cause == nil {
		return []error{ErrInvalid}
	}
	return []error{ErrInvalid, e.cause}
}

func invalidf(format string, args ...any) error {
	return &invalidError{msg: fmt.Sprintf(format, args...)}
}

// problems collects an entry's problems, each starting with its ID.
type problems struct {
	id   string
	errs []error
}

func (p *problems) add(format string, args ...any) {
	p.errs = append(p.errs, invalidf("%s: %s", p.id, fmt.Sprintf(format, args...)))
}

// Validate checks one entry against the registry's rules and returns every
// problem it finds, joined, each starting with the entry ID. Uniqueness
// across files is Load's job.
func Validate(e Entry) error {
	p := &problems{id: e.ID}

	if !idPattern.MatchString(e.ID) {
		p.add("id %q isn't a Hugging Face repo (owner/name)", e.ID)
	}
	if !revisionPattern.MatchString(e.Revision) {
		p.add("revision %q isn't a 40-character commit SHA", e.Revision)
	}
	validateText(p, e)
	validateLicense(p, e.License)
	validateTasks(p, e.Tasks)
	if e.Parameters <= 0 {
		p.add("parameters must be positive")
	}
	if e.ActiveParameters < 0 || e.ActiveParameters > e.Parameters {
		p.add("active_parameters must be between 0 and parameters")
	}
	if e.ContextLength <= 0 {
		p.add("context_length must be positive")
	}
	validateArchitecture(p, e.Architecture, poolingOnly(e.Tasks))
	validateEmbedding(p, e)
	if e.TrustRemoteCode && !trustRemoteCodeAllowed[e.ID] {
		p.add("trust_remote_code is only allowed on reviewed entries")
	}
	validateQuantizations(p, e)

	return errors.Join(p.errs...)
}

func validateText(p *problems, e Entry) {
	for _, field := range []struct{ name, value string }{
		{"title", e.Title}, {"publisher", e.Publisher}, {"summary", e.Summary},
	} {
		if strings.TrimSpace(field.value) == "" {
			p.add("%s is required", field.name)
		}
	}
	for _, field := range []struct{ name, value string }{{"title", e.Title}, {"summary", e.Summary}} {
		if strings.ContainsAny(field.value, "—–") {
			p.add("%s has an em or en dash; use a comma, colon or full stop", field.name)
		}
	}
}

func validateLicense(p *problems, l License) {
	info, ok := licenseInfo(l.ID)
	if !ok {
		p.add("license %q isn't one of the registry's licenses", l.ID)
		return
	}
	if l.Commercial != info.Commercial {
		p.add("license %s has commercial %v, want %v", l.ID, l.Commercial, info.Commercial)
	}
}

func validateTasks(p *problems, tasks []Task) {
	if len(tasks) == 0 {
		p.add("needs at least one task")
	}
	seen := map[Task]bool{}
	for _, t := range tasks {
		switch {
		case !knownTasks[t]:
			p.add("unknown task %q", t)
		case seen[t]:
			p.add("task %q is listed twice", t)
		}
		seen[t] = true
	}
}

// poolingOnly reports whether every task is embedding or rerank. Pooling
// models keep no KV cache, so their KV head count doesn't matter.
func poolingOnly(tasks []Task) bool {
	for _, t := range tasks {
		if t != TaskEmbedding && t != TaskRerank {
			return false
		}
	}
	return len(tasks) > 0
}

func validateArchitecture(p *problems, a Architecture, pooling bool) {
	if a.Name == "" {
		p.add("architecture name is required")
	}
	if a.Layers <= 0 {
		p.add("architecture layers must be positive")
	}
	if a.AttentionLayers < 0 || a.AttentionLayers > a.Layers {
		p.add("architecture attention_layers must be between 0 and layers")
	}
	if a.AttentionHeads <= 0 {
		p.add("architecture attention_heads must be positive")
	}
	if a.HeadDim <= 0 {
		p.add("architecture head_dim must be positive")
	}
	switch {
	case a.KVHeads > a.AttentionHeads:
		p.add("architecture kv_heads %d is more than attention_heads %d", a.KVHeads, a.AttentionHeads)
	case a.KVHeads < 0 || (a.KVHeads == 0 && !pooling):
		p.add("architecture kv_heads must be positive")
	}
	if a.KVLoraRank < 0 || a.RopeHeadDim < 0 {
		p.add("architecture kv_lora_rank and rope_head_dim can't be negative")
	}
	if a.KVLoraRank > 0 && a.RopeHeadDim <= 0 {
		p.add("architecture rope_head_dim must be positive when kv_lora_rank is set")
	}
}

func validateEmbedding(p *problems, e Entry) {
	embeds := false
	for _, t := range e.Tasks {
		if t == TaskEmbedding {
			embeds = true
		}
	}
	switch {
	case embeds && e.Embedding == nil:
		p.add("embedding block is required for the embedding task")
	case !embeds && e.Embedding != nil:
		p.add("embedding block is only for entries with the embedding task")
	case e.Embedding != nil:
		if e.Embedding.Dimensions <= 0 {
			p.add("embedding dimensions must be positive")
		}
		if e.Embedding.MaxInputTokens <= 0 {
			p.add("embedding max_input_tokens must be positive")
		}
	}
}

func validateQuantizations(p *problems, e Entry) {
	if len(e.Quantizations) == 0 {
		p.add("needs at least one quantization")
	}
	seen := map[string]bool{}
	for _, q := range e.Quantizations {
		if seen[q.ID] {
			p.add("duplicate quantization %q", q.ID)
			continue
		}
		seen[q.ID] = true
		validateQuantization(p, e, q)
	}
}

func validateQuantization(p *problems, e Entry, q Quantization) {
	if !quantizationIDs[q.ID] {
		p.add("unknown quantization %q", q.ID)
		return
	}
	if want := engineFor(q.ID); q.Engine != want {
		p.add("quantization %s runs on engine %s, not %q", q.ID, want, q.Engine)
	}
	if served, ok := engineTasks[q.Engine]; ok {
		for _, t := range e.Tasks {
			if knownTasks[t] && !served[t] {
				p.add("quantization %s: %s can't serve %s", q.ID, q.Engine, t)
			}
		}
	}

	gguf := strings.HasPrefix(q.ID, "gguf-")
	switch {
	case gguf && q.File == "":
		p.add("quantization %s needs the .gguf file name", q.ID)
	case gguf && !strings.HasSuffix(q.File, ".gguf"):
		p.add("quantization %s file %q doesn't end in .gguf", q.ID, q.File)
	case !gguf && q.File != "":
		p.add("quantization %s has a file, which only GGUF quantizations take", q.ID)
	}

	switch {
	case q.Repo != "":
		if !idPattern.MatchString(q.Repo) {
			p.add("quantization %s repo %q isn't a Hugging Face repo (owner/name)", q.ID, q.Repo)
		}
		if !revisionPattern.MatchString(q.Revision) {
			p.add("quantization %s revision %q isn't a 40-character commit SHA", q.ID, q.Revision)
		}
	case q.Revision != "":
		p.add("quantization %s has a revision but no repo", q.ID)
	}

	if q.WeightBytes <= 0 {
		p.add("quantization %s weight_bytes must be positive", q.ID)
	}
	if q.MinGPUMemoryGiB < 0 || q.MinMemoryGiB < 0 {
		p.add("quantization %s memory minimums can't be negative", q.ID)
	}
	for _, arg := range q.Args {
		flag, _, _ := strings.Cut(arg, "=")
		if !strings.HasPrefix(flag, "--") || len(flag) == 2 || strings.ContainsAny(flag, " \t\n") {
			p.add("quantization %s arg %q isn't \"--flag\" or \"--flag=value\"", q.ID, arg)
		}
	}
}
