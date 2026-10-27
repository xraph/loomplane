// Package aisettings keeps a workspace's AI settings: its monthly AI
// budget and how that budget is shared out, the embeddings model its
// gateways use, and the provider keys they reach model providers with. Key
// values live in Vault; this package's tables hold only their metadata.
package aisettings

import "time"

// Settings is one workspace's AI settings.
type Settings struct {
	OrgID            string     `json:"org_id"`
	MonthlyBudgetUSD string     `json:"monthly_budget_usd"` // "0.00" until an admin sets it
	Embeddings       Embeddings `json:"embeddings"`
	Revision         int64      `json:"revision"` // 0 until the row is first written
	UpdatedBy        string     `json:"updated_by,omitempty"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// EmbeddingMode says where a workspace's embeddings come from.
type EmbeddingMode string

// The places embeddings can come from.
const (
	EmbeddingsLocal      EmbeddingMode = "local"      // the bundled ai-embeddings engine; the default
	EmbeddingsProvider   EmbeddingMode = "provider"   // a workspace provider key's embedding model
	EmbeddingsDeployment EmbeddingMode = "deployment" // a W3 model deployment; refused until W3 lands
)

// The model the bundled embeddings engine serves.
const (
	LocalEmbeddingModel      = "granite-embedding-107m-multilingual"
	LocalEmbeddingDimensions = 384
)

// Embeddings is a workspace's embeddings choice.
type Embeddings struct {
	Mode       EmbeddingMode `json:"mode"`
	Provider   string        `json:"provider,omitempty"` // ProviderKey.Name when Mode is provider
	Model      string        `json:"model"`
	Dimensions int           `json:"dimensions"`
	Deployment string        `json:"deployment,omitempty"` // W3
}

// LocalEmbeddings is the default choice: the bundled engine.
func LocalEmbeddings() Embeddings {
	return Embeddings{Mode: EmbeddingsLocal, Model: LocalEmbeddingModel, Dimensions: LocalEmbeddingDimensions}
}

// ProviderKey is a provider key's metadata. Its values are in Vault.
type ProviderKey struct {
	ID        string            `json:"id" forge:"id"` // typeid prefix "aikey"
	OrgID     string            `json:"org_id"`
	Name      string            `json:"name"`               // ^[a-z0-9][a-z0-9-]{0,62}$; the provider's name on every gateway
	Type      string            `json:"type"`               // a ProviderType.Type
	Fields    map[string]string `json:"fields,omitempty"`   // non-secret fields: base_url, region, resource_name, ...
	Models    []OpenCompatModel `json:"models,omitempty"`   // opencompat only
	KeyHint   string            `json:"key_hint,omitempty"` // last four characters of the primary secret
	Projects  []string          `json:"projects"`           // empty: every project
	Revision  int64             `json:"revision"`
	CreatedBy string            `json:"created_by"`
	UpdatedBy string            `json:"updated_by"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// OpenCompatModel is one model an OpenAI-compatible provider serves, with
// the prices Loomplane can't look up for it.
type OpenCompatModel struct {
	ID                  string `json:"id"`
	Task                string `json:"task"` // chat or embedding
	ContextWindow       int    `json:"context_window,omitempty"`
	MaxOutput           int    `json:"max_output,omitempty"`
	Dimensions          int    `json:"dimensions,omitempty"`
	InputPerMillion     string `json:"input_per_million"` // USD; "0" for free
	OutputPerMillion    string `json:"output_per_million"`
	EmbeddingPerMillion string `json:"embedding_per_million,omitempty"`
}

// Allocation is the share of the workspace budget one scope may spend.
type Allocation struct {
	Scope            string    `json:"scope"` // "gateway:<environment id>/<resource>" or "agent" (3a)
	ProjectID        string    `json:"project_id,omitempty"`
	EnvironmentID    string    `json:"environment_id,omitempty"`
	Label            string    `json:"label"` // "shop / staging"
	MonthlyBudgetUSD string    `json:"monthly_budget_usd"`
	UpdatedAt        time.Time `json:"updated_at"`
}
