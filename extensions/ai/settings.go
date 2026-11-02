package ai

import (
	"context"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/security"
)

// budgetAPI is what the workspace budget route asks of the AI settings.
// *aisettings.Service is the real one.
type budgetAPI interface {
	Update(ctx context.Context, p security.Principal, in aisettings.UpdateInput) (*aisettings.View, error)
}

// agentAPI is what the provider key and agent routes ask of the agent's
// settings, which keep the agent's own key usable and write through the
// AI settings. *agentsettings.Service is the real one; every method needs
// ai:manage.
type agentAPI interface {
	Get(ctx context.Context, p security.Principal) (*agentsettings.AgentView, error)
	Update(ctx context.Context, p security.Principal, in agentsettings.AgentUpdateInput) (*agentsettings.AgentView, error)
	SetBudget(ctx context.Context, p security.Principal, in agentsettings.AgentBudgetInput) (*agentsettings.AgentView, error)
	PutProviderKey(ctx context.Context, p security.Principal, name string, in aisettings.ProviderKeyInput) (*aisettings.ProviderKey, error)
	DeleteProviderKey(ctx context.Context, p security.Principal, name string) error
}

var (
	_ budgetAPI = (*aisettings.Service)(nil)
	_ agentAPI  = (*agentsettings.Service)(nil)
)

// WorkspaceBudgetInput is the workspace's monthly AI budget, with the
// settings revision the page read.
type WorkspaceBudgetInput struct {
	// MonthlyBudgetUSD is an amount in USD, like 250 or 99.50. It can't go
	// below what's already allocated.
	MonthlyBudgetUSD string `json:"monthly_budget_usd"`
	Revision         int64  `json:"revision"`
}

// Descriptions of the settings writes.
const (
	providerKeyDescription = "Anthropic, OpenAI and OpenAI-compatible keys only. Secret fields are write-only: no " +
		"answer carries them, and only the key's hint (its last four characters) is shown. A secret field left out " +
		"keeps its value. The key the agent uses must stay granted to every project."
	agentDescription = "Turning the agent on needs its monthly budget, a provider key (or the server's key) and a " +
		"model this server prices: nexus's catalog or ai.prices. A key's own model prices don't count for the agent."
)

// mountSettings serves the AI settings writes: the workspace budget,
// provider keys, and the agent's settings and budget, under /api/v1 behind
// guard.
func mountSettings(r forge.Router, guard identity.Guard, b budgetAPI, a agentAPI) error {
	h := settingsHandlers{budget: b, agent: a}
	g := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("AI"))
	errs := []error{
		g.PUT("/ai/settings/budget", h.setBudget,
			forge.WithName("aiSettings.setBudget"),
			forge.WithSummary("Set the workspace's monthly AI budget, which every allocation shares"),
			forge.WithRequestSchema(WorkspaceBudgetInput{}),
			forge.WithResponseSchema(http.StatusOK, "The settings", aisettings.View{}),
		),
		g.PUT("/ai/settings/provider-keys/:name", h.putKey,
			forge.WithName("aiSettings.putProviderKey"),
			forge.WithSummary("Add a provider key, or change one; its secrets are never shown again"),
			forge.WithDescription(providerKeyDescription),
			forge.WithRequestSchema(aisettings.ProviderKeyInput{}),
			forge.WithResponseSchema(http.StatusOK, "The key, without its secrets", aisettings.ProviderKey{}),
		),
		g.DELETE("/ai/settings/provider-keys/:name", h.deleteKey,
			forge.WithName("aiSettings.deleteProviderKey"),
			forge.WithSummary("Delete a provider key, unless the agent or the workspace embeddings use it"),
			forge.WithNoContentResponse(),
		),
		g.GET("/ai/settings/agent", h.getAgent,
			forge.WithName("aiSettings.getAgent"),
			forge.WithSummary("The agent's settings, what it runs with and its month's budget, without any key"),
			forge.WithResponseSchema(http.StatusOK, "The agent's settings", agentsettings.AgentView{}),
		),
		g.PATCH("/ai/settings/agent", h.updateAgent,
			forge.WithName("aiSettings.updateAgent"),
			forge.WithSummary("Turn the agent on or off, or change its provider key or model"),
			forge.WithDescription(agentDescription),
			forge.WithRequestSchema(agentsettings.AgentUpdateInput{}),
			forge.WithResponseSchema(http.StatusOK, "The agent's settings", agentsettings.AgentView{}),
		),
		g.PUT("/ai/settings/agent/budget", h.setAgentBudget,
			forge.WithName("aiSettings.setAgentBudget"),
			forge.WithSummary("Set the agent's monthly budget, its share of the workspace's; 0 releases it while the agent is off"),
			forge.WithRequestSchema(agentsettings.AgentBudgetInput{}),
			forge.WithResponseSchema(http.StatusOK, "The agent's settings", agentsettings.AgentView{}),
		),
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// settingsHandlers serve the AI settings writes.
type settingsHandlers struct {
	budget budgetAPI
	agent  agentAPI
}

func (h settingsHandlers) setBudget(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in WorkspaceBudgetInput
	if berr := bind(c, &in, `{"monthly_budget_usd": "250", "revision": 3}`); berr != nil {
		return berr
	}
	out, err := h.budget.Update(c.Request().Context(), p, aisettings.UpdateInput{MonthlyBudgetUSD: &in.MonthlyBudgetUSD, Revision: in.Revision})
	return answer(c, http.StatusOK, out, err)
}

func (h settingsHandlers) putKey(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in aisettings.ProviderKeyInput
	if berr := bind(c, &in, `{"type": "anthropic", "fields": {"api_key": "..."}}`); berr != nil {
		return berr
	}
	out, err := h.agent.PutProviderKey(c.Request().Context(), p, c.Param("name"), in)
	return answer(c, http.StatusOK, out, err)
}

func (h settingsHandlers) deleteKey(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err = h.agent.DeleteProviderKey(c.Request().Context(), p, c.Param("name")); err != nil {
		return fail(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h settingsHandlers) getAgent(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	out, err := h.agent.Get(c.Request().Context(), p)
	return answer(c, http.StatusOK, out, err)
}

func (h settingsHandlers) updateAgent(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in agentsettings.AgentUpdateInput
	if berr := bind(c, &in, `{"enabled": true, "provider_key": "claude", "model": "...", "revision": 3}`); berr != nil {
		return berr
	}
	out, err := h.agent.Update(c.Request().Context(), p, in)
	return answer(c, http.StatusOK, out, err)
}

func (h settingsHandlers) setAgentBudget(c forge.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in agentsettings.AgentBudgetInput
	if berr := bind(c, &in, `{"monthly_budget_usd": "25", "revision": 3}`); berr != nil {
		return berr
	}
	out, err := h.agent.SetBudget(c.Request().Context(), p, in)
	return answer(c, http.StatusOK, out, err)
}
