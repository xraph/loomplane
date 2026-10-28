package engines

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/models/registry"
)

// Spec is one model deployment as an engine runs it.
type Spec struct {
	Entry        registry.Entry
	Quantization registry.Quantization
	ServedName   string
	MaxModelLen  int
	GPUs         int  // 0 on CPU
	OnGPU        bool // llama.cpp only: true for model-llamacpp-gpu
}

// CustomEntries finds the entries a workspace built from a pasted Hugging
// Face repo. Their IDs start with "hf:".
type CustomEntries interface {
	Custom(ctx context.Context, orgID, entryID string) (registry.Entry, error)
}

// customPrefix starts the ID of an entry that isn't in the registry.
const customPrefix = "hf:"

// CustomEntryID is the ID of an entry built from a pasted Hugging Face
// repo: "hf:<repo>@<first 12 hex of revision>". The model kinds' entry
// setting takes exactly this form.
func CustomEntryID(repo, revision string) string {
	return customPrefix + repo + "@" + revision[:min(12, len(revision))]
}

// The engine listens here, on the environment network only. The guard is
// the one that's routed.
const (
	engineHost = "0.0.0.0"
	enginePort = "8000"
	cacheDir   = "/cache"
)

// poolingContextCap is the longest input vLLM is told to take for a pooling
// model, whatever context the model claims.
const poolingContextCap = 8192

// minCacheMB is the smallest weight cache, the size the model kinds start
// with on Kubernetes.
const minCacheMB = 20480

// ContextWindow is the context window Loomplane advertises for the model
// (TEI may serve more for some entries): MaxModelLen,
// except for a pooling model (embedding or rerank only), which is served at
// min(its context, 8192) whatever MaxModelLen says. The resource's
// max_model_len setting keeps the value that was asked for, so read the
// window from here, never from the setting.
func (s Spec) ContextWindow() int {
	if poolingOnly(s.Entry.Tasks) {
		return min(s.Entry.ContextLength, poolingContextCap)
	}
	return s.MaxModelLen
}

// Args returns the engine's arguments, one per line of engine.args, after
// the binary. Every argument is checked: none holds a line break, since
// the wrapper reads one argument per line, and every flag is one the pinned
// release accepts.
func Args(s Spec) ([]string, error) {
	var (
		args       []string
		positional int // leading arguments that aren't flags
		err        error
	)
	switch s.Quantization.Engine {
	case registry.EngineVLLM:
		args, err = vllmArgs(s)
		positional = 1
	case registry.EngineTEI:
		args = teiArgs(s)
	case registry.EngineLlamaCPP:
		args = llamaCPPArgs(s)
	default:
		return nil, fmt.Errorf("%s %s runs on engine %q, which Loomplane doesn't ship", s.Entry.ID, s.Quantization.ID, s.Quantization.Engine)
	}
	if err != nil {
		return nil, err
	}
	if err := checkTrust(s.Entry, s.Quantization); err != nil {
		return nil, err
	}
	if err := checkArgs(s.Quantization.Engine, args, positional); err != nil {
		return nil, fmt.Errorf("%s %s: %w", s.Entry.ID, s.Quantization.ID, err)
	}
	return args, nil
}

func vllmArgs(s Spec) ([]string, error) {
	if s.GPUs < 1 {
		return nil, fmt.Errorf("%s %s runs on vLLM, which needs at least one GPU", s.Entry.ID, s.Quantization.ID)
	}
	e, q := s.Entry, s.Quantization
	repo, revision := weightsRepo(e, q)
	pooling := poolingOnly(e.Tasks)
	maxLen := s.ContextWindow()
	args := []string{
		repo,
		"--revision=" + revision,
		"--tokenizer-revision=" + revision,
		"--served-model-name=" + s.ServedName,
		"--host=" + engineHost,
		"--port=" + enginePort,
		"--max-model-len=" + strconv.Itoa(maxLen),
		"--gpu-memory-utilization=0.90",
		"--tensor-parallel-size=" + strconv.Itoa(s.GPUs),
	}
	if e.TrustRemoteCode {
		args = append(args, trustRemoteCode)
	}
	if !pooling && e.ToolParser != "" {
		args = append(args, "--enable-auto-tool-choice", "--tool-call-parser="+e.ToolParser)
	}
	if !pooling && e.ReasoningParser != "" {
		args = append(args, "--reasoning-parser="+e.ReasoningParser)
	}
	return append(args, q.Args...), nil
}

func teiArgs(s Spec) []string {
	repo, revision := weightsRepo(s.Entry, s.Quantization)
	return append([]string{
		"--model-id=" + repo,
		"--revision=" + revision,
		"--served-model-name=" + s.ServedName,
		"--hostname=" + engineHost,
		"--port=" + enginePort,
		"--huggingface-hub-cache=" + cacheDir,
		"--max-client-batch-size=64",
		"--auto-truncate",
	}, s.Quantization.Args...)
}

func llamaCPPArgs(s Spec) []string {
	q := s.Quantization
	repo, revision := weightsRepo(s.Entry, q)
	layers := "0"
	if s.OnGPU {
		layers = "999"
	}
	return append([]string{
		"--model-url=https://huggingface.co/" + repo + "/resolve/" + revision + "/" + q.File,
		// The revision in the file name keeps a re-pinned file from
		// reusing the old download.
		"--model=" + cacheDir + "/" + revision[:min(12, len(revision))] + "-" + q.File,
		"--alias=" + s.ServedName,
		"--host=" + engineHost,
		"--port=" + enginePort,
		"--ctx-size=" + strconv.Itoa(s.MaxModelLen),
		"--jinja",
		"--n-gpu-layers=" + layers,
	}, q.Args...)
}

// trustRemoteCode is the engine flag that runs Python shipped in a model
// repo. Only a curated registry entry can ask for it, through
// trust_remote_code, which is reviewed with the revision it pins.
const trustRemoteCode = "--trust-remote-code"

// checkTrust refuses remote code that didn't come from the registry: a
// custom entry asking for it, or the flag among a quantization's extra
// arguments, where it would skip the trust_remote_code review.
func checkTrust(e registry.Entry, q registry.Quantization) error {
	if e.TrustRemoteCode && strings.HasPrefix(e.ID, customPrefix) {
		return fmt.Errorf("%s is a pasted Hugging Face repo, and only registry entries can run the repo's own code", e.ID)
	}
	for _, arg := range q.Args {
		if flag, _, _ := strings.Cut(arg, "="); flag == trustRemoteCode {
			return fmt.Errorf("%s %s: %s can't come through extra arguments; only a registry entry's trust_remote_code sets it", e.ID, q.ID, trustRemoteCode)
		}
	}
	return nil
}

// checkArgs refuses an argument with a line break, which would split into
// two in engine.args, and a flag the pinned release of e doesn't take. The
// first positional arguments aren't flags.
func checkArgs(e registry.Engine, args []string, positional int) error {
	for i, arg := range args {
		if strings.ContainsAny(arg, "\r\n") {
			return fmt.Errorf("argument %q has a line break", arg)
		}
		if i < positional {
			if arg == "" || strings.HasPrefix(arg, "-") {
				return fmt.Errorf("argument %q should name the model", arg)
			}
			continue
		}
		flag, _, _ := strings.Cut(arg, "=")
		if !KnownFlag(e, flag) {
			return fmt.Errorf("%s %s doesn't take %s", e, Current()[e], flag)
		}
	}
	return nil
}

// poolingOnly reports whether every task is embedding or rerank, so the
// model returns vectors or scores and never generates text.
func poolingOnly(tasks []registry.Task) bool {
	for _, t := range tasks {
		if t != registry.TaskEmbedding && t != registry.TaskRerank {
			return false
		}
	}
	return len(tasks) > 0
}

// guardConfig checks the bearer token against LOOMPLANE_MODEL_KEY and
// proxies to the engine. /health is open so probes need no key. Caddy's
// admin endpoint is off and there's no log directive, so nothing ever
// writes a request's Authorization header out.
const guardConfig = `{
	admin off
	auto_https off
}
:8080 {
	@health path /health
	handle @health {
		reverse_proxy {$LOOMPLANE_ENGINE_ADDR}
	}
	@key header Authorization "Bearer {$LOOMPLANE_MODEL_KEY}"
	handle @key {
		reverse_proxy {$LOOMPLANE_ENGINE_ADDR} {
			flush_interval -1
		}
	}
	handle {
		respond "Send the model's API key as a bearer token." 401
	}
}
`

// GuardConfig is the Caddyfile every guard gets; it holds no secret. Caddy
// reads the key and the engine's host:port from the environment.
func GuardConfig() []byte { return []byte(guardConfig) }

// Settings returns the kind settings for s (entry, model, revision,
// quantization, served_name, max_model_len, file, cache_mb). model and
// revision are where the weights come from, which for some quantizations is
// another repo than the entry's.
func Settings(s Spec) map[string]string {
	repo, revision := weightsRepo(s.Entry, s.Quantization)
	out := map[string]string{
		"entry":         s.Entry.ID,
		"model":         repo,
		"revision":      revision,
		"quantization":  s.Quantization.ID,
		"served_name":   s.ServedName,
		"max_model_len": strconv.Itoa(s.MaxModelLen),
		"cache_mb":      strconv.FormatInt(cacheMB(s.Quantization.WeightBytes), 10),
	}
	if s.Quantization.File != "" {
		out["file"] = s.Quantization.File
	}
	return out
}

// cacheMB sizes a weight cache: 1.25 times the weights, rounded up to a
// whole GiB, so the tokenizer and config files fit beside the download.
// It's never less than minCacheMB.
func cacheMB(weightBytes int64) int64 {
	const gib = int64(1) << 30
	// ceil(1.25 x w / 2^30) == ceil(5w / 2^32)
	gibs := (5*weightBytes + 4*gib - 1) / (4 * gib)
	return max(minCacheMB, gibs*1024)
}

// FromSettings rebuilds the Spec from a resource's settings and the
// registry, or orgID's custom entry for an "hf:" ID, for hooks and digests.
// The model, revision and file settings must be the ones the entry pins
// for the quantization: its args and trust_remote_code were checked
// against those weights, not whatever a manifest names.
func FromSettings(ctx context.Context, r *registry.Registry, custom CustomEntries, orgID string, settings map[string]string, gpus int, onGPU bool) (Spec, error) {
	id := settings["entry"]
	if id == "" {
		return Spec{}, errors.New("the model has no entry setting")
	}
	var e registry.Entry
	if strings.HasPrefix(id, customPrefix) {
		if custom == nil {
			return Spec{}, fmt.Errorf("%s is a custom entry, and custom entries aren't available here", id)
		}
		ce, err := custom.Custom(ctx, orgID, id)
		if err != nil {
			return Spec{}, fmt.Errorf("custom entry %s: %w", id, err)
		}
		if ce.ID != id {
			return Spec{}, fmt.Errorf("custom entry %s came back as %s", id, ce.ID)
		}
		if ce.TrustRemoteCode {
			return Spec{}, fmt.Errorf("%s is a pasted Hugging Face repo, and only registry entries can run the repo's own code", id)
		}
		e = ce
	} else {
		re, ok := r.Entry(id)
		if !ok {
			return Spec{}, fmt.Errorf("the model registry has no entry %s", id)
		}
		e = re
	}
	q, ok := e.Quantization(settings["quantization"])
	if !ok {
		return Spec{}, fmt.Errorf("%s has no quantization %q", id, settings["quantization"])
	}
	if onGPU && q.Engine != registry.EngineLlamaCPP {
		return Spec{}, fmt.Errorf("%s %s runs on %s, not llama.cpp on a GPU", id, q.ID, q.Engine)
	}
	repo, revision := weightsRepo(e, q)
	if strings.HasPrefix(id, customPrefix) {
		if err := checkCustomPin(id, e, repo, revision); err != nil {
			return Spec{}, err
		}
	}
	if settings["model"] != repo || settings["revision"] != revision || settings["file"] != q.File {
		return Spec{}, fmt.Errorf("%s %s is pinned to %s, but the resource asks for %s",
			id, q.ID, weights(repo, revision, q.File), weights(settings["model"], settings["revision"], settings["file"]))
	}
	served := settings["served_name"]
	if served == "" {
		return Spec{}, fmt.Errorf("%s has no served_name setting", id)
	}
	maxLen, err := strconv.Atoi(settings["max_model_len"])
	if err != nil || maxLen <= 0 {
		return Spec{}, fmt.Errorf("%s: max_model_len %q isn't a positive number", id, settings["max_model_len"])
	}
	if !poolingOnly(e.Tasks) && maxLen > e.ContextLength {
		return Spec{}, fmt.Errorf("%s: max_model_len %d is longer than the model's context of %d tokens", id, maxLen, e.ContextLength)
	}
	return Spec{Entry: e, Quantization: q, ServedName: served, MaxModelLen: maxLen, GPUs: gpus, OnGPU: onGPU}, nil
}

// checkCustomPin checks that a custom entry's ID, "hf:<repo>@<sha12>",
// names the weights the entry downloads: the same repo, and a revision
// starting with the same 12 hex, both the entry's and the weights'.
func checkCustomPin(id string, e registry.Entry, repo, revision string) error {
	rest := strings.TrimPrefix(id, customPrefix)
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return fmt.Errorf("custom entry %s has no @<revision> in its ID", id)
	}
	idRepo, sha12 := rest[:at], rest[at+1:]
	if len(sha12) != 12 || len(e.Revision) < 12 || e.Revision[:12] != sha12 || len(revision) < 12 || revision[:12] != sha12 {
		return fmt.Errorf("custom entry %s is pinned to revision %s, not the one its ID names", id, e.Revision)
	}
	if idRepo != repo {
		return fmt.Errorf("custom entry %s downloads its weights from %s, not the repo its ID names", id, repo)
	}
	return nil
}

// weightsRepo returns where q's weights live, as registry.WeightsRepo
// does, except that a custom entry's ID stands for its repo: "hf:" and the
// "@<sha12>" come off.
func weightsRepo(e registry.Entry, q registry.Quantization) (repo, revision string) {
	repo, revision = q.WeightsRepo(e)
	if rest, ok := strings.CutPrefix(repo, customPrefix); ok {
		repo, _, _ = strings.Cut(rest, "@")
	}
	return repo, revision
}

// weights names a download as repo@revision, with the file for GGUF.
func weights(repo, revision, file string) string {
	if file != "" {
		return repo + "@" + revision + "/" + file
	}
	return repo + "@" + revision
}
