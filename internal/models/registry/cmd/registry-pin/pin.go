package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/xraph/loomplane/internal/models/hfhub"
	"github.com/xraph/loomplane/internal/models/registry"
)

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// checkedLicenses are the license tags that name exactly one registry
// license. Tags like "other" can't be checked.
var checkedLicenses = map[string]bool{
	"apache-2.0": true, "mit": true, "gemma": true,
	"llama3.1": true, "llama3.2": true, "llama3.3": true, "llama4": true,
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
	arch, contextLength, err := hfhub.ArchitectureOf(cfg)
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

// weightBytes is the size of the weights an engine loads, as
// hfhub.WeightBytes counts it.
func weightBytes(files []sibling) int64 {
	infos := make([]hfhub.FileInfo, len(files))
	for i, f := range files {
		infos[i] = hfhub.FileInfo{Name: f.Name, Size: f.size()}
	}
	return hfhub.WeightBytes(infos)
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

// asString returns v when it's a string, or "".
func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
