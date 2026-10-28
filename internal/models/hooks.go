// Package models runs the model kinds: what their components are given
// when they start, what a plan compares to decide whether they change, and
// the seam the AI gateway plugs into.
package models

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/recipes"
)

// Tokens reads the workspace's Hugging Face token. *aisettings.Service is
// the real one.
type Tokens interface {
	// HuggingFaceTokenValue returns the token and its revision, or
	// aisettings.ErrNotFound when none is set.
	HuggingFaceTokenValue(ctx context.Context, orgID string) (string, int64, error)
	// HuggingFaceTokenRevision returns the token's revision, 0 when none is
	// set, without reading the token.
	HuggingFaceTokenRevision(ctx context.Context, orgID string) (int64, error)
}

var _ Tokens = (*aisettings.Service)(nil)

// HooksDeps are what Hooks need.
type HooksDeps struct {
	Registry *registry.Registry
	// Custom finds a workspace's entries built from a pasted Hugging Face
	// repo. Nil refuses every "hf:" entry.
	Custom   engines.CustomEntries
	Settings Tokens // *aisettings.Service
	// Gateways is told when a model is ready or removed. Nil is noGateways
	// until SetGateways binds one.
	Gateways Gateways
	// HuggingFaceURL is ai.models.huggingface_url. A mirror there is where
	// the engines download weights from.
	HuggingFaceURL string
}

// Hooks are the model kinds' catalog hooks: they render the engine's
// arguments and token file and the guard's config, and tell the gateways
// when a model comes or goes.
type Hooks struct {
	registry *registry.Registry
	custom   engines.CustomEntries
	tokens   Tokens
	hub      string // engines.HubEndpoint of the config

	mu       sync.RWMutex
	gateways Gateways
}

var _ deployments.CatalogHooks = (*Hooks)(nil)

// NewHooks returns the model kinds' hooks.
func NewHooks(d HooksDeps) *Hooks {
	h := &Hooks{registry: d.Registry, custom: d.Custom, tokens: d.Settings, hub: engines.HubEndpoint(d.HuggingFaceURL)}
	h.SetGateways(d.Gateways)
	return h
}

// SetGateways binds what's told when a model is ready or removed. Nil
// unbinds it.
func (h *Hooks) SetGateways(g Gateways) {
	if g == nil {
		g = noGateways{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gateways = g
}

func (h *Hooks) gatewaysOf() Gateways {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.gateways
}

// Files implements deployments.CatalogHooks. The engine gets its arguments,
// one per line, the workspace's Hugging Face token when its weights are
// gated (an empty file when they aren't), and the Hugging Face mirror it
// downloads from (an empty file for huggingface.co). The guard gets its
// Caddyfile.
// The token goes into its own file and nowhere else: never an argument, an
// environment variable the plan records, or an error.
func (h *Hooks) Files(ctx context.Context, run deployments.ComponentRun) (map[string][]byte, error) {
	switch run.Component.Name {
	case recipes.ModelGuard:
		return map[string][]byte{recipes.ModelGuardConfig: engines.GuardConfig()}, nil
	case recipes.ModelEngine:
	default:
		return nil, fmt.Errorf("%s has no component %s", run.Kind, run.Component.Name)
	}
	gpus := 0
	if run.GPU != nil {
		gpus = run.GPU.Count
	}
	spec, err := modelSpec(ctx, h.registry, h.custom, run.OrgID, run.Resource, run.Kind, run.Settings, gpus)
	if err != nil {
		return nil, err
	}
	spec.Hub = h.hub
	args, err := engines.Args(spec)
	if err != nil {
		return nil, err
	}
	token := []byte{}
	if spec.Entry.Gated {
		if h.tokens == nil {
			return nil, &GatedError{Title: spec.Entry.Title}
		}
		value, _, terr := h.tokens.HuggingFaceTokenValue(ctx, run.OrgID)
		switch {
		case errors.Is(terr, aisettings.ErrNotFound):
			return nil, &GatedError{Title: spec.Entry.Title}
		case terr != nil:
			// The error comes from reading the value, so it could quote
			// what it read. Say what failed without it.
			return nil, fmt.Errorf("couldn't read the workspace's Hugging Face token for %s. Try again", spec.Entry.Title)
		}
		token = []byte(value)
	}
	return map[string][]byte{
		recipes.ModelArgsPath:     argsFile(args),
		recipes.ModelTokenPath:    token,
		recipes.ModelEndpointPath: engines.Endpoint(spec),
	}, nil
}

// Ready implements deployments.CatalogHooks. Once the guard is up, the
// model can take traffic, so the project's gateways are told. Telling them
// never fails the step.
func (h *Hooks) Ready(ctx context.Context, run deployments.ComponentRun) error {
	if run.Component.Name == recipes.ModelGuard {
		h.gatewaysOf().ModelsChanged(ctx, run.OrgID, run.ProjectID)
	}
	return nil
}

// Removed implements deployments.CatalogHooks: the project's gateways are
// told the model is gone. Telling them never fails the step.
func (h *Hooks) Removed(ctx context.Context, res deployments.ComposedResource) error {
	h.gatewaysOf().ModelsChanged(ctx, res.OrgID, res.ProjectID)
	return nil
}

// argsFile is the engine's arguments, one per line, with a trailing newline.
func argsFile(args []string) []byte {
	return []byte(strings.Join(args, "\n") + "\n")
}

// GatedError is a gated model with no workspace Hugging Face token to
// download it with. It says what to do, and holds nothing secret.
type GatedError struct{ Title string }

func (e *GatedError) Error() string {
	return e.Title + " is gated on Hugging Face. Add a Hugging Face token that has accepted its license in AI settings"
}

// InputsProblem implements planning.InputsProblem: a plan shows the
// sentence as it is.
func (*GatedError) InputsProblem() {}

// modelSpec rebuilds a model resource's engine Spec from its settings, and
// checks that the quantization runs on the kind's engine and, for a registry
// entry, that the settings name the weights the registry pins. gpus is how
// many GPUs the engine runs on.
func modelSpec(ctx context.Context, r *registry.Registry, custom engines.CustomEntries, orgID, resource, kind string, settings map[string]string, gpus int) (engines.Spec, error) {
	if !recipes.IsModelKind(kind) {
		return engines.Spec{}, fmt.Errorf("%s is a %s, not a model", resource, kind)
	}
	if e, q, ok := registryQuantization(r, settings); ok {
		// Checked before FromSettings, whose errors don't say what to do.
		if err := checkEngine(resource, kind, e.ID, q); err != nil {
			return engines.Spec{}, err
		}
		if err := checkPin(resource, e, q, settings); err != nil {
			return engines.Spec{}, err
		}
	}
	onGPU := kind == recipes.ModelLlamaCPPGPUKind
	spec, err := engines.FromSettings(ctx, r, custom, orgID, settings, gpus, onGPU)
	if err != nil {
		return engines.Spec{}, fmt.Errorf("%s: %w", resource, err)
	}
	if err := checkEngine(resource, kind, spec.Entry.ID, spec.Quantization); err != nil {
		return engines.Spec{}, err
	}
	return spec, nil
}

// registryQuantization finds the registry entry and quantization the
// settings name. A custom "hf:" entry isn't in the registry.
func registryQuantization(r *registry.Registry, settings map[string]string) (registry.Entry, registry.Quantization, bool) {
	id := settings["entry"]
	if r == nil || strings.HasPrefix(id, "hf:") {
		return registry.Entry{}, registry.Quantization{}, false
	}
	e, ok := r.Entry(id)
	if !ok {
		return registry.Entry{}, registry.Quantization{}, false
	}
	q, ok := e.Quantization(settings["quantization"])
	return e, q, ok
}

// gpuKind reports whether a model kind runs its engine on a GPU.
func gpuKind(kind string) bool {
	return kind == recipes.ModelVLLMKind || kind == recipes.ModelLlamaCPPGPUKind
}

// checkEngine refuses a quantization whose engine isn't the kind's: vLLM
// for model-vllm, Text Embeddings Inference for model-tei and llama.cpp for
// the two llama.cpp kinds. Otherwise the engine would be handed another
// engine's arguments.
func checkEngine(resource, kind, entryID string, q registry.Quantization) error {
	want, ok := recipes.ModelKindFor(string(q.Engine), gpuKind(kind))
	if ok && want == kind {
		return nil
	}
	msg := fmt.Sprintf("%s is a %s, and %s %s runs on %s, which %s doesn't run", resource, kind, entryID, q.ID, q.Engine, kind)
	for _, gpu := range []bool{gpuKind(kind), !gpuKind(kind)} {
		if other, found := recipes.ModelKindFor(string(q.Engine), gpu); found && other != kind {
			msg += ". Deploy it as " + other
			break
		}
	}
	return errors.New(msg)
}

// checkPin refuses a registry entry whose model, revision or file settings
// aren't the weights the registry pins, saying what the pin is now. That
// happens to every resource deployed before a registry re-pin: its settings
// still name the old weights, and the engine's arguments were only ever
// checked against the new ones. Custom entries are pinned by their ID, so
// engines.FromSettings checks those.
func checkPin(resource string, e registry.Entry, q registry.Quantization, settings map[string]string) error {
	repo, revision := q.WeightsRepo(e)
	if settings["model"] == repo && settings["revision"] == revision && settings["file"] == q.File {
		return nil
	}
	return &RepinnedError{Resource: resource, Entry: e.ID, Quantization: q.ID,
		Model: repo, Revision: revision, File: q.File,
		Asked: weights(settings["model"], settings["revision"], settings["file"])}
}

// RepinnedError is a model resource whose settings name other weights than
// the registry pins for its entry and quantization, as every resource
// deployed before a registry re-pin does. It fails the plan, and the step
// that would start the engine, until the resource's settings are moved to
// the new pin. Nothing in it is secret.
type RepinnedError struct {
	Resource, Entry, Quantization string
	// Model, Revision and File are the pin now.
	Model, Revision, File string
	// Asked is what the resource's settings name.
	Asked string
}

// InputsProblem implements planning.InputsProblem: a plan shows the
// sentence as it is.
func (*RepinnedError) InputsProblem() {}

func (e *RepinnedError) Error() string {
	set := "model: " + e.Model + ", revision: " + e.Revision
	if e.File != "" {
		set += ", file: " + e.File
	}
	return fmt.Sprintf("%s asks for %s, but the model registry now pins %s %s to %s. Set %s's settings to %s, then plan again",
		e.Resource, e.Asked, e.Entry, e.Quantization, weights(e.Model, e.Revision, e.File), e.Resource, set)
}

// weights names a download as repo@revision, with the file for GGUF.
func weights(repo, revision, file string) string {
	if file != "" {
		return repo + "@" + revision + "/" + file
	}
	return repo + "@" + revision
}
