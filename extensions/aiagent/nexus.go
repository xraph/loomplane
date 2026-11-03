package aiagent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/xraph/forge"
	"github.com/xraph/nexus"
	nexusext "github.com/xraph/nexus/extension"
	"github.com/xraph/nexus/model"
	"github.com/xraph/nexus/money"
	"github.com/xraph/nexus/pipeline"
	"github.com/xraph/nexus/provider"
	"github.com/xraph/nexus/providers/anthropic"
	"github.com/xraph/nexus/providers/openai"
	"github.com/xraph/nexus/providers/opencompat"
	"github.com/xraph/nexus/tenant"
	shieldengine "github.com/xraph/shield/engine"
	"github.com/xraph/shield/presets"

	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/config"
)

// What nexus gives each call: five minutes, and two retries of a call
// that failed in a way worth retrying.
const (
	gatewayTimeout    = 5 * time.Minute
	gatewayMaxRetries = 2
)

// openAIBaseURL is OpenAI's API, where an openai embeddings provider sends
// its requests.
const openAIBaseURL = "https://api.openai.com/v1"

// nexusOptions are the nexus extension's options: the chat providers, the
// embeddings provider when knowledge is configured, the gateway options
// and shield's output guard. The credential resolver, the dial guard and
// shield's engine are e's, bound late: nexus asks for none of them before
// it starts.
func (e *Extension) nexusOptions() []nexusext.Option {
	ai := e.cfg.AI
	// Nexus no longer mounts routes; disable_routes stays set so a release
	// that brings them back still serves none.
	opts := []nexusext.Option{nexusext.WithDisableRoutes(), nexusext.WithDisableMigrate()} //nolint:staticcheck // see above
	for _, p := range chatProviders(ai) {
		opts = append(opts, nexusext.WithProvider(p))
	}
	if ai.Knowledge.Configured() {
		opts = append(opts, nexusext.WithProvider(embeddingsProvider(ai.Knowledge)))
	}
	for _, o := range gatewayOptions(ai, lateCredentials{e}, e.providerTransport) {
		opts = append(opts, nexusext.WithGatewayOption(o))
	}
	for _, o := range guardOptions(e.shieldEngine) {
		opts = append(opts, nexusext.WithGatewayOption(o))
	}
	return opts
}

// shieldEngine is shield's engine, or nil before shield registers.
func (e *Extension) shieldEngine() *shieldengine.Engine {
	if e.shield == nil {
		return nil
	}
	return e.shield.Engine()
}

// gatewayOptions are the gateway's own options: the workspace credential
// resolver, the server's prices with unpriced models refused, the call
// timeout and retries, usage recording, and the transport wrapper that
// guards calls to a workspace's base URL. A nil creds or transport is
// left out, for tests.
func gatewayOptions(ai config.AIConfig, creds provider.CredentialProvider, transport func(http.RoundTripper) http.RoundTripper) []nexus.Option {
	overrides := prices(ai.Prices)
	if o, ok := embeddingPrice(context.Background(), ai.Knowledge, ai.Prices); ok {
		overrides = append(overrides, o)
	}
	opts := []nexus.Option{
		nexus.WithPriceOverrides(overrides...),
		nexus.WithUnpricedPolicy(nexus.RefuseUnpriced),
		nexus.WithTimeout(gatewayTimeout),
		nexus.WithMaxRetries(gatewayMaxRetries),
		nexus.WithUsageEnabled(true),
	}
	if creds != nil {
		opts = append(opts, nexus.WithCredentialProvider(creds))
	}
	if transport != nil {
		opts = append(opts, nexus.WithProviderTransport(transport))
	}
	return opts
}

// prices is ai.prices as nexus's price overrides. Config has checked each
// amount; one that still doesn't parse is left out, so its model stays
// unpriced and refused rather than free.
func prices(ps []config.AIPrice) []model.PriceOverride {
	out := make([]model.PriceOverride, 0, len(ps))
	for _, p := range ps {
		in, ierr := money.Parse(p.InputPerMillion)
		outPrice, oerr := money.Parse(p.OutputPerMillion)
		if ierr != nil || oerr != nil {
			continue
		}
		out = append(out, model.PriceOverride{Provider: p.Provider, Model: p.Model,
			Pricing: provider.Pricing{InputPerMillion: in, OutputPerMillion: outPrice, Free: in.IsZero() && outPrice.IsZero()}})
	}
	return out
}

// embeddingPrice is the price override for the server's embeddings, or ok
// false when there is none. Nexus keys prices by provider name, and the
// embeddings provider has its own name, so its models start unpriced:
//   - openai: the pricing of the openai catalog row for the embedding model.
//   - openai-compatible: the ai.prices entry for the embedding model, with
//     its input price as the embedding price. A zero entry is free.
func embeddingPrice(ctx context.Context, k config.AIKnowledgeConfig, ps []config.AIPrice) (model.PriceOverride, bool) {
	override := func(p provider.Pricing) (model.PriceOverride, bool) {
		return model.PriceOverride{Provider: agentsettings.EmbeddingsProvider, Model: k.EmbeddingModel, Pricing: p}, true
	}
	switch k.EmbeddingProvider {
	case config.AIProviderOpenAI:
		models, err := openai.New("").Models(ctx)
		if err != nil {
			return model.PriceOverride{}, false
		}
		for _, m := range models {
			if m.ID == k.EmbeddingModel {
				return override(m.Pricing)
			}
		}
	case config.AIProviderOpenAICompatible:
		for _, p := range ps {
			if p.Provider != config.AIProviderOpenAICompatible || p.Model != k.EmbeddingModel {
				continue
			}
			in, ierr := money.Parse(p.InputPerMillion)
			out, oerr := money.Parse(p.OutputPerMillion)
			if ierr != nil || oerr != nil {
				return model.PriceOverride{}, false
			}
			return override(provider.Pricing{InputPerMillion: in, EmbeddingPerMillion: in, Free: in.IsZero() && out.IsZero()})
		}
	}
	return model.PriceOverride{}, false
}

// embeddingsPriced reports whether nexus prices the server's embedding
// model, so knowledge can say so rather than fail on its first embed.
func (e *Extension) embeddingsPriced(ctx context.Context) bool { //nolint:unused // Boot (plan 3b task 6) calls it; drop this comment then
	return gatewayAdapter{gw: e.nexus.Gateway}.Priced(ctx, agentsettings.EmbeddingsProvider, e.cfg.AI.Knowledge.EmbeddingModel)
}

// serverKeys is the key each chat provider is built with: the server key
// (ai.api_key) for the server's provider, none for the others, so a
// workspace on another provider brings its own.
func serverKeys(ai config.AIConfig) map[string]string {
	keys := map[string]string{config.AIProviderAnthropic: "", config.AIProviderOpenAI: "", config.AIProviderOpenAICompatible: ""}
	if _, ok := keys[ai.Provider]; ok {
		keys[ai.Provider] = ai.APIKey
	}
	return keys
}

// chatProviders are the three providers the agent can run on, each with
// the server key only when it's the server's provider, and ai.base_url
// only for the server's provider.
func chatProviders(ai config.AIConfig) []provider.Provider {
	keys := serverKeys(ai)
	base := func(name string) string {
		if ai.Provider == name {
			return ai.BaseURL
		}
		return ""
	}
	aopts := []anthropic.Option{anthropic.WithPromptCaching(anthropic.CacheConfig{TTL: anthropic.CacheTTL5m, Messages: true})}
	if u := base(config.AIProviderAnthropic); u != "" {
		aopts = append(aopts, anthropic.WithBaseURL(u))
	}
	var oopts []openai.Option
	if u := base(config.AIProviderOpenAI); u != "" {
		oopts = append(oopts, openai.WithBaseURL(u))
	}
	return []provider.Provider{
		anthropic.New(keys[config.AIProviderAnthropic], aopts...),
		openai.New(keys[config.AIProviderOpenAI], oopts...),
		opencompat.New(config.AIProviderOpenAICompatible, base(config.AIProviderOpenAICompatible), keys[config.AIProviderOpenAICompatible]),
	}
}

// embeddingsProvider is the provider knowledge embeds with, under its own
// name so no embedding goes to a chat provider. It always runs on the
// server's embedding key.
func embeddingsProvider(k config.AIKnowledgeConfig) provider.Provider {
	base := k.EmbeddingBaseURL
	if base == "" && k.EmbeddingProvider == config.AIProviderOpenAI {
		base = openAIBaseURL
	}
	return opencompat.New(agentsettings.EmbeddingsProvider, base, k.EmbeddingAPIKey)
}

// lateCredentials is the agent settings' credential resolver, looked up on
// each call: nexus is built before the settings exist. Until they do, a
// call that would run on a workspace's key is refused, never served on
// the server's.
type lateCredentials struct{ e *Extension }

var (
	_ provider.CredentialResolver = lateCredentials{}
	_ provider.CredentialProvider = lateCredentials{}
)

var errNotReady = errors.New("aiagent: refused a call: the agent's settings aren't ready")

// ResolveCredential implements provider.CredentialResolver.
func (c lateCredentials) ResolveCredential(ctx context.Context, providerName string) (provider.Credential, error) {
	s := c.e.agentSettings()
	if s == nil {
		if pipeline.TenantID(ctx) == "" || providerName == agentsettings.EmbeddingsProvider {
			return provider.Credential{}, provider.ErrCredentialNotFound
		}
		return provider.Credential{}, errNotReady
	}
	return s.Credentials().ResolveCredential(ctx, providerName)
}

// GetCredential implements provider.CredentialProvider.
func (c lateCredentials) GetCredential(ctx context.Context, providerName string) (string, error) {
	s := c.e.agentSettings()
	if s == nil {
		if pipeline.TenantID(ctx) == "" || providerName == agentsettings.EmbeddingsProvider {
			return "", provider.ErrCredentialNotFound
		}
		return "", errNotReady
	}
	return s.Credentials().GetCredential(ctx, providerName)
}

// providerTransport wraps each provider's transport, from nexus's Start:
// the dial guard for calls to a workspace base URL, then forge's tracing,
// which nexus's own default would have added.
func (e *Extension) providerTransport(next http.RoundTripper) http.RoundTripper {
	s := e.agentSettings()
	if s == nil {
		// Nexus starts after every extension registered, so the settings
		// are there. Should they not be, every workspace base URL is
		// refused.
		s = agentsettings.NewService(agentsettings.Deps{Environment: config.EnvProduction, Lookup: refuseLookup})
	}
	return forge.InstrumentTransport(s.ProviderTransport(next))
}

func refuseLookup(context.Context, string) ([]netip.Addr, error) {
	return nil, errNotReady
}

// tenantSlug is the nexus tenant slug of a workspace.
func tenantSlug(orgID string) string {
	return "loomplane-" + strings.ToLower(strings.ReplaceAll(orgID, "_", "-"))
}

// gatewayAdapter is nexus as agentsettings.Gateway: one tenant per
// workspace, its monthly quota, its status and its spend.
type gatewayAdapter struct {
	gw func() *nexus.Gateway
}

var _ agentsettings.Gateway = gatewayAdapter{}

func (a gatewayAdapter) gateway() (*nexus.Gateway, error) {
	gw := a.gw()
	if gw == nil || gw.Tenants() == nil {
		return nil, fmt.Errorf("aiagent: %w", nexus.ErrNotInitialized)
	}
	return gw, nil
}

func usd(amount string) (money.USD, error) {
	v, err := money.Parse(amount)
	if err != nil {
		return money.USD{}, fmt.Errorf("aiagent: %q isn't an amount in USD: %w", amount, err)
	}
	return v, nil
}

// EnsureTenant finds the workspace's tenant by its slug, or creates it,
// and sets its monthly budget.
func (a gatewayAdapter) EnsureTenant(ctx context.Context, orgID, budgetUSD string) (string, error) {
	gw, err := a.gateway()
	if err != nil {
		return "", err
	}
	budget, err := usd(budgetUSD)
	if err != nil {
		return "", err
	}
	slug := tenantSlug(orgID)
	t, err := gw.Tenants().GetBySlug(ctx, slug)
	switch {
	case err == nil:
		q := t.Quota
		q.MonthlyBudgetUSD = budget
		if uerr := gw.Tenants().UpdateQuota(ctx, t.ID.String(), &q); uerr != nil {
			return "", fmt.Errorf("set the agent's nexus budget: %w", uerr)
		}
		return t.ID.String(), nil
	case !errors.Is(err, tenant.ErrNotFound) && !errors.Is(err, nexus.ErrTenantNotFound):
		return "", fmt.Errorf("find the agent's nexus tenant: %w", err)
	}
	t, err = gw.Tenants().Create(ctx, &tenant.CreateInput{
		Name: "Loomplane agent " + orgID, Slug: slug,
		Quota:    &tenant.Quota{MonthlyBudgetUSD: budget},
		Metadata: map[string]string{"org_id": orgID},
	})
	if err != nil {
		return "", fmt.Errorf("create the agent's nexus tenant: %w", err)
	}
	return t.ID.String(), nil
}

// SetBudget changes the tenant's monthly budget.
func (a gatewayAdapter) SetBudget(ctx context.Context, tenantID, budgetUSD string) error {
	gw, err := a.gateway()
	if err != nil {
		return err
	}
	budget, err := usd(budgetUSD)
	if err != nil {
		return err
	}
	t, err := gw.Tenants().Get(ctx, tenantID)
	if err != nil {
		return err
	}
	q := t.Quota
	q.MonthlyBudgetUSD = budget
	return gw.Tenants().UpdateQuota(ctx, tenantID, &q)
}

// SetActive opens the tenant, or closes it to every call.
func (a gatewayAdapter) SetActive(ctx context.Context, tenantID string, active bool) error {
	gw, err := a.gateway()
	if err != nil {
		return err
	}
	status := tenant.StatusDisabled
	if active {
		status = tenant.StatusActive
	}
	return gw.Tenants().SetStatus(ctx, tenantID, status)
}

// MonthlySpend is what the tenant spent this month, in USD.
func (a gatewayAdapter) MonthlySpend(ctx context.Context, tenantID string) (string, error) {
	gw, err := a.gateway()
	if err != nil {
		return "", err
	}
	spent, err := gw.Usage().MonthlySpend(ctx, tenantID)
	if err != nil {
		return "", err
	}
	return spent.String(), nil
}

// Priced reports whether nexus prices the model: its catalog or ai.prices.
func (a gatewayAdapter) Priced(ctx context.Context, providerName, modelID string) bool {
	gw := a.gw()
	return gw != nil && gw.PriceBook().Priced(ctx, providerName, modelID)
}

// shieldSeeder seeds a workspace's guardrail profiles: shield's
// InfraAgent preset under agentscope.App, then drops what shield cached
// for the workspace so the profiles apply at once.
type shieldSeeder struct {
	eng func() *shieldengine.Engine
}

var _ agentsettings.Seeder = shieldSeeder{}

// Seed implements agentsettings.Seeder.
func (s shieldSeeder) Seed(ctx context.Context, orgID string) error {
	eng := s.eng()
	if eng == nil {
		return errors.New("aiagent: shield isn't running")
	}
	if _, err := presets.InfraAgent().Seed(ctx, eng.Store(), orgID, agentscope.App); err != nil {
		return err
	}
	eng.InvalidateProfiles(orgID)
	return nil
}
