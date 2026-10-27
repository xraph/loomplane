package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/manifest"
)

// ManifestReader reads one resource of a project's current manifest, or nil
// when the manifest has no such resource.
type ManifestReader interface {
	Resource(ctx context.Context, orgID, projectID, resource string) (*manifest.Resource, error)
}

// Inputs digests what the model kinds render into their components from
// outside the manifest: the registry's engine arguments for the entry and
// the workspace's Hugging Face token. A plan records the digest, so a
// registry change to an entry's arguments, or a new token for a gated
// model, plans an update of the engine.
type Inputs struct {
	Registry  *registry.Registry
	Custom    engines.CustomEntries
	Settings  Tokens // *aisettings.Service
	Manifests ManifestReader
}

var _ planning.CatalogInputs = Inputs{}

// Digest implements planning.CatalogInputs. A model's settings are the same
// in every environment it runs in, so the environment doesn't change it.
// The engine's digest covers its
// arguments and, for gated weights, the token's revision, never the token.
// The guard's covers its Caddyfile.
//
// The arguments are rendered for one GPU on the GPU kinds: how many GPUs
// the engine gets is part of its reviewed GPU placement, which the plan
// already compares, and an environment may give it another count than the
// manifest's.
//
// Errors carry no secret: their text becomes the plan's failure reason. A
// resource deployed before a registry re-pin fails here with a
// RepinnedError that says what to set.
func (i Inputs) Digest(ctx context.Context, orgID, projectID, _, resource string) (map[string]string, error) {
	if i.Manifests == nil {
		return nil, fmt.Errorf("the model inputs have no manifest reader")
	}
	r, err := i.Manifests.Resource(ctx, orgID, projectID, resource)
	if err != nil {
		return nil, fmt.Errorf("read %s from the manifest: %w", resource, err)
	}
	if r == nil {
		return nil, nil
	}
	kind, ok := recipes.Default().Kind(r.Kind)
	if !ok || !recipes.IsModelKind(kind.Name) {
		return nil, fmt.Errorf("%s is a %s, not a model", resource, r.Kind)
	}
	gpus := 0
	if gpuKind(kind.Name) {
		gpus = 1
	}
	spec, err := modelSpec(ctx, i.Registry, i.Custom, orgID, resource, kind.Name, withDefaults(kind, r.Settings), gpus)
	if err != nil {
		return nil, err
	}
	args, err := engines.Args(spec)
	if err != nil {
		return nil, err
	}
	var tokenRevision int64
	if spec.Entry.Gated && i.Settings != nil {
		if tokenRevision, err = i.Settings.HuggingFaceTokenRevision(ctx, orgID); err != nil {
			return nil, fmt.Errorf("read which Hugging Face token %s downloads with: %w", resource, err)
		}
	}
	engine := append(argsFile(args), "hf-token-revision:"+strconv.FormatInt(tokenRevision, 10)...)
	return map[string]string{
		recipes.ModelEngine: digest(engine),
		recipes.ModelGuard:  digest(engines.GuardConfig()),
	}, nil
}

// withDefaults is a resource's settings with the kind's defaults filled in,
// as the plan gives them to its components.
func withDefaults(kind recipes.Kind, settings map[string]string) map[string]string {
	out := make(map[string]string, len(kind.Settings))
	for _, s := range kind.Settings {
		if v := settings[s.Name]; v != "" {
			out[s.Name] = v
		} else if s.Default != "" {
			out[s.Name] = s.Default
		}
	}
	return out
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
