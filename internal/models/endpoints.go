package models

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/catalogstate"
	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

// Gateways is told when the set of models a project's gateways should
// route to may have changed. W1 sets the real one; until then it is
// noGateways{}. It returns nothing: a gateway that can't follow must never
// fail a model's deployment.
type Gateways interface {
	ModelsChanged(ctx context.Context, orgID, projectID string)
}

// noGateways is the Gateways of a Loomplane without the AI gateway.
type noGateways struct{}

func (noGateways) ModelsChanged(context.Context, string, string) {}

// Placement is where a caller of a model runs: what reach is checked
// against.
type Placement struct {
	EnvironmentID, ConnectionID, Provider string // provider "server" or "kubernetes"
	// SharedNetworks are the project networks the caller has joined
	// (Docker). Nothing fills it yet: until models share a network, a
	// caller reaches only the models of its own environment.
	SharedNetworks []string
}

// Secret is a value that's never printed, logged or encoded: String,
// GoString, MarshalJSON and LogValue all give "[redacted]". Reveal is the
// one way to read it, for the gateway that sends it.
type Secret string

const redacted = "[redacted]"

// Reveal returns the secret's value.
func (s Secret) Reveal() string { return string(s) }

// String implements fmt.Stringer.
func (Secret) String() string { return redacted }

// GoString implements fmt.GoStringer, for %#v.
func (Secret) GoString() string { return redacted }

// MarshalJSON implements json.Marshaler.
func (Secret) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// Endpoint is one deployed model as a caller at a Placement reaches it.
// It holds the model's key, so it's for the gateway's own sync only: no
// route returns it (Task 25 must not route Endpoints), and the key is a
// Secret that never prints or encodes.
type Endpoint struct {
	Deployment, Resource, EnvironmentID string
	// Provider is ProviderName(Resource).
	Provider string
	// BaseURL is the guard's URL and "/v1".
	BaseURL string
	// APIKey is the model's key; Reveal reads it.
	APIKey     Secret
	ServedName string
	Task       registry.Task
	// ContextWindow is engines.Spec.ContextWindow: for a pooling model the
	// window it's served at, not its stored max_model_len.
	ContextWindow, MaxOutput, Dimensions int
	// Alias is the entry's default alias.
	Alias string
}

// Skipped is a deployed model a caller can't reach, and why.
type Skipped struct{ Resource, EnvironmentID, Reason string }

// maxProviderName is the longest provider name a gateway takes.
const maxProviderName = 63

// ProviderName is the gateway provider name of a model resource:
// "model-" and the resource. One longer than 63 characters is cut, and
// ends with "-" and 6 hex of its hash, so two long names that share a
// prefix stay apart.
func ProviderName(resource string) string {
	name := "model-" + resource
	if len(name) <= maxProviderName {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "-" + hex.EncodeToString(sum[:])[:6]
	return name[:maxProviderName-len(suffix)] + suffix
}

// maxOutputCap is the most a model's answer is allowed, in tokens, when a
// gateway registers it.
const maxOutputCap = 8192

// Endpoints lists the project's ready models a caller at from can reach,
// with what a gateway needs to call each one, and says in Skipped why each
// other model is out. Until models are shared across environments, those
// are the models running in from.EnvironmentID on from's server or
// cluster; a model elsewhere is skipped with "runs in <environment>".
// Rerank and speech models are included; W1's adapter keeps the tasks the
// gateway routes.
//
// It's for the gateway's own background sync, as CatalogAccess's internal
// reads are: it checks no permission, and an endpoint holds the model's
// key, so no route returns it. Every read is scoped to orgID.
func (s *Service) Endpoints(ctx context.Context, orgID, projectID string, from Placement) ([]Endpoint, []Skipped, error) {
	if s.d.Deployed == nil || s.d.Store == nil {
		return nil, nil, errors.New("models: deployment facts aren't configured")
	}
	rows, err := s.d.Store.ListProject(ctx, orgID, projectID)
	if err != nil {
		return nil, nil, err
	}
	bindings, err := s.d.Deployed.Bindings(ctx, orgID, from.EnvironmentID)
	if err != nil {
		return nil, nil, err
	}
	envNames := map[string]string{}
	envName := func(id string) string {
		if name, ok := envNames[id]; ok {
			return name
		}
		name := id
		if env, eerr := s.environment(ctx, orgID, projectID, id); eerr == nil {
			name = env.Name
		}
		envNames[id] = name
		return name
	}

	type candidate struct {
		resource, deployment string
	}
	var here []candidate
	var skipped []Skipped
	seen := map[string]bool{}
	for _, d := range rows {
		switch {
		case d.EnvironmentID != from.EnvironmentID:
			skipped = append(skipped, Skipped{Resource: d.Resource, EnvironmentID: d.EnvironmentID, Reason: "runs in " + envName(d.EnvironmentID)})
		case (d.Phase == PhaseWaitingForServer || d.Phase == PhaseNeedsAttention) && (d.Request == nil || d.Request.Moving == nil):
			// A model moving onto a new server still serves where it is; a
			// new one waiting for its server doesn't serve yet.
			skipped = append(skipped, Skipped{Resource: d.Resource, EnvironmentID: d.EnvironmentID, Reason: "is waiting for its GPU server"})
			seen[d.Resource] = true
		default:
			here = append(here, candidate{resource: d.Resource, deployment: d.ID})
			seen[d.Resource] = true
		}
	}
	// A model added to the manifest by hand has no row, but it's bound.
	for _, b := range bindings {
		if b.Subject != planning.ResourceSubject || seen[b.Name] {
			continue
		}
		if cfg := resourceConfig(b); cfg != nil && cfg.Composition != nil && recipes.IsModelKind(cfg.Composition.Kind) {
			here = append(here, candidate{resource: b.Name})
			seen[b.Name] = true
		}
	}

	// Connection names are for the reasons only; IDs stand in when they
	// can't be read.
	names, nerr := s.d.Deployed.ConnectionNames(ctx, orgID)
	if nerr != nil {
		names = map[string]string{}
	}
	var out []Endpoint
	for _, c := range here {
		e, reason, eerr := s.endpoint(ctx, orgID, projectID, from, c.resource, bindings, names)
		if eerr != nil {
			return nil, nil, eerr
		}
		if reason != "" {
			skipped = append(skipped, Skipped{Resource: c.resource, EnvironmentID: from.EnvironmentID, Reason: reason})
			continue
		}
		e.Deployment = c.deployment
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b Endpoint) int { return strings.Compare(a.Resource, b.Resource) })
	slices.SortFunc(skipped, func(a, b Skipped) int {
		return cmp.Or(strings.Compare(a.Resource, b.Resource), strings.Compare(a.EnvironmentID, b.EnvironmentID))
	})
	return out, skipped, nil
}

// endpoint is one model of from's environment as a caller at from reaches
// it, or why it can't.
func (s *Service) endpoint(ctx context.Context, orgID, projectID string, from Placement, resource string, bindings []*planning.Binding,
	names map[string]string,
) (Endpoint, string, error) {
	fact, err := s.d.Deployed.State(ctx, orgID, projectID, from.EnvironmentID, resource)
	if err != nil {
		return Endpoint{}, "", err
	}
	if fact.Value != catalogstate.Ready {
		return Endpoint{}, "isn't ready: it's " + fact.Value, nil
	}
	if fact.ConnectionID != from.ConnectionID {
		name := func(id string) string { return cmp.Or(names[id], id) }
		return Endpoint{}, fmt.Sprintf("runs on %s, and a caller on %s can't reach it. Run them on the same server or cluster.",
			name(fact.ConnectionID), cmp.Or(name(from.ConnectionID), "no server or cluster")), nil
	}
	settings, gpus, kind := engineSettings(bindings, resource)
	if settings == nil {
		return Endpoint{}, "has no engine Loomplane can read", nil
	}
	spec, err := s.servedSpec(ctx, orgID, settings, gpus, kind == recipes.ModelLlamaCPPGPUKind)
	if err != nil {
		return Endpoint{}, "serves something Loomplane can't read: " + err.Error(), nil
	}
	url, err := s.d.Deployed.URL(ctx, orgID, from.EnvironmentID, resource)
	if err != nil {
		return Endpoint{}, "", err
	}
	if url == "" {
		return Endpoint{}, "has no address yet", nil
	}
	key, err := s.d.Deployed.APIKey(ctx, orgID, projectID, from.EnvironmentID, resource)
	if err != nil {
		return Endpoint{}, "", err
	}
	e := Endpoint{
		Resource: resource, EnvironmentID: from.EnvironmentID, Provider: ProviderName(resource),
		BaseURL: strings.TrimRight(url, "/") + "/v1", APIKey: Secret(key), ServedName: spec.ServedName,
		ContextWindow: spec.ContextWindow(), Alias: DefaultAlias(spec.Entry),
	}
	e.MaxOutput = min(maxOutputCap, e.ContextWindow/2)
	if len(spec.Entry.Tasks) > 0 {
		e.Task = spec.Entry.Tasks[0]
	}
	if spec.Entry.Embedding != nil {
		e.Dimensions = spec.Entry.Embedding.Dimensions
	}
	return e, "", nil
}

// resourceConfig reads a binding's resource config, nil when it has none.
func resourceConfig(b *planning.Binding) *planning.ResourceConfig {
	var cfg planning.ResourceConfig
	if b == nil || len(b.Config) == 0 || json.Unmarshal(b.Config, &cfg) != nil {
		return nil
	}
	return &cfg
}

// engineSettings are the settings, GPU count and kind the model's engine
// was deployed with, from its component's binding: what it serves now.
func engineSettings(bindings []*planning.Binding, resource string) (settings map[string]string, gpus int, kind string) {
	name := planning.ComponentActionName(resource, recipes.ModelEngine)
	for _, b := range bindings {
		if b.Subject != planning.ResourceSubject || b.Name != name {
			continue
		}
		cfg := resourceConfig(b)
		if cfg == nil || cfg.Component == nil {
			return nil, 0, ""
		}
		if cfg.Component.GPU != nil {
			gpus = cfg.Component.GPU.Count
		}
		return cfg.Component.Settings, gpus, cfg.Component.Kind
	}
	return nil, 0, ""
}

// servedSpec reads what a model's settings serve. Unlike the hooks, it
// doesn't check the pin: a model deployed before a registry re-pin serves
// its old weights until it's moved, and it's reachable all the same.
func (s *Service) servedSpec(ctx context.Context, orgID string, settings map[string]string, gpus int, onGPU bool) (engines.Spec, error) {
	v, err := s.lookup(ctx, orgID, settings["entry"])
	if err != nil {
		return engines.Spec{}, err
	}
	q, ok := v.entry.Quantization(settings["quantization"])
	if !ok {
		return engines.Spec{}, fmt.Errorf("%s has no quantization %q", v.entry.ID, settings["quantization"])
	}
	maxLen, err := strconv.Atoi(settings["max_model_len"])
	if err != nil || maxLen <= 0 {
		return engines.Spec{}, fmt.Errorf("max_model_len %q isn't a context length", settings["max_model_len"])
	}
	served := settings["served_name"]
	if served == "" {
		served = repoOf(v.entry)
	}
	return engines.Spec{Entry: v.entry, Quantization: q, ServedName: served, MaxModelLen: maxLen, GPUs: gpus, OnGPU: onGPU}, nil
}
