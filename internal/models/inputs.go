package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"

	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/manifest"
)

// Inputs digests what the model kinds render into their components from
// outside the manifest: the registry's engine arguments for the entry, the
// workspace's Hugging Face token and the Hugging Face mirror. A plan
// records the digest, so a registry change to an entry's arguments, a new
// token for a gated model or a new mirror plans an update of the engine.
type Inputs struct {
	Registry *registry.Registry
	Custom   engines.CustomEntries
	Settings Tokens // *aisettings.Service
	// HuggingFaceURL is ai.models.huggingface_url, as Hooks have it.
	HuggingFaceURL string
}

// RepinnedError and GatedError are planning.InputsProblems, which
// TestDigestProblemsKeepTheirFix checks.
var _ planning.CatalogInputs = Inputs{}

// Digest implements planning.CatalogInputs for r, the resource as the
// plan's own manifest revision has it. A model's settings are the same in
// every environment it runs in, so the environment doesn't change it. The
// engine's digest covers its arguments and, for gated weights, the token's
// revision, never the token. The guard's covers its Caddyfile.
//
// The arguments are rendered for one GPU on the GPU kinds: how many GPUs
// the engine gets is part of its reviewed GPU placement, which the plan
// already compares, and an environment may give it another count than the
// manifest's.
//
// Settings the kind itself refuses (unknown, missing or malformed) get no
// digest: the planner reports each one at its path. Errors carry no secret,
// since their text becomes the plan's failure reason. A resource deployed
// before a registry re-pin fails with a RepinnedError, and a gated model
// with no workspace token with a GatedError; both say what to do.
func (i Inputs) Digest(ctx context.Context, orgID, _, _, resource string, r manifest.Resource) (map[string]string, error) {
	kind, ok := recipes.Default().Kind(r.Kind)
	if !ok || !recipes.IsModelKind(kind.Name) {
		return nil, fmt.Errorf("%s is a %s, not a model", resource, r.Kind)
	}
	settings, ok := kindSettings(kind, r.Settings)
	if !ok {
		return nil, nil
	}
	gpus := 0
	if gpuKind(kind.Name) {
		gpus = 1
	}
	spec, err := modelSpec(ctx, i.Registry, i.Custom, orgID, resource, kind.Name, settings, gpus)
	if err != nil {
		return nil, err
	}
	spec.Hub = engines.HubEndpoint(i.HuggingFaceURL)
	args, err := engines.Args(spec)
	if err != nil {
		return nil, err
	}
	var tokenRevision int64
	if spec.Entry.Gated {
		if i.Settings != nil {
			if tokenRevision, err = i.Settings.HuggingFaceTokenRevision(ctx, orgID); err != nil {
				return nil, fmt.Errorf("read which Hugging Face token %s downloads with: %w", resource, err)
			}
		}
		// Without a token the engine can't download its weights, so the
		// plan says so now rather than at deploy.
		if tokenRevision == 0 {
			return nil, &GatedError{Title: spec.Entry.Title}
		}
	}
	engine := append(argsFile(args), "hf-token-revision:"+strconv.FormatInt(tokenRevision, 10)...)
	if endpoint := engines.Endpoint(spec); len(endpoint) > 0 {
		// Only a mirror adds to the digest, so huggingface.co's stays as it was.
		engine = append(engine, "\nhf-endpoint:"+string(endpoint)...)
	}
	return map[string]string{
		recipes.ModelEngine: digest(engine),
		recipes.ModelGuard:  digest(engines.GuardConfig()),
	}, nil
}

// kindSettings is a resource's settings with the kind's defaults filled in,
// as the plan gives them to its components, and whether the kind takes
// them: every setting known, every required one set and every value
// matching its pattern.
func kindSettings(kind recipes.Kind, settings map[string]string) (map[string]string, bool) {
	for name := range settings {
		if !slices.ContainsFunc(kind.Settings, func(s recipes.Setting) bool { return s.Name == name }) {
			return nil, false
		}
	}
	out := make(map[string]string, len(kind.Settings))
	for _, s := range kind.Settings {
		v := settings[s.Name]
		switch {
		case v == "" && s.Default != "":
			out[s.Name] = s.Default
		case v == "" && s.Required:
			return nil, false
		case v == "":
		case !s.Matches(v):
			return nil, false
		default:
			out[s.Name] = v
		}
	}
	return out, true
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
