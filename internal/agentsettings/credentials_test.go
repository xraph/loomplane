package agentsettings_test

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xraph/nexus/pipeline"
	"github.com/xraph/nexus/provider"

	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/config"
)

// on is a harness with the agent on, on the key called name ("" for the
// server's), and a context for its tenant.
func on(t *testing.T, name, model string, change ...func(*agentsettings.Deps)) (*harness, context.Context) {
	t.Helper()
	h := newHarness(t, change...)
	h.gw.priced["openai-compatible/llama"] = true
	h.budget(t, "25")
	switch name {
	case "":
	case "local":
		h.putKey(t, name, openCompatKey("https://llm.example.com/v1"))
	case "gpt":
		h.putKey(t, name, aisettings.ProviderKeyInput{Type: "openai", Fields: map[string]string{"api_key": "sk-openai-ws-5678"}})
	default:
		h.putKey(t, name, anthropicKey())
	}
	in := agentsettings.AgentUpdateInput{Enabled: ptr(true), ProviderKey: ptr(name)}
	if model != "" {
		in.Model = ptr(model)
	}
	h.mustUpdate(t, in)
	return h, pipeline.WithRequestID(pipeline.WithTenantID(context.Background(), "tenant_o"), "req_test")
}

// refused is a refusal: not ErrCredentialNotFound, which would serve the
// request on the server's key, and without the key in it.
func refused(t *testing.T, err error) {
	t.Helper()
	if err == nil || errors.Is(err, provider.ErrCredentialNotFound) {
		t.Fatalf("got %v, want a refusal", err)
	}
	for _, secret := range []string{secretValue, "oc-secret-9876", "sk-server-key-1234"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("the refusal carries a key: %v", err)
		}
	}
}

func TestCredentialsWithoutTenantUseTheServerKey(t *testing.T) {
	h, _ := on(t, "claude", "")
	_, err := h.svc.Credentials().ResolveCredential(context.Background(), "anthropic")
	if !errors.Is(err, provider.ErrCredentialNotFound) {
		t.Errorf("no tenant: %v", err)
	}
}

func TestEmbeddingsAlwaysUseTheServerKey(t *testing.T) {
	h, ctx := on(t, "claude", "")
	_, err := h.svc.Credentials().ResolveCredential(ctx, agentsettings.EmbeddingsProvider)
	if !errors.Is(err, provider.ErrCredentialNotFound) {
		t.Errorf("embeddings: %v", err)
	}
}

func TestCredentialsRefuseAnUnknownTenant(t *testing.T) {
	h, _ := on(t, "claude", "")
	_, err := h.svc.Credentials().ResolveCredential(pipeline.WithTenantID(context.Background(), "tenant_other"), "anthropic")
	refused(t, err)
}

func TestCredentialsInheritUseTheServerKey(t *testing.T) {
	h, ctx := on(t, "", "")
	if _, err := h.svc.Credentials().ResolveCredential(ctx, "anthropic"); !errors.Is(err, provider.ErrCredentialNotFound) {
		t.Errorf("inherit: %v", err)
	}
	// Another provider would bill the server for a provider it doesn't run.
	_, err := h.svc.Credentials().ResolveCredential(ctx, "openai")
	refused(t, err)
}

func TestCredentialsInheritWithoutAServerKey(t *testing.T) {
	h, ctx := on(t, "", "")
	// The operator restarts the server without its key after the agent
	// was turned on.
	h.restart(func(d *agentsettings.Deps) { d.Config.APIKey = "" })
	_, err := h.svc.Credentials().ResolveCredential(ctx, "anthropic")
	refused(t, err)
	if !strings.Contains(err.Error(), "no key") {
		t.Errorf("refusal %v", err)
	}
}

func TestCredentialsReturnTheWorkspaceKey(t *testing.T) {
	h, ctx := on(t, "local", "llama")
	c, err := h.svc.Credentials().ResolveCredential(ctx, "openai-compatible")
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKey != "oc-secret-9876" || c.BaseURL != "https://llm.example.com/v1" {
		t.Errorf("credential %v", c)
	}
	h2, ctx2 := on(t, "claude", "")
	c, err = h2.svc.Credentials().ResolveCredential(ctx2, "anthropic")
	if err != nil || c.APIKey != secretValue || c.BaseURL != "" {
		t.Errorf("anthropic credential %v %v", c, err)
	}
	key, err := h2.svc.Credentials().GetCredential(ctx2, "anthropic")
	if err != nil || key != secretValue {
		t.Errorf("GetCredential %v", err)
	}
}

// The server's ai.base_url is for the server's own key. A workspace key
// with no base_url of its own goes to its provider's own API, never to
// the operator's gateway.
func TestWorkspaceKeysSkipTheServersBaseURL(t *testing.T) {
	gateway := func(provider string) func(*agentsettings.Deps) {
		return func(d *agentsettings.Deps) {
			d.Config.Provider, d.Config.BaseURL = provider, "https://gateway.corp.example.com/v1"
		}
	}
	h, ctx := on(t, "claude", "", gateway(config.AIProviderAnthropic))
	c, err := h.svc.Credentials().ResolveCredential(ctx, "anthropic")
	if err != nil || c.APIKey != secretValue || c.BaseURL != "https://api.anthropic.com" {
		t.Fatalf("anthropic workspace key: %v %v", c, err)
	}
	h, ctx = on(t, "gpt", "gpt-x", gateway(config.AIProviderOpenAI))
	c, err = h.svc.Credentials().ResolveCredential(ctx, "openai")
	if err != nil || c.APIKey != "sk-openai-ws-5678" || c.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("openai workspace key: %v %v", c, err)
	}
	// On another provider than the server's, the provider was built with
	// its own API already.
	h, ctx = on(t, "gpt", "gpt-x", gateway(config.AIProviderAnthropic))
	if c, err = h.svc.Credentials().ResolveCredential(ctx, "openai"); err != nil || c.BaseURL != "" {
		t.Fatalf("another provider's key: %v %v", c, err)
	}
	// The server's own key still goes to ai.base_url: nexus serves it on
	// the provider as built.
	h, ctx = on(t, "", "", gateway(config.AIProviderAnthropic))
	if _, err = h.svc.Credentials().ResolveCredential(ctx, "anthropic"); !errors.Is(err, provider.ErrCredentialNotFound) {
		t.Fatalf("the server's key: %v", err)
	}
}

func TestCredentialsRefuseAnotherProvider(t *testing.T) {
	h, ctx := on(t, "claude", "")
	_, err := h.svc.Credentials().ResolveCredential(ctx, "openai")
	refused(t, err)
}

func TestCredentialsVaultErrorRefusesWithoutFallback(t *testing.T) {
	h, ctx := on(t, "claude", "")
	h.keys.SetFail(fmt.Errorf("vault: %w", context.DeadlineExceeded))
	_, err := h.svc.Credentials().ResolveCredential(ctx, "anthropic")
	refused(t, err)
	if !errors.Is(err, agentsettings.ErrKeyUnavailable) {
		t.Errorf("vault error: %v", err)
	}
	// A key that's gone is refused the same way.
	h.keys.SetFail(nil)
	h.keys.SetFail(aisettings.ErrNotFound)
	_, err = h.svc.Credentials().ResolveCredential(ctx, "anthropic")
	refused(t, err)
}

// The refusal goes to nexus and, through cortex's publication checks, to
// the run's error. Vault's cause names the secret's path, two IDs that
// shield's secret detector can read as a token, so the refusal leaves it out.
func TestCredentialsKeyErrorLeavesOutTheVaultPath(t *testing.T) {
	h, ctx := on(t, "claude", "")
	const path = "aorg_01m6qga6qjekebxjj7xd6vddcc/provider-keys/aikey_01m6qga6vbety8msvyw5x7phde"
	h.keys.SetFail(fmt.Errorf("read credential: secret: decrypt %q: crypto: ciphertext too short", path))
	_, err := h.svc.Credentials().ResolveCredential(ctx, "anthropic")
	refused(t, err)
	if !errors.Is(err, agentsettings.ErrKeyUnavailable) {
		t.Fatalf("vault error: %v", err)
	}
	if strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "decrypt") {
		t.Fatalf("the refusal carries the cause: %v", err)
	}
}

func TestCredentialsRefuseWhenTheAgentIsOff(t *testing.T) {
	h, ctx := on(t, "claude", "")
	h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(false)})
	_, err := h.svc.Credentials().ResolveCredential(ctx, "anthropic")
	refused(t, err)
	if !errors.Is(err, agentsettings.ErrDisabled) {
		t.Errorf("off: %v", err)
	}
}

// What was checked when the key was chosen is checked again on every call:
// the key changed since. Where its host resolves is the dial guard's to
// check, at dial, so a call looks nothing up here.
func TestCredentialsCheckTheKeyOnEveryCall(t *testing.T) {
	h, ctx := on(t, "local", "llama")
	var lookups atomic.Int32
	next := h.deps.Lookup
	h.restart(func(d *agentsettings.Deps) {
		d.Lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			lookups.Add(1)
			return next(ctx, host)
		}
	})
	creds := h.svc.Credentials()
	if _, err := creds.ResolveCredential(ctx, "openai-compatible"); err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != 0 {
		t.Fatalf("a call looked the host up %d times", lookups.Load())
	}

	h.store.SetKeyField(org, "local", "base_url", "http://llm.example.com/v1")
	_, err := creds.ResolveCredential(ctx, "openai-compatible")
	refused(t, err)
	h.store.SetKeyField(org, "local", "base_url", "https://user:pass@llm.example.com/v1")
	_, err = creds.ResolveCredential(ctx, "openai-compatible")
	refused(t, err)
	if strings.Contains(err.Error(), "pass") {
		t.Errorf("the refusal quotes the URL: %v", err)
	}
	// A literal address needs no lookup, and is refused here already.
	for _, u := range []string{"https://10.0.0.5/v1", "https://169.254.169.254/latest", "https://203.0.113.7/v1"} {
		h.store.SetKeyField(org, "local", "base_url", u)
		_, err = creds.ResolveCredential(ctx, "openai-compatible")
		refused(t, err)
	}
	h.store.SetKeyField(org, "local", "base_url", "https://llm.example.com/v1")

	h.store.RestrictKey(org, "local", "p1")
	_, err = creds.ResolveCredential(ctx, "openai-compatible")
	refused(t, err)
	if lookups.Load() != 0 {
		t.Fatalf("calls looked the host up %d times", lookups.Load())
	}
}
