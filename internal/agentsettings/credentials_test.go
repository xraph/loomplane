package agentsettings_test

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"github.com/xraph/nexus/pipeline"
	"github.com/xraph/nexus/provider"

	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/aisettings"
)

// on is a harness with the agent on, on the key called name ("" for the
// server's), and a context for its tenant.
func on(t *testing.T, name, model string) (*harness, context.Context) {
	t.Helper()
	h := newHarness(t)
	h.gw.priced["openai-compatible/llama"] = true
	h.budget(t, "25")
	switch name {
	case "":
	case "local":
		h.putKey(t, name, openCompatKey("https://llm.example.com/v1"))
	default:
		h.putKey(t, name, anthropicKey())
	}
	in := agentsettings.AgentUpdateInput{Enabled: ptr(true), ProviderKey: ptr(name)}
	if model != "" {
		in.Model = ptr(model)
	}
	h.mustUpdate(t, in)
	return h, pipeline.WithTenantID(context.Background(), "tenant_o")
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
// the key changed since, or its host now resolves somewhere else.
func TestCredentialsCheckTheKeyOnEveryCall(t *testing.T) {
	h, ctx := on(t, "local", "llama")
	creds := h.svc.Credentials()
	h.hosts["llm.example.com"] = []netip.Addr{netip.MustParseAddr("10.9.8.7")}
	_, err := creds.ResolveCredential(ctx, "openai-compatible")
	refused(t, err)
	h.hosts["llm.example.com"] = []netip.Addr{netip.MustParseAddr("93.184.215.14")}
	if _, err = creds.ResolveCredential(ctx, "openai-compatible"); err != nil {
		t.Fatalf("public again: %v", err)
	}

	h.store.SetKeyField(org, "local", "base_url", "http://llm.example.com/v1")
	_, err = creds.ResolveCredential(ctx, "openai-compatible")
	refused(t, err)
	h.store.SetKeyField(org, "local", "base_url", "https://user:pass@llm.example.com/v1")
	_, err = creds.ResolveCredential(ctx, "openai-compatible")
	refused(t, err)
	if strings.Contains(err.Error(), "pass") {
		t.Errorf("the refusal quotes the URL: %v", err)
	}
	h.store.SetKeyField(org, "local", "base_url", "https://llm.example.com/v1")

	h.store.RestrictKey(org, "local", "p1")
	_, err = creds.ResolveCredential(ctx, "openai-compatible")
	refused(t, err)
}
