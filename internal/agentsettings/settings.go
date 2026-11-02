// Package agentsettings is the workspace's infra agent settings: whether
// it's on, which provider key and model it runs with, its share of the
// workspace AI budget, and the nexus tenant its calls are billed under.
//
// It's a thin layer over the workspace AI settings (internal/aisettings).
// Its columns sit on the same loomplane_ai_settings row and share its
// revision. The agent's key is one of the workspace's provider keys,
// whose values aisettings keeps in Vault; this package never keeps a
// secret of its own. Its budget is the "agent" allocation of the
// workspace budget.
package agentsettings

import (
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/config"
)

// The agent's allocation of the workspace AI budget.
const (
	AllocationScope = "agent"
	AllocationLabel = "Infra agent"
)

// EmbeddingsProvider is the nexus provider knowledge embeds with. It's
// always served with the server's key, never a workspace's.
const EmbeddingsProvider = "loomplane-embeddings"

// agentKeyTypes are the provider key types the agent can use, by the
// nexus provider each one is. Nexus hands a tenant's credential only to
// providers that accept one, and these are those.
var agentKeyTypes = map[string]string{
	"anthropic":  config.AIProviderAnthropic,
	"openai":     config.AIProviderOpenAI,
	"opencompat": config.AIProviderOpenAICompatible,
}

// NexusProvider returns the in-process nexus provider a provider key type
// runs on, and whether the agent can use that type at all. aisettings'
// "opencompat" is nexus's "openai-compatible"; this is the one place that
// says so.
func NexusProvider(keyType string) (string, bool) {
	p, ok := agentKeyTypes[keyType]
	return p, ok
}

// Errors the Service returns. Test with errors.Is.
var (
	ErrInvalid         = errors.New("invalid agent setting")
	ErrDisabled        = errors.New("the agent is off for this workspace; an admin can turn it on in AI settings")
	ErrBudgetExhausted = errors.New("this workspace has used its AI budget for the month; an admin can raise it in AI settings")
	ErrKeyUnavailable  = errors.New("the workspace's AI key couldn't be read")
	// ErrStale and ErrNotFound are the workspace AI settings' own: the
	// agent's settings share their row.
	ErrStale    = aisettings.ErrStale
	ErrNotFound = aisettings.ErrNotFound
)

// Settings are the agent's columns of a workspace's AI settings row.
type Settings struct {
	OrgID   string `json:"org_id"`
	Enabled bool   `json:"enabled"`
	// ProviderKey names the workspace provider key the agent uses; "" is
	// the server's key (ai.api_key).
	ProviderKey string `json:"provider_key"`
	// Model is the agent's model; "" is the default for its provider.
	Model     string `json:"model"`
	Knowledge bool   `json:"knowledge"`
	// NexusTenantID is set the first time the agent turns on, and kept.
	NexusTenantID string `json:"nexus_tenant_id"`
	// Revision is the workspace AI settings' revision, shared with them.
	Revision  int64     `json:"revision"`
	UpdatedBy string    `json:"updated_by,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Effective is what the agent runs with in a workspace.
type Effective struct {
	OrgID            string
	Provider         string // nexus provider: anthropic, openai or openai-compatible
	ProviderKey      string // "" is the server's key
	Model            string
	SummaryModel     string
	BaseURL          string
	NexusTenantID    string
	MonthlyBudgetUSD string
	Enabled          bool
	Knowledge        bool // turned on, and available on this server
}

// KnowledgeStatus is whether this server can give the agent knowledge.
type KnowledgeStatus struct {
	Configured bool   `json:"configured"`
	Available  bool   `json:"available"`
	Reason     string `json:"reason,omitempty"`
}

// ServerDefaults are what the agent runs with when the workspace picks no
// key or model.
type ServerDefaults struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url,omitempty"`
	KeySet   bool   `json:"key_set"` // ai.api_key is set; never the key
}

// EffectiveView is Effective as the settings page shows it.
type EffectiveView struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	SummaryModel string `json:"summary_model"`
	BaseURL      string `json:"base_url,omitempty"`
}

// KeyView is the provider key the agent uses, without its secret.
type KeyView struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Hint string `json:"hint"`
}

// BudgetState is the agent's month so far.
type BudgetState struct {
	LimitUSD  string `json:"limit_usd"` // the agent allocation; "0.00" when none
	SpentUSD  string `json:"spent_usd"`
	Exhausted bool   `json:"exhausted"`
}

// AgentView is the agent's settings as the settings page shows them: never a
// key.
type AgentView struct {
	Settings
	Server          ServerDefaults  `json:"server"`
	Effective       EffectiveView   `json:"effective"`
	Key             *KeyView        `json:"key,omitempty"`
	Budget          BudgetState     `json:"budget"`
	KnowledgeStatus KnowledgeStatus `json:"knowledge_status"`
	// Available is whether this server can run the agent at all.
	Available bool `json:"available"`
	// Problem says why the agent couldn't run as set, when it couldn't.
	Problem string `json:"problem,omitempty"`
}

// Status is what any member sees.
type Status struct {
	Server    bool `json:"server"`  // ai.enabled
	Enabled   bool `json:"enabled"` // this workspace
	Knowledge bool `json:"knowledge"`
}

// AgentUpdateInput changes the agent's settings. A nil field keeps its value.
// Revision is the workspace AI settings' revision the caller read.
type AgentUpdateInput struct {
	Enabled *bool `json:"enabled,omitempty"`
	// ProviderKey names a workspace provider key; "" is the server's key.
	ProviderKey *string `json:"provider_key,omitempty"`
	// Model is the agent's model; "" is its provider's default.
	Model     *string `json:"model,omitempty"`
	Knowledge *bool   `json:"knowledge,omitempty"`
	Revision  int64   `json:"revision"`
}

// AgentBudgetInput sets the agent's monthly budget, its allocation of the
// workspace's. "0" releases it, which only an agent that's off can do.
type AgentBudgetInput struct {
	MonthlyBudgetUSD string `json:"monthly_budget_usd"`
}
