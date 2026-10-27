package aisettings

import (
	"slices"
	"strings"
)

// ProviderField is one setting a provider key takes. Name is the key nexus's
// config.ProviderConfig reads it from.
type ProviderField struct {
	Name     string `json:"name"` // nexus config.ProviderConfig yaml key: api_key, base_url, region, access_key_id, ...
	Title    string `json:"title"`
	Secret   bool   `json:"secret"` // stored in Vault, never returned
	Required bool   `json:"required"`
	Primary  bool   `json:"primary,omitempty"` // the secret whose last four characters are the hint
}

// ProviderType is a kind of model provider a workspace can hold keys for,
// one of the types nexus's config package builds.
type ProviderType struct {
	Type      string          `json:"type"`  // "anthropic", "openai", "opencompat", "bedrock", ...
	Title     string          `json:"title"` // "Anthropic", "OpenAI-compatible (any URL)"
	Fields    []ProviderField `json:"fields"`
	Dynamic   bool            `json:"dynamic"`   // opencompat: pushed live through the admin API, needs Models
	Embedding bool            `json:"embedding"` // offers embedding models
	Docs      string          `json:"docs"`      // provider's key page
}

// field returns the type's field called name.
func (t ProviderType) field(name string) (ProviderField, bool) {
	for _, f := range t.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return ProviderField{}, false
}

// primary returns the name of the secret field the key hint comes from, or
// "" when the type has none.
func (t ProviderType) primary() string {
	for _, f := range t.Fields {
		if f.Primary {
			return f.Name
		}
	}
	return ""
}

var (
	apiKeyField = ProviderField{Name: "api_key", Title: "API key", Secret: true, Required: true, Primary: true}
	baseURL     = ProviderField{Name: "base_url", Title: "Base URL"}
)

// hosted is a provider reached with an API key and, optionally, another
// base URL (a proxy, or a region's endpoint).
func hosted(typ, title, docs string, embedding bool) ProviderType {
	return ProviderType{
		Type: typ, Title: title, Docs: docs, Embedding: embedding,
		Fields: []ProviderField{apiKeyField, baseURL},
	}
}

// providerTypes are nexus config/v1.8.1's provider types (config/apply.go),
// less the ones that can't serve a workspace: vertex reads a credential file
// from the gateway's disk, and ollama and lmstudio take no key and dial
// localhost, which opencompat covers with a real address. Embedding follows
// each nexus provider's Capabilities().Embeddings at that version.
var providerTypes = []ProviderType{
	hosted("anthropic", "Anthropic", "https://console.anthropic.com/settings/keys", false),
	hosted("openai", "OpenAI", "https://platform.openai.com/api-keys", true),
	{
		Type: "opencompat", Title: "OpenAI-compatible (any URL)", Dynamic: true, Embedding: true,
		Fields: []ProviderField{
			{Name: "base_url", Title: "Base URL", Required: true},
			{Name: "api_key", Title: "API key", Secret: true, Primary: true},
		},
	},
	hosted("ai21", "AI21 Labs", "https://studio.ai21.com/account/api-key", false),
	{
		Type: "bedrock", Title: "Amazon Bedrock", Docs: "https://console.aws.amazon.com/iam/home#/security_credentials",
		Fields: []ProviderField{
			{Name: "region", Title: "Region", Required: true},
			{Name: "access_key_id", Title: "Access key ID", Secret: true, Required: true, Primary: true},
			{Name: "secret_access_key", Title: "Secret access key", Secret: true, Required: true},
			{Name: "session_token", Title: "Session token", Secret: true},
			baseURL,
		},
	},
	hosted("anyscale", "Anyscale", "https://console.anyscale.com/credentials", false),
	{
		Type: "azureopenai", Title: "Azure OpenAI", Embedding: true,
		Docs: "https://portal.azure.com/#view/Microsoft_Azure_ProjectOxford/CognitiveServicesHub/~/OpenAI",
		Fields: []ProviderField{
			apiKeyField,
			{Name: "resource_name", Title: "Resource name", Required: true},
			{Name: "deployment_id", Title: "Deployment ID", Required: true},
			{Name: "api_version", Title: "API version"},
			baseURL,
		},
	},
	hosted("cerebras", "Cerebras", "https://cloud.cerebras.ai/platform", false),
	hosted("cohere", "Cohere", "https://dashboard.cohere.com/api-keys", true),
	hosted("deepinfra", "DeepInfra", "https://deepinfra.com/dash/api_keys", true),
	hosted("deepseek", "DeepSeek", "https://platform.deepseek.com/api_keys", false),
	hosted("fireworks", "Fireworks AI", "https://fireworks.ai/account/api-keys", true),
	hosted("gemini", "Google Gemini", "https://aistudio.google.com/apikey", true),
	hosted("groq", "Groq", "https://console.groq.com/keys", false),
	hosted("hyperbolic", "Hyperbolic", "https://app.hyperbolic.xyz/settings", false),
	hosted("jinaai", "Jina AI", "https://jina.ai/api-dashboard", true),
	hosted("lepton", "Lepton AI", "https://dashboard.lepton.ai", false),
	hosted("mistral", "Mistral", "https://console.mistral.ai/api-keys", true),
	hosted("nvidia", "NVIDIA NIM", "https://build.nvidia.com", true),
	hosted("nebius", "Nebius AI Studio", "https://studio.nebius.com/settings/api-keys", true),
	hosted("novita", "Novita AI", "https://novita.ai/settings/key-management", false),
	hosted("openrouter", "OpenRouter", "https://openrouter.ai/settings/keys", true),
	hosted("perplexity", "Perplexity", "https://www.perplexity.ai/settings/api", false),
	hosted("sambanova", "SambaNova", "https://cloud.sambanova.ai/apis", false),
	hosted("together", "Together AI", "https://api.together.ai/settings/api-keys", true),
	hosted("voyageai", "Voyage AI", "https://dashboard.voyageai.com/api-keys", true),
	hosted("xai", "xAI", "https://console.x.ai", false),
}

// firstTypes lead the list; the rest follow by title.
var firstTypes = []string{"anthropic", "openai", "opencompat"}

// ProviderTypes lists the provider types a workspace can hold keys for:
// Anthropic, OpenAI and OpenAI-compatible first, then the rest by title.
func ProviderTypes() []ProviderType {
	out := make([]ProviderType, len(providerTypes))
	for i, t := range providerTypes {
		t.Fields = slices.Clone(t.Fields)
		out[i] = t
	}
	slices.SortStableFunc(out, func(a, b ProviderType) int {
		ai, bi := slices.Index(firstTypes, a.Type), slices.Index(firstTypes, b.Type)
		switch {
		case ai >= 0 && bi >= 0:
			return ai - bi
		case ai >= 0:
			return -1
		case bi >= 0:
			return 1
		}
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	})
	return out
}

// providerType returns the offered type called typ.
func providerType(typ string) (ProviderType, bool) {
	for _, t := range providerTypes {
		if t.Type == typ {
			return t, true
		}
	}
	return ProviderType{}, false
}
