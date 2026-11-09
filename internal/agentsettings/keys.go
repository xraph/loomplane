package agentsettings

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/xraph/loomplane/internal/aisettings"
)

// KeySource is where the agent's provider keys come from.
// *aisettings.Service is the real one: the agent keeps no key of its own.
type KeySource interface {
	// KeyInfo returns a key's metadata without its secrets.
	KeyInfo(ctx context.Context, orgID, name string) (*aisettings.ProviderKey, error)
	// RevealKey returns a key with its values.
	RevealKey(ctx context.Context, orgID, name string) (*aisettings.GatewayKey, error)
}

// Key is a workspace provider key as the agent sees it. APIKey is set only
// by Keys.Reveal. Key prints and logs without it.
type Key struct {
	ID       string
	Name     string
	Type     string // the aisettings type: anthropic, openai, opencompat, ...
	Provider string // the nexus provider, or "" when the agent can't use the type
	BaseURL  string
	Hint     string
	Projects []string // empty: every project
	APIKey   string
}

// String implements fmt.Stringer, without the key.
func (k Key) String() string {
	return fmt.Sprintf("{Name:%s Type:%s Provider:%s Hint:%s Projects:%v APIKey:%s}",
		k.Name, k.Type, k.Provider, k.Hint, k.Projects, redact(k.APIKey))
}

// GoString implements fmt.GoStringer, without the key.
func (k Key) GoString() string {
	return fmt.Sprintf("agentsettings.Key{Name:%q, Type:%q, Provider:%q, Hint:%q, Projects:%#v, APIKey:%q}",
		k.Name, k.Type, k.Provider, k.Hint, k.Projects, redact(k.APIKey))
}

// LogValue implements slog.LogValuer, without the key.
func (k Key) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("name", k.Name), slog.String("type", k.Type), slog.String("provider", k.Provider),
		slog.String("hint", k.Hint), slog.Any("projects", k.Projects),
	)
}

func redact(secret string) string {
	if secret == "" {
		return "none"
	}
	return "[redacted]"
}

// Keys reads the workspace's provider keys for the agent, through
// aisettings.Service.RevealKey and its metadata reader.
type Keys struct{ src KeySource }

// NewKeys returns Keys over src.
func NewKeys(src KeySource) *Keys { return &Keys{src: src} }

// Info returns the key called name without its secret, or ErrNotFound.
func (k *Keys) Info(ctx context.Context, orgID, name string) (*Key, error) {
	pk, err := k.src.KeyInfo(ctx, orgID, name)
	if err != nil {
		return nil, err
	}
	provider, _ := NexusProvider(pk.Type)
	return &Key{
		ID: pk.ID, Name: pk.Name, Type: pk.Type, Provider: provider, BaseURL: pk.Fields["base_url"],
		Hint: pk.KeyHint, Projects: slices.Clone(pk.Projects),
	}, nil
}

// unavailable is Reveal's error for a key it couldn't read. It names the
// key and nothing else, and the cause goes to the log. The error reaches
// nexus and, through cortex's publication checks, the run's error, and a
// cause such as Vault's names the secret's path: two IDs that shield's
// secret detector can take for a token, which then blocks the run.
func unavailable(ctx context.Context, orgID, name string, cause error) error {
	slog.WarnContext(ctx, "agentsettings: reading a provider key failed", "org", orgID, "key", name, "error", cause)
	return fmt.Errorf("%w: provider key %q", ErrKeyUnavailable, name)
}

// Reveal returns the key called name with its API key. Every error wraps
// ErrKeyUnavailable and none carries a value.
func (k *Keys) Reveal(ctx context.Context, orgID, name string) (*Key, error) {
	key, err := k.Info(ctx, orgID, name)
	if err != nil {
		return nil, unavailable(ctx, orgID, name, err)
	}
	g, err := k.src.RevealKey(ctx, orgID, name)
	if err != nil {
		return nil, unavailable(ctx, orgID, name, err)
	}
	if g.Type != key.Type {
		return nil, fmt.Errorf("%w: provider key %q changed while it was read", ErrKeyUnavailable, name)
	}
	key.APIKey = g.Fields["api_key"]
	key.BaseURL = g.Fields["base_url"]
	return key, nil
}
