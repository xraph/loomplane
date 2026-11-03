package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// DefaultHuggingFaceURL is the Hugging Face Hub the models section reads
// from unless ai.models.huggingface_url points it somewhere else.
const DefaultHuggingFaceURL = "https://huggingface.co"

// The chat providers the agent can use, and its default models.
const (
	AIProviderAnthropic        = "anthropic"
	AIProviderOpenAI           = "openai"
	AIProviderOpenAICompatible = "openai-compatible"

	DefaultAIModel = "claude-sonnet-5-5"
	// SummaryAIModel is the cheaper model for summarising steps.
	SummaryAIModel = "claude-haiku-5-5"

	DefaultEmbeddingDimensions = 1536
	// MaxEmbeddingDimensions is pgvector.MaxIndexedDimensions; aiagent
	// asserts they match.
	MaxEmbeddingDimensions = 2000
)

// aiPriceDecimal is a non-negative USD amount with up to six decimals.
var aiPriceDecimal = regexp.MustCompile(`^\d+(\.\d{1,6})?$`)

// AIConfig switches the AI section on and configures the infra agent. It is
// off unless the operator asks.
type AIConfig struct {
	// Enabled turns on the AI section (ai.enabled). Later AI settings join
	// this struct rather than starting a second block.
	Enabled bool
	// Provider is the agent's server provider (ai.provider): anthropic,
	// openai or openai-compatible.
	Provider string
	// APIKey is the server key (ai.api_key). Optional even when enabled,
	// because a workspace can bring its own. It is never served or logged.
	APIKey string
	// BaseURL is required for openai-compatible and an optional override
	// otherwise (ai.base_url).
	BaseURL string
	// Model is the agent's default model (ai.model). It sits beside the
	// ai.models registry block, which is unrelated.
	Model string
	// Prices prices models nexus's catalog doesn't (ai.prices, YAML only).
	// The agent uses server prices only: these and nexus's catalog. A
	// workspace's per-key prices don't apply to it.
	Prices []AIPrice
	// Knowledge configures embeddings for retrieval (ai.knowledge).
	Knowledge AIKnowledgeConfig
	// Models configures where models come from (ai.models).
	Models AIModelsConfig
}

// AIKnowledgeConfig configures the one embedding model the whole server
// shares, since a pgvector column has one fixed dimension.
type AIKnowledgeConfig struct {
	// EmbeddingProvider is "", openai or openai-compatible. Anthropic has no
	// embeddings API.
	EmbeddingProvider string
	EmbeddingAPIKey   string
	EmbeddingBaseURL  string
	EmbeddingModel    string
	Dimensions        int
}

// Configured reports whether an embedding provider is set.
func (k AIKnowledgeConfig) Configured() bool { return k.EmbeddingProvider != "" }

// AIPrice prices one model, in decimal USD per million tokens.
type AIPrice struct {
	Provider         string
	Model            string
	InputPerMillion  string
	OutputPerMillion string
}

// Validate refuses AI settings the agent can't run with. It is called only
// when AI is enabled. Messages name the key and never echo a secret.
func (a AIConfig) Validate(environment string) error {
	var errs []error
	switch a.Provider {
	case AIProviderAnthropic, AIProviderOpenAI, AIProviderOpenAICompatible:
	default:
		errs = append(errs, fmt.Errorf("ai.provider must be anthropic, openai or openai-compatible, got %q", a.Provider))
	}
	if a.Provider == AIProviderOpenAICompatible && a.BaseURL == "" {
		errs = append(errs, errors.New("ai.base_url is required when ai.provider is openai-compatible"))
	}
	if err := checkAIBaseURL("ai.base_url", a.BaseURL, environment); err != nil {
		errs = append(errs, err)
	}
	if a.Model == "" {
		errs = append(errs, errors.New("ai.model is required"))
	}
	errs = append(errs, a.Knowledge.validate(environment), validateAIPrices(a.Prices))
	return errors.Join(errs...)
}

func (k AIKnowledgeConfig) validate(environment string) error {
	var errs []error
	switch k.EmbeddingProvider {
	case "":
	case AIProviderOpenAI, AIProviderOpenAICompatible:
		if k.EmbeddingModel == "" {
			errs = append(errs, errors.New("ai.knowledge.embedding_model is required when ai.knowledge.embedding_provider is set"))
		}
		if k.EmbeddingProvider == AIProviderOpenAICompatible && k.EmbeddingBaseURL == "" {
			errs = append(errs, errors.New("ai.knowledge.embedding_base_url is required when ai.knowledge.embedding_provider is openai-compatible"))
		}
	case AIProviderAnthropic:
		errs = append(errs, errors.New("ai.knowledge.embedding_provider anthropic is not supported: Anthropic has no embeddings API; use openai or openai-compatible"))
	default:
		errs = append(errs, fmt.Errorf("ai.knowledge.embedding_provider must be openai or openai-compatible, got %q", k.EmbeddingProvider))
	}
	if err := checkAIBaseURL("ai.knowledge.embedding_base_url", k.EmbeddingBaseURL, environment); err != nil {
		errs = append(errs, err)
	}
	if k.Dimensions < 1 || k.Dimensions > MaxEmbeddingDimensions {
		errs = append(errs, fmt.Errorf("ai.knowledge.dimensions must be between 1 and %d, got %d", MaxEmbeddingDimensions, k.Dimensions))
	}
	return errors.Join(errs...)
}

func validateAIPrices(prices []AIPrice) error {
	var errs []error
	seen := map[[2]string]bool{}
	for i, p := range prices {
		key := fmt.Sprintf("ai.prices[%d]", i)
		switch p.Provider {
		case AIProviderAnthropic, AIProviderOpenAI, AIProviderOpenAICompatible:
		default:
			errs = append(errs, fmt.Errorf("%s.provider must be anthropic, openai or openai-compatible, got %q", key, p.Provider))
		}
		if p.Model == "" {
			errs = append(errs, fmt.Errorf("%s.model is required", key))
		}
		if !aiPriceDecimal.MatchString(p.InputPerMillion) {
			errs = append(errs, fmt.Errorf("%s.input_per_million must be a decimal USD amount like 3 or 0.80, got %q", key, p.InputPerMillion))
		}
		if !aiPriceDecimal.MatchString(p.OutputPerMillion) {
			errs = append(errs, fmt.Errorf("%s.output_per_million must be a decimal USD amount like 15 or 0.80, got %q", key, p.OutputPerMillion))
		}
		id := [2]string{p.Provider, p.Model}
		if seen[id] {
			errs = append(errs, fmt.Errorf("%s prices %s %q a second time", key, p.Provider, p.Model))
		}
		seen[id] = true
	}
	return errors.Join(errs...)
}

// checkAIBaseURL accepts an empty URL, or an absolute http(s) one without
// credentials. Production wants https, since the key is sent there; http
// is allowed only for a loopback host.
func checkAIBaseURL(key, raw, environment string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return fmt.Errorf("%s must be an absolute http(s) URL, got %q", key, redactURL(raw))
	}
	if u.User != nil {
		return fmt.Errorf("%s can't carry a user or password", key)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if environment != EnvProduction || loopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("%s must be https in production, since the API key is sent to it (http only for localhost)", key)
	}
	return fmt.Errorf("%s must be an absolute http(s) URL, got %q", key, redactURL(raw))
}

// redactURL drops anything after the scheme and host that could hold a secret.
func redactURL(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	if i := strings.IndexAny(raw, "@?#"); i >= 0 {
		return raw[:i]
	}
	return raw
}

// AIModelsConfig configures the model registry's outside sources.
type AIModelsConfig struct {
	// HuggingFaceURL is the Hugging Face Hub, or a mirror of it
	// (ai.models.huggingface_url). The workspace's Hugging Face token goes
	// to this host, so it must be https; plain http is allowed only for a
	// loopback host, for tests and a mirror on the same machine.
	HuggingFaceURL string
}

// Validate refuses a Hugging Face URL the token shouldn't be sent to. An
// empty one is the default.
func (c AIModelsConfig) Validate() error {
	if c.HuggingFaceURL == "" {
		return nil
	}
	u, err := url.Parse(c.HuggingFaceURL)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return fmt.Errorf("ai.models.huggingface_url must be an absolute URL like %s, got %q", DefaultHuggingFaceURL, c.HuggingFaceURL)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("ai.models.huggingface_url can't carry a user, a query or a fragment, got %q", c.HuggingFaceURL)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if loopbackHost(u.Hostname()) {
			return nil
		}
	}
	return fmt.Errorf("ai.models.huggingface_url must be https, since the workspace's Hugging Face token is sent to it (http only for localhost), got %q", c.HuggingFaceURL)
}

// loopbackHost reports whether host is localhost or a loopback address.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
