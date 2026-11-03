package agentsettings_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/security"
)

func TestGetNeedsAIManage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, p := range []security.Principal{developer, viewer, outsider} {
		if _, err := h.svc.Get(ctx, p); !errors.Is(err, security.ErrForbidden) {
			t.Errorf("%s: %v", p.UserID, err)
		}
	}
	v, err := h.svc.Get(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if v.Enabled || v.ProviderKey != "" || v.Model != "" || v.NexusTenantID != "" {
		t.Errorf("a workspace never set up: %+v", v.Settings)
	}
	if v.Server != (agentsettings.ServerDefaults{Provider: "anthropic", Model: config.DefaultAIModel, KeySet: true}) {
		t.Errorf("server %+v", v.Server)
	}
	if v.Effective.Provider != "anthropic" || v.Effective.Model != config.DefaultAIModel || v.Effective.SummaryModel != config.SummaryAIModel {
		t.Errorf("effective %+v", v.Effective)
	}
	if v.Budget.LimitUSD != "0.00" || !v.Budget.Exhausted || !v.Available {
		t.Errorf("budget %+v, available %t", v.Budget, v.Available)
	}
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "sk-server-key-1234") {
		t.Errorf("the view carries the server key: %s", b)
	}
}

func TestEnableCreatesTenantSeedsAndAssigns(t *testing.T) {
	h := newHarness(t)
	h.budget(t, "25")
	h.gw.take()
	v := h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(true)})
	if !v.Enabled || v.NexusTenantID != "tenant_o" {
		t.Fatalf("view %+v", v.Settings)
	}
	if got := h.gw.take(); !slices.Equal(got, []string{"ensure o 25.00", "active tenant_o true"}) {
		t.Errorf("gateway %v", got)
	}
	if got := h.seed.take(); !slices.Equal(got, []string{"seed o"}) {
		t.Errorf("seeder %v", got)
	}
	if got := h.roles.take(); !slices.Equal(got, []string{"assign o"}) {
		t.Errorf("roles %v", got)
	}
	st, _ := h.store.Get(context.Background(), org)
	if st.NexusTenantID != "tenant_o" || !st.Enabled || st.UpdatedBy != admin.UserID {
		t.Errorf("stored %+v", st)
	}

	// Off, then on again: the same tenant, its quota brought up to date,
	// and the seed and role again in case a release changed them.
	h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(false)})
	h.gw.take()
	h.seed.take()
	h.roles.take()
	h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(true)})
	if got := h.gw.take(); !slices.Equal(got, []string{"budget tenant_o 25.00", "active tenant_o true"}) {
		t.Errorf("gateway on again %v", got)
	}
	if h.seed.take() == nil || !slices.Equal(h.roles.take(), []string{"assign o"}) {
		t.Error("on again didn't seed and assign")
	}
}

func TestEnableNeedsABudget(t *testing.T) {
	h := newHarness(t)
	_, err := h.update(t, agentsettings.AgentUpdateInput{Enabled: ptr(true)})
	wantInvalid(t, err, "monthly budget")
	if h.gw.take() != nil || h.roles.take() != nil {
		t.Error("a refused enable touched nexus or roles")
	}
}

func TestEnableNeedsTheGateway(t *testing.T) {
	h := newHarness(t, func(d *agentsettings.Deps) { d.Gateway, d.Seeder = nil, nil })
	h.budget(t, "25")
	v, _ := h.svc.Get(context.Background(), admin)
	if v.Available {
		t.Error("available without a gateway")
	}
	_, err := h.update(t, agentsettings.AgentUpdateInput{Enabled: ptr(true)})
	wantInvalid(t, err, "can't run the agent")
	// Bound later, it can.
	h.svc.BindGateway(h.gw, h.seed)
	h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(true)})
}

func TestEnableNeedsAServerKeyOrAProviderKey(t *testing.T) {
	h := newHarness(t, func(d *agentsettings.Deps) { d.Config.APIKey = "" })
	h.budget(t, "25")
	_, err := h.update(t, agentsettings.AgentUpdateInput{Enabled: ptr(true)})
	wantInvalid(t, err, "provider key")
	h.putKey(t, "claude", anthropicKey())
	h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(true), ProviderKey: ptr("claude")})
}

func TestDisableRevokesAndCancels(t *testing.T) {
	h := newHarness(t)
	h.budget(t, "25")
	h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(true)})
	h.gw.take()
	h.roles.take()
	v := h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(false)})
	if v.Enabled || v.NexusTenantID != "tenant_o" {
		t.Errorf("view %+v", v.Settings)
	}
	if got := h.gw.take(); !slices.Equal(got, []string{"active tenant_o false"}) {
		t.Errorf("gateway %v", got)
	}
	if got := h.roles.take(); !slices.Equal(got, []string{"revoke o"}) {
		t.Errorf("roles %v", got)
	}
	if got := h.cancel.take(); !slices.Equal(got, []string{"cancel o"}) {
		t.Errorf("canceller %v", got)
	}
}

// The agent uses Anthropic, OpenAI and OpenAI-compatible keys granted to
// every project, and no other.
func TestAgentKeyRules(t *testing.T) {
	h := newHarness(t)
	h.putKey(t, "mistral", aisettings.ProviderKeyInput{Type: "mistral", Fields: map[string]string{"api_key": "mistral-key-1234"}})
	_, err := h.update(t, agentsettings.AgentUpdateInput{ProviderKey: ptr("mistral")})
	wantInvalid(t, err, "Anthropic, OpenAI or OpenAI-compatible")

	in := anthropicKey()
	in.Projects = &[]string{"p1"}
	h.putKey(t, "scoped", in)
	_, err = h.update(t, agentsettings.AgentUpdateInput{ProviderKey: ptr("scoped")})
	wantInvalid(t, err, "every project")

	_, err = h.update(t, agentsettings.AgentUpdateInput{ProviderKey: ptr("nope")})
	wantInvalid(t, err, "no provider key")

	h.putKey(t, "claude", anthropicKey())
	v := h.mustUpdate(t, agentsettings.AgentUpdateInput{ProviderKey: ptr("claude")})
	if v.ProviderKey != "claude" || v.Key == nil || v.Key.Hint != "wxyz" || v.Key.Type != "anthropic" {
		t.Errorf("view %+v key %+v", v.Settings, v.Key)
	}
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), secretValue) {
		t.Errorf("the view carries the key: %s", b)
	}
	// Back to the server's key.
	v = h.mustUpdate(t, agentsettings.AgentUpdateInput{ProviderKey: ptr("")})
	if v.ProviderKey != "" || v.Key != nil {
		t.Errorf("server key: %+v %+v", v.Settings, v.Key)
	}
}

func TestUpdateRefusesBaseURLsOnPrivateNetworks(t *testing.T) {
	for _, c := range []struct {
		url, mention string
	}{
		{"http://llm.example.com/v1", "https"},
		{"https://10.0.0.5/v1", "private"},
		{"https://localhost/v1", "private"},
		{"https://169.254.169.254/latest", "cloud metadata"},
		{"https://[fd00::1]/v1", "private"},
		{"https://[fec0::1]/v1", "private"},
		{"https://[::ffff:10.0.0.1]/v1", "private"},
		{"https://100.64.0.1/v1", "private"},
		{"https://sneaky.example.com/v1", "private"},
		{"https://llm.corp.example.com/v1", "private"},
		{"https://mgmt.example.com/v1", "planning.endpoint_denied_cidrs"},
		{"https://203.0.113.7/v1", "planning.endpoint_denied_cidrs"},
		{"https://unknown.example.com/v1", "look up"},
	} {
		t.Run(c.url, func(t *testing.T) {
			h := newHarness(t)
			h.putKey(t, "local", openCompatKey(c.url))
			_, err := h.update(t, agentsettings.AgentUpdateInput{ProviderKey: ptr("local"), Model: ptr("llama")})
			wantInvalid(t, err, c.mention)
			if strings.Contains(err.Error(), c.url) {
				t.Errorf("the error quotes the URL: %v", err)
			}
		})
	}
	h := newHarness(t)
	h.putKey(t, "public", openCompatKey("https://llm.example.com/v1"))
	h.mustUpdate(t, agentsettings.AgentUpdateInput{ProviderKey: ptr("public"), Model: ptr("llama")})

	// Development allows http and private networks, never a denied range.
	dev := newHarness(t, func(d *agentsettings.Deps) { d.Environment = config.EnvDevelopment })
	dev.putKey(t, "ollama", openCompatKey("http://localhost:11434/v1"))
	dev.mustUpdate(t, agentsettings.AgentUpdateInput{ProviderKey: ptr("ollama"), Model: ptr("llama")})
	dev.putKey(t, "mgmt", openCompatKey("http://mgmt.example.com/v1"))
	_, err := dev.update(t, agentsettings.AgentUpdateInput{ProviderKey: ptr("mgmt")})
	wantInvalid(t, err, "planning.endpoint_denied_cidrs")
	// Nor link-local or cloud metadata addresses, in any environment.
	for _, env := range []string{config.EnvDevelopment, config.EnvStaging, config.EnvProduction} {
		for _, u := range []string{
			"https://169.254.169.254/latest", "https://169.254.10.1/v1", "https://[fe80::1]/v1",
			"https://[fd00:ec2::254]/latest", "https://100.100.100.200/latest", "https://168.63.129.16/machine",
			"https://metadata.example.com/v1",
		} {
			h := newHarness(t, func(d *agentsettings.Deps) { d.Environment = env })
			h.hosts["metadata.example.com"] = []netip.Addr{netip.MustParseAddr("169.254.169.254")}
			h.putKey(t, "meta", openCompatKey(u))
			_, err = h.update(t, agentsettings.AgentUpdateInput{ProviderKey: ptr("meta")})
			if !errors.Is(err, agentsettings.ErrInvalid) || !strings.Contains(err.Error(), "link-local or cloud metadata") {
				t.Errorf("%s in %s: %v", u, env, err)
			}
		}
	}

	// Staging allows http to a public host, not a private one.
	staging := newHarness(t, func(d *agentsettings.Deps) { d.Environment = config.EnvStaging })
	staging.putKey(t, "plain", openCompatKey("http://llm.example.com/v1"))
	staging.mustUpdate(t, agentsettings.AgentUpdateInput{ProviderKey: ptr("plain"), Model: ptr("llama")})
	staging.putKey(t, "inside", openCompatKey("http://10.0.0.5/v1"))
	_, err = staging.update(t, agentsettings.AgentUpdateInput{ProviderKey: ptr("inside")})
	wantInvalid(t, err, "private")
}

// The agent runs only on models the server prices: nexus's catalog or
// ai.prices. A key's own prices don't count.
func TestUnpricedModelIsRefused(t *testing.T) {
	h := newHarness(t)
	h.budget(t, "25")
	h.putKey(t, "local", openCompatKey("https://llm.example.com/v1"))
	_, err := h.update(t, agentsettings.AgentUpdateInput{Enabled: ptr(true), ProviderKey: ptr("local"), Model: ptr("llama")})
	wantInvalid(t, err, "ai.prices")

	// Priced in ai.prices, it runs.
	priced := newHarness(t, func(d *agentsettings.Deps) {
		d.Config.Prices = []config.AIPrice{{Provider: "openai-compatible", Model: "llama", InputPerMillion: "0.1", OutputPerMillion: "0.2"}}
	})
	priced.budget(t, "25")
	priced.putKey(t, "local", openCompatKey("https://llm.example.com/v1"))
	priced.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(true), ProviderKey: ptr("local"), Model: ptr("llama")})

	// While it's on, a change to an unpriced model is refused too.
	_, err = h.update(t, agentsettings.AgentUpdateInput{Enabled: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.update(t, agentsettings.AgentUpdateInput{Model: ptr("claude-unpriced")})
	wantInvalid(t, err, "ai.prices")
}

func TestModelIsNeededForAnotherProvider(t *testing.T) {
	h := newHarness(t)
	h.budget(t, "25")
	h.putKey(t, "gpt", aisettings.ProviderKeyInput{Type: "openai", Fields: map[string]string{"api_key": "sk-openai-1234"}})
	_, err := h.update(t, agentsettings.AgentUpdateInput{Enabled: ptr(true), ProviderKey: ptr("gpt")})
	wantInvalid(t, err, "model")
	h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(true), ProviderKey: ptr("gpt"), Model: ptr("gpt-x")})
}

func TestEffectiveInheritsServerDefaults(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.budget(t, "25")
	h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(true)})
	e, err := h.svc.Effective(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	want := agentsettings.Effective{
		OrgID: org, Provider: "anthropic", Model: config.DefaultAIModel, SummaryModel: config.SummaryAIModel,
		NexusTenantID: "tenant_o", MonthlyBudgetUSD: "25.00", Enabled: true,
	}
	if *e != want {
		t.Errorf("effective\n%+v\nwant\n%+v", *e, want)
	}

	h.putKey(t, "gpt", aisettings.ProviderKeyInput{Type: "openai", Fields: map[string]string{"api_key": "sk-openai-1234"}})
	h.mustUpdate(t, agentsettings.AgentUpdateInput{ProviderKey: ptr("gpt"), Model: ptr("gpt-x")})
	e, _ = h.svc.Effective(ctx, org)
	if e.Provider != "openai" || e.Model != "gpt-x" || e.SummaryModel != "gpt-x" || e.ProviderKey != "gpt" {
		t.Errorf("openai %+v", e)
	}

	h.gw.priced["openai-compatible/llama"] = true
	h.putKey(t, "local", openCompatKey("https://llm.example.com/v1"))
	h.mustUpdate(t, agentsettings.AgentUpdateInput{ProviderKey: ptr("local"), Model: ptr("llama")})
	e, _ = h.svc.Effective(ctx, org)
	if e.Provider != "openai-compatible" || e.BaseURL != "https://llm.example.com/v1" || e.SummaryModel != "llama" {
		t.Errorf("opencompat %+v", e)
	}
}

func TestEffectiveIsDisabledWhenOff(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.Effective(ctx, org); !errors.Is(err, agentsettings.ErrDisabled) {
		t.Errorf("never set up: %v", err)
	}
	if on, err := h.svc.Enabled(ctx, org); on || err != nil {
		t.Errorf("enabled %t %v", on, err)
	}
	if _, err := h.svc.NexusTenant(ctx, org); !errors.Is(err, agentsettings.ErrDisabled) {
		t.Errorf("tenant while off: %v", err)
	}
	h.budget(t, "25")
	h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(true)})
	if tenant, err := h.svc.NexusTenant(ctx, org); tenant != "tenant_o" || err != nil {
		t.Errorf("tenant %q %v", tenant, err)
	}
	h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(false)})
	if _, err := h.svc.Effective(ctx, org); !errors.Is(err, agentsettings.ErrDisabled) {
		t.Errorf("turned off: %v", err)
	}
}

func TestKnowledgeNeedsServerSupport(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.budget(t, "25")
	h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(true), Knowledge: ptr(true)})
	h.svc.SetKnowledge(agentsettings.KnowledgeStatus{Configured: false, Reason: "Knowledge isn't configured on this server"})
	e, _ := h.svc.Effective(ctx, org)
	v, _ := h.svc.Get(ctx, admin)
	if e.Knowledge || !v.Knowledge || v.KnowledgeStatus.Reason == "" {
		t.Errorf("effective %+v, view %+v", e, v.KnowledgeStatus)
	}
	h.svc.SetKnowledge(agentsettings.KnowledgeStatus{Configured: true, Available: true})
	if e, _ = h.svc.Effective(ctx, org); !e.Knowledge {
		t.Error("knowledge available and on, and Effective says no")
	}
}

func TestBudgetState(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// No allocation: nothing to spend.
	if err := h.svc.RequireBudget(ctx, org); !errors.Is(err, agentsettings.ErrBudgetExhausted) {
		t.Errorf("no allocation: %v", err)
	}
	h.budget(t, "25")
	h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(true)})
	for spend, exhausted := range map[string]bool{"25.00": true, "25.0001": true, "24.99": false, "24.999999": false, "0": false} {
		h.gw.spend["tenant_o"] = spend
		b, err := h.svc.Budget(ctx, org)
		if err != nil {
			t.Fatal(err)
		}
		if b.LimitUSD != "25.00" || b.SpentUSD != spend || b.Exhausted != exhausted {
			t.Errorf("spend %s: %+v", spend, b)
		}
		err = h.svc.RequireBudget(ctx, org)
		if exhausted != errors.Is(err, agentsettings.ErrBudgetExhausted) || (!exhausted && err != nil) {
			t.Errorf("spend %s: RequireBudget %v", spend, err)
		}
	}
}

func TestSetBudgetAllocatesTheAgentScope(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.SetBudget(ctx, developer, agentsettings.AgentBudgetInput{MonthlyBudgetUSD: "10", Revision: h.rev(t)}); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("developer: %v", err)
	}
	// More than the workspace budget leaves.
	if _, err := h.svc.SetBudget(ctx, admin, agentsettings.AgentBudgetInput{MonthlyBudgetUSD: "10", Revision: h.rev(t)}); !errors.Is(err, aisettings.ErrOverBudget) {
		t.Errorf("over the workspace budget: %v", err)
	}
	h.budget(t, "30")
	a, err := h.ai.Allocation(ctx, org, agentsettings.AllocationScope)
	if err != nil || a.Label != "Infra agent" || a.MonthlyBudgetUSD != "30.00" || a.ProjectID != "" {
		t.Fatalf("allocation %+v %v", a, err)
	}
	if _, nerr := h.svc.SetBudget(ctx, admin, agentsettings.AgentBudgetInput{MonthlyBudgetUSD: "-1", Revision: h.rev(t)}); !errors.Is(nerr, agentsettings.ErrInvalid) && !errors.Is(nerr, aisettings.ErrInvalid) {
		t.Errorf("negative: %v", nerr)
	}

	// While on, a change reaches the tenant's quota, and the budget can't
	// go to zero.
	h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(true)})
	h.gw.take()
	v, err := h.svc.SetBudget(ctx, admin, agentsettings.AgentBudgetInput{MonthlyBudgetUSD: "40", Revision: h.rev(t)})
	if err != nil || v.Budget.LimitUSD != "40.00" {
		t.Fatalf("raise: %+v %v", v, err)
	}
	if got := h.gw.take(); !slices.Equal(got, []string{"budget tenant_o 40.00"}) {
		t.Errorf("gateway %v", got)
	}
	_, err = h.svc.SetBudget(ctx, admin, agentsettings.AgentBudgetInput{MonthlyBudgetUSD: "0", Revision: h.rev(t)})
	wantInvalid(t, err, "turn the agent off")

	// Off, zero releases it.
	h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(false)})
	if _, err = h.svc.SetBudget(ctx, admin, agentsettings.AgentBudgetInput{MonthlyBudgetUSD: "0", Revision: h.rev(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err = h.ai.Allocation(ctx, org, agentsettings.AllocationScope); !errors.Is(err, aisettings.ErrNotFound) {
		t.Errorf("released allocation: %v", err)
	}
}

func TestStatusForMembers(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.budget(t, "25")
	h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(true)})
	st, err := h.svc.Status(ctx, viewer)
	if err != nil || *st != (agentsettings.Status{Server: true, Enabled: true}) {
		t.Errorf("viewer: %+v %v", st, err)
	}
	if _, err := h.svc.Status(ctx, outsider); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("outsider: %v", err)
	}
}

// The agent's settings and the workspace's share one revision.
func TestStaleRevisionIsRefused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	read := h.rev(t)
	budget := "100"
	if _, err := h.ai.Update(ctx, admin, aisettings.UpdateInput{MonthlyBudgetUSD: &budget, Revision: read}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Update(ctx, admin, agentsettings.AgentUpdateInput{Model: ptr("claude-sonnet-5-5"), Revision: read}); !errors.Is(err, agentsettings.ErrStale) {
		t.Errorf("stale agent write: %v", err)
	}
	before := h.rev(t)
	h.mustUpdate(t, agentsettings.AgentUpdateInput{Model: ptr("claude-sonnet-5-5")})
	if h.rev(t) != before+1 {
		t.Errorf("revision %d after %d", h.rev(t), before)
	}
	if _, err := h.ai.Update(ctx, admin, aisettings.UpdateInput{MonthlyBudgetUSD: &budget, Revision: before}); !errors.Is(err, aisettings.ErrStale) {
		t.Errorf("stale workspace write after the agent's: %v", err)
	}
}

func TestSettingsChangesAreAudited(t *testing.T) {
	h := newHarness(t)
	h.budget(t, "25")
	h.putKey(t, "claude", anthropicKey())
	h.audit.take()
	h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(true), ProviderKey: ptr("claude"), Model: ptr("claude-sonnet-5-5")})
	// Refused writes aren't recorded.
	_, _ = h.svc.Update(context.Background(), developer, agentsettings.AgentUpdateInput{Enabled: ptr(false), Revision: h.rev(t)})
	_, _ = h.update(t, agentsettings.AgentUpdateInput{ProviderKey: ptr("nope")})

	events := h.audit.take()
	if len(events) != 1 || events[0].Action != "agent.settings.update" || events[0].Principal != admin {
		t.Fatalf("events %+v", events)
	}
	fields, _ := events[0].Attributes["fields"].([]string)
	if !slices.Equal(fields, []string{"enabled", "model", "provider_key"}) {
		t.Errorf("fields %v", fields)
	}
	k, _ := h.ai.KeyInfo(context.Background(), org, "claude")
	if events[0].Attributes["provider_key"] != "claude" || events[0].Attributes["provider_key_id"] != k.ID || events[0].Attributes["key_hint"] != "wxyz" {
		t.Errorf("attributes %v", events[0].Attributes)
	}
	if strings.Contains(fmt.Sprint(events[0].Attributes), secretValue) {
		t.Errorf("the audit carries the key: %v", events[0].Attributes)
	}
}

// Keys written through the agent's settings are Anthropic, OpenAI or
// OpenAI-compatible, and the agent's own key stays usable by it.
func TestProviderKeyWritesKeepTheAgentsKeyUsable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.PutProviderKey(ctx, developer, "claude", anthropicKey()); !errors.Is(err, security.ErrForbidden) {
		t.Errorf("developer: %v", err)
	}
	_, err := h.svc.PutProviderKey(ctx, admin, "mistral", aisettings.ProviderKeyInput{Type: "mistral", Fields: map[string]string{"api_key": "mistral-key-1234"}})
	wantInvalid(t, err, "Anthropic, OpenAI or OpenAI-compatible")
	h.putKey(t, "groq", aisettings.ProviderKeyInput{Type: "groq", Fields: map[string]string{"api_key": "groq-key-12345"}})
	if err = h.svc.DeleteProviderKey(ctx, admin, "groq"); !errors.Is(err, agentsettings.ErrInvalid) {
		t.Errorf("delete a groq key: %v", err)
	}

	k, err := h.svc.PutProviderKey(ctx, admin, "claude", anthropicKey())
	if err != nil || k.KeyHint != "wxyz" {
		t.Fatalf("put: %+v %v", k, err)
	}
	h.putKey(t, "local", openCompatKey("https://llm.example.com/v1"))
	h.mustUpdate(t, agentsettings.AgentUpdateInput{ProviderKey: ptr("local")})

	// The agent's key can't be narrowed to some projects, moved to a
	// private address or deleted.
	_, err = h.svc.PutProviderKey(ctx, admin, "local", aisettings.ProviderKeyInput{Projects: &[]string{"p1"}})
	wantInvalid(t, err, "every project")
	_, err = h.svc.PutProviderKey(ctx, admin, "local", aisettings.ProviderKeyInput{Fields: map[string]string{"base_url": "https://10.0.0.9/v1"}})
	wantInvalid(t, err, "private")
	if err = h.svc.DeleteProviderKey(ctx, admin, "local"); !errors.Is(err, aisettings.ErrInUse) {
		t.Errorf("delete the agent's key: %v", err)
	}
	// Another key can.
	if _, err = h.svc.PutProviderKey(ctx, admin, "claude", aisettings.ProviderKeyInput{Projects: &[]string{"p1"}}); err != nil {
		t.Errorf("narrow another key: %v", err)
	}
	if err = h.svc.DeleteProviderKey(ctx, admin, "claude"); err != nil {
		t.Errorf("delete another key: %v", err)
	}
}

// Two enables at the same revision: the one that claims the row first
// turns the agent on, and the other changes nothing. However they
// interleave, the agent ends on, with its tenant open and its role.
func TestConcurrentEnablesLeaveTheAgentOn(t *testing.T) {
	for name, existingTenant := range map[string]bool{"first enable": false, "enable again": true} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.budget(t, "25")
			if existingTenant {
				h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(true)})
				h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(false)})
			}
			rev := h.rev(t)
			h.gw.take()
			h.roles.take()
			var other error
			// The other enable lands while the first is seeding.
			h.seed.during = func() {
				_, other = h.svc.Update(context.Background(), admin, agentsettings.AgentUpdateInput{Enabled: ptr(true), Revision: rev})
			}
			_, first := h.svc.Update(context.Background(), admin, agentsettings.AgentUpdateInput{Enabled: ptr(true), Revision: rev})
			if errors.Is(first, agentsettings.ErrStale) == errors.Is(other, agentsettings.ErrStale) || (first != nil && !errors.Is(first, agentsettings.ErrStale)) || (other != nil && !errors.Is(other, agentsettings.ErrStale)) {
				t.Fatalf("first %v, other %v: want one on and one stale", first, other)
			}
			st, _ := h.store.Get(context.Background(), org)
			if !st.Enabled || st.NexusTenantID != "tenant_o" {
				t.Fatalf("stored %+v", st)
			}
			calls := h.gw.take()
			if len(calls) == 0 || calls[len(calls)-1] != "active tenant_o true" || slices.Contains(calls, "active tenant_o false") {
				t.Errorf("gateway %v: the tenant isn't left open", calls)
			}
			roles := h.roles.take()
			if !slices.Contains(roles, "assign o") || slices.Contains(roles, "revoke o") {
				t.Errorf("roles %v: the agent isn't left its role", roles)
			}
		})
	}
}

// A turn-on whose side effect fails puts the row back off, closes the
// tenant and takes the role away again, and says why.
func TestEnableRevertsWhenASideEffectFails(t *testing.T) {
	h := newHarness(t)
	h.budget(t, "25")
	h.gw.take()
	h.roles.assignErr = errors.New("warden is down")
	_, err := h.update(t, agentsettings.AgentUpdateInput{Enabled: ptr(true)})
	if err == nil || !strings.Contains(err.Error(), "warden is down") {
		t.Fatalf("enable: %v", err)
	}
	if on, _ := h.svc.Enabled(context.Background(), org); on {
		t.Error("the agent is on")
	}
	if got := h.roles.take(); !slices.Equal(got, []string{"assign o", "revoke o"}) {
		t.Errorf("roles %v", got)
	}
	if got := h.gw.take(); !slices.Equal(got, []string{"ensure o 25.00", "active tenant_o false"}) {
		t.Errorf("gateway %v", got)
	}
	// The tenant is kept for the next try, which works.
	h.roles.assignErr = nil
	v := h.mustUpdate(t, agentsettings.AgentUpdateInput{Enabled: ptr(true)})
	if !v.Enabled || v.NexusTenantID != "tenant_o" {
		t.Errorf("retry %+v", v.Settings)
	}
	if events := h.audit.take(); len(events) == 0 {
		t.Error("the retry wasn't audited")
	}
}

// staleOnEnable is a store whose save that turns the agent on finds the
// row changed, as when someone else saved meanwhile.
type staleOnEnable struct{ agentsettings.Store }

func (s staleOnEnable) Save(ctx context.Context, in *agentsettings.Settings, expect int64) error {
	if in.Enabled {
		return agentsettings.ErrStale
	}
	return s.Store.Save(ctx, in, expect)
}

// An enable that loses its claim on the row changes nothing: no tenant,
// no seed, no role.
func TestALostClaimChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.budget(t, "25")
	h.restart(func(d *agentsettings.Deps) { d.Store = staleOnEnable{h.store} })
	h.gw.take()
	if _, err := h.update(t, agentsettings.AgentUpdateInput{Enabled: ptr(true)}); !errors.Is(err, agentsettings.ErrStale) {
		t.Fatalf("enable: %v", err)
	}
	if roles, calls, seeds := h.roles.take(), h.gw.take(), h.seed.take(); roles != nil || calls != nil || seeds != nil {
		t.Errorf("a lost claim did roles %v, gateway %v, seeds %v", roles, calls, seeds)
	}
	if on, _ := h.svc.Enabled(context.Background(), org); on {
		t.Error("the agent is on")
	}
}

// Two budget writes at the same revision: the first to claim the row sets
// the agent's budget, and the other is stale and changes nothing.
func TestConcurrentBudgetWritesOneWins(t *testing.T) {
	h := newHarness(t)
	h.budget(t, "25")
	rev := h.rev(t)
	amounts := []string{"30", "40"}
	errs := make([]error, len(amounts))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, amount := range amounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = h.svc.SetBudget(context.Background(), admin, agentsettings.AgentBudgetInput{MonthlyBudgetUSD: amount, Revision: rev})
		}()
	}
	close(start)
	wg.Wait()
	won := -1
	for i, err := range errs {
		switch {
		case err == nil && won < 0:
			won = i
		case errors.Is(err, agentsettings.ErrStale):
		default:
			t.Fatalf("writes: %v", errs)
		}
	}
	if won < 0 {
		t.Fatalf("no write won: %v", errs)
	}
	a, err := h.ai.Allocation(context.Background(), org, agentsettings.AllocationScope)
	if err != nil || a.MonthlyBudgetUSD != amounts[won]+".00" {
		t.Fatalf("allocation %+v %v, want the winner's %s", a, err, amounts[won])
	}
	if h.rev(t) <= rev {
		t.Fatalf("the revision didn't move: %d", h.rev(t))
	}

	// A write at an old revision is stale, releasing included.
	if _, err = h.svc.SetBudget(context.Background(), admin, agentsettings.AgentBudgetInput{MonthlyBudgetUSD: "0", Revision: rev}); !errors.Is(err, agentsettings.ErrStale) {
		t.Fatalf("stale release: %v", err)
	}
}
