package agentsettings

import (
	"context"
	"errors"
	"fmt"

	"github.com/xraph/nexus/pipeline"
	"github.com/xraph/nexus/provider"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/metering"
)

// Credentials resolves the key and base URL each of the agent's nexus
// calls is sent with, from the call's tenant: the workspace's own provider
// key, or the server's.
//
// provider.ErrCredentialNotFound serves the call on the server's key, so
// it's answered only when the server's key is the right one: a call with
// no tenant, an embeddings call, or a workspace on the server's key. Every
// other failure refuses the call, so a workspace never runs on the
// server's key by accident. No error carries a key.
type Credentials struct{ s *Service }

var (
	_ provider.CredentialResolver = (*Credentials)(nil)
	_ provider.CredentialProvider = (*Credentials)(nil)
)

// ResolveCredential implements provider.CredentialResolver.
func (c *Credentials) ResolveCredential(ctx context.Context, providerName string) (provider.Credential, error) {
	tenant := pipeline.TenantID(ctx)
	if tenant == "" || providerName == EmbeddingsProvider {
		return provider.Credential{}, provider.ErrCredentialNotFound
	}
	s := c.s
	st, err := s.store.ByNexusTenant(ctx, tenant)
	if errors.Is(err, ErrNotFound) {
		return provider.Credential{}, errors.New("agentsettings: refused a call for a nexus tenant no workspace has")
	}
	if err != nil {
		return provider.Credential{}, refusal("read the workspace's agent settings", err)
	}
	if !st.Enabled {
		return provider.Credential{}, fmt.Errorf("agentsettings: refused a call: %w", ErrDisabled)
	}
	if st.ProviderKey == "" {
		if providerName != s.cfg.Provider {
			return provider.Credential{}, fmt.Errorf("agentsettings: refused a call to %s: the workspace's agent runs on the server's %s key", providerName, s.cfg.Provider)
		}
		if s.cfg.APIKey == "" && providerName != config.AIProviderOpenAICompatible {
			return provider.Credential{}, fmt.Errorf("agentsettings: refused a call to %s: the server has no key for it (ai.api_key), and the workspace chose none", providerName)
		}
		// The server's key is the platform's: the edition's gate says
		// whether this workspace may use it, and the call is claimed so
		// billing can price it later.
		if err = s.canUseServerKey(ctx, st.OrgID); err != nil {
			if errors.Is(err, metering.ErrBlocked) {
				return provider.Credential{}, err
			}
			return provider.Credential{}, refusal("check whether the workspace may use the server's key", err)
		}
		requestID := pipeline.RequestID(ctx)
		if requestID == "" {
			return provider.Credential{}, errors.New("agentsettings: refused a call with no request id on the server's key")
		}
		if err = s.metering.Record(ctx, metering.Event{
			ID: "ai-request:" + requestID, OrgID: st.OrgID, Feature: FeatureAIPlatformRequests,
			ResourceRef: tenant + ":" + requestID, Quantity: 1, OccurredAt: s.now().UTC(), PlatformOwned: true,
		}); err != nil {
			return provider.Credential{}, refusal("claim the call on the server's key", err)
		}
		return provider.Credential{}, provider.ErrCredentialNotFound
	}
	k, err := s.keys.Reveal(ctx, st.OrgID, st.ProviderKey)
	if err != nil {
		return provider.Credential{}, refusal("read the agent's provider key", err)
	}
	if k.Provider != providerName {
		return provider.Credential{}, fmt.Errorf("agentsettings: refused a call to %s: the workspace's agent uses the %s key %q", providerName, k.Type, k.Name)
	}
	// The key may have changed since it was chosen. Where its host
	// resolves now is checked at dial, by ProviderTransport.
	if err = s.checkKeyStatic(k); err != nil {
		return provider.Credential{}, refusal("use the agent's provider key", err)
	}
	if k.Provider == config.AIProviderOpenAICompatible && k.BaseURL == "" {
		return provider.Credential{}, fmt.Errorf("agentsettings: refused a call: the OpenAI-compatible key %q has no base_url", k.Name)
	}
	base := k.BaseURL
	if base == "" && k.Provider == s.cfg.Provider && s.cfg.BaseURL != "" {
		// The server's provider was built with ai.base_url, which is for
		// the server's own key. A workspace key without a base URL of its
		// own goes to its provider's own API, never the operator's gateway.
		base = providerAPIs[k.Provider]
	}
	return provider.Credential{APIKey: k.APIKey, BaseURL: base}, nil
}

// providerAPIs are the providers' own APIs, as nexus's providers default
// to them. OpenAI-compatible has none: its keys always carry a base URL.
var providerAPIs = map[string]string{
	config.AIProviderAnthropic: "https://api.anthropic.com",
	config.AIProviderOpenAI:    "https://api.openai.com/v1",
}

// GetCredential implements provider.CredentialProvider, for a gateway that
// asks for the key alone. A key with its own base URL is refused there:
// it would go to the provider's configured host instead.
func (c *Credentials) GetCredential(ctx context.Context, providerName string) (string, error) {
	cred, err := c.ResolveCredential(ctx, providerName)
	if err != nil {
		return "", err
	}
	if cred.BaseURL != "" {
		return "", errors.New("agentsettings: refused a call: the workspace's key has its own base URL, which only ResolveCredential can give")
	}
	return cred.APIKey, nil
}

// refusal wraps err so that it refuses the call: it keeps err's sentinels
// for errors.Is, except ErrCredentialNotFound, which would serve the call
// on the server's key.
func refusal(what string, err error) error {
	if errors.Is(err, provider.ErrCredentialNotFound) {
		return fmt.Errorf("agentsettings: refused a call: couldn't %s", what)
	}
	return fmt.Errorf("agentsettings: refused a call: couldn't %s: %w", what, err)
}
