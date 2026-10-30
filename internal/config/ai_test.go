package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestAIIsOffByDefault(t *testing.T) {
	if Defaults().AI.Enabled {
		t.Fatal("Defaults() turns AI on")
	}
	c, err := FromSource(mapGetter{"database.url": testDSN})
	if err != nil {
		t.Fatal(err)
	}
	if c.AI.Enabled {
		t.Fatal("AI is on with nothing set")
	}
}

func TestAIFromSource(t *testing.T) {
	c, err := FromSource(mapGetter{"database.url": testDSN, "ai.enabled": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.AI.Enabled {
		t.Fatal("ai.enabled=true did not turn AI on")
	}
}

func TestConfigYAMLWiresTheAIFlag(t *testing.T) {
	raw, err := os.ReadFile("../../config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	inAI := false
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" && line[0] != ' ' && line[0] != '#' {
			inAI = strings.TrimRight(line, " ") == "ai:"
			continue
		}
		if inAI && strings.TrimSpace(line) == `enabled: "${LOOMPLANE_AI_ENABLED:-false}"` {
			return
		}
	}
	t.Fatal(`config.yaml has no top-level "ai:" key with enabled: "${LOOMPLANE_AI_ENABLED:-false}"`)
}

func TestHuggingFaceURLDefaultsToTheHub(t *testing.T) {
	c, err := FromSource(mapGetter{"database.url": testDSN})
	if err != nil {
		t.Fatal(err)
	}
	if c.AI.Models.HuggingFaceURL != "https://huggingface.co" {
		t.Fatalf("huggingface_url %q", c.AI.Models.HuggingFaceURL)
	}
	c, err = FromSource(mapGetter{"database.url": testDSN, "ai.models.huggingface_url": "https://hf-mirror.example.com/hub"})
	if err != nil || c.AI.Models.HuggingFaceURL != "https://hf-mirror.example.com/hub" {
		t.Fatalf("mirror: %q, %v", c.AI.Models.HuggingFaceURL, err)
	}
}

// The workspace's Hugging Face token goes to this URL, so a load refuses
// anything but https, except plain http to a loopback host for tests and
// a mirror on the same machine.
func TestHuggingFaceURLMustBeHTTPSUnlessLoopback(t *testing.T) {
	for _, ok := range []string{
		"https://huggingface.co", "https://hf.internal:8443/mirror", "http://localhost:8080", "http://127.0.0.1:9999",
		"http://[::1]:8080", "HTTP://LOCALHOST",
	} {
		if _, err := FromSource(mapGetter{"database.url": testDSN, "ai.models.huggingface_url": ok}); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://huggingface.co", "http://10.0.0.5:8080", "http://localhost.evil.com", "ftp://huggingface.co",
		"huggingface.co", "https://", "https://user:pass@huggingface.co", "https://huggingface.co/?x=1", "//huggingface.co",
	} {
		_, err := FromSource(mapGetter{"database.url": testDSN, "ai.models.huggingface_url": bad})
		if err == nil || !strings.Contains(err.Error(), "ai.models.huggingface_url") {
			t.Errorf("%s accepted: %v", bad, err)
		}
	}
}

func TestConfigYAMLWiresTheHuggingFaceURL(t *testing.T) {
	raw, err := os.ReadFile("../../config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := `    huggingface_url: "${LOOMPLANE_AI_MODELS_HUGGINGFACE_URL:-https://huggingface.co}"`
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" && line[0] != ' ' && line[0] != '#' {
			section = strings.TrimRight(line, " ")
			continue
		}
		if section == "ai:" && line == want {
			return
		}
	}
	t.Fatalf("config.yaml has no %s under ai: models:", want)
}

func TestAIDefaults(t *testing.T) {
	want := AIConfig{
		Provider:  "anthropic",
		Model:     "claude-sonnet-5-5",
		Models:    AIModelsConfig{HuggingFaceURL: DefaultHuggingFaceURL},
		Knowledge: AIKnowledgeConfig{Dimensions: 1536},
	}
	if got := Defaults().AI; !reflect.DeepEqual(got, want) {
		t.Fatalf("Defaults().AI = %+v, want %+v", got, want)
	}
}

func TestAIAgentFromSource(t *testing.T) {
	c, err := FromSource(mapGetter{
		"database.url":                    testDSN,
		"ai.enabled":                      "true",
		"ai.provider":                     "openai-compatible",
		"ai.base_url":                     "https://llm.example.com/v1",
		"ai.api_key":                      "k",
		"ai.model":                        "llama-3.3-70b",
		"ai.knowledge.embedding_provider": "openai",
		"ai.knowledge.embedding_api_key":  "e",
		"ai.knowledge.embedding_model":    "text-embedding-3-small",
		"ai.knowledge.dimensions":         "1536",
		"ai.prices": []any{map[string]any{
			"provider": "openai-compatible", "model": "llama-3.3-70b",
			"input_per_million": "0.6", "output_per_million": "0.6",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := AIConfig{
		Enabled:  true,
		Provider: AIProviderOpenAICompatible,
		APIKey:   "k",
		BaseURL:  "https://llm.example.com/v1",
		Model:    "llama-3.3-70b",
		Models:   AIModelsConfig{HuggingFaceURL: DefaultHuggingFaceURL},
		Prices: []AIPrice{{
			Provider: "openai-compatible", Model: "llama-3.3-70b", InputPerMillion: "0.6", OutputPerMillion: "0.6",
		}},
		Knowledge: AIKnowledgeConfig{
			EmbeddingProvider: "openai", EmbeddingAPIKey: "e", EmbeddingModel: "text-embedding-3-small", Dimensions: 1536,
		},
	}
	if !reflect.DeepEqual(c.AI, want) {
		t.Fatalf("AI = %+v, want %+v", c.AI, want)
	}
	if !c.AI.Knowledge.Configured() {
		t.Fatal("Knowledge.Configured() is false with an embedding provider")
	}
	if (AIKnowledgeConfig{}).Configured() {
		t.Fatal("Knowledge.Configured() is true with no embedding provider")
	}
}

func TestAIIsNotValidatedWhenOff(t *testing.T) {
	_, err := FromSource(mapGetter{
		"database.url": testDSN, "ai.enabled": "false", "ai.provider": "bogus", "ai.knowledge.dimensions": "9000",
	})
	if err != nil {
		t.Fatalf("AI off but validated: %v", err)
	}
}

func validAI() AIConfig {
	return AIConfig{Enabled: true, Provider: AIProviderAnthropic, Model: DefaultAIModel, Knowledge: AIKnowledgeConfig{Dimensions: DefaultEmbeddingDimensions}}
}

func TestAIValidate(t *testing.T) {
	price := func(p, m, in, out string) AIPrice {
		return AIPrice{Provider: p, Model: m, InputPerMillion: in, OutputPerMillion: out}
	}
	cases := []struct {
		name string
		env  string
		edit func(*AIConfig)
		want string
	}{
		{"provider", EnvDevelopment, func(a *AIConfig) { a.Provider = "gemini" }, "ai.provider"},
		{"compatible without base url", EnvDevelopment, func(a *AIConfig) { a.Provider = AIProviderOpenAICompatible }, "ai.base_url"},
		{"base url scheme", EnvDevelopment, func(a *AIConfig) { a.BaseURL = "ftp://x" }, "ai.base_url"},
		{"base url http in production", EnvProduction, func(a *AIConfig) { a.BaseURL = "http://llm.example.com/v1" }, "ai.base_url"},
		{"model", EnvDevelopment, func(a *AIConfig) { a.Model = "" }, "ai.model"},
		{"embedding provider anthropic", EnvDevelopment, func(a *AIConfig) { a.Knowledge.EmbeddingProvider = "anthropic" }, "Anthropic has no embeddings API; use openai or openai-compatible"},
		{"embedding model", EnvDevelopment, func(a *AIConfig) { a.Knowledge.EmbeddingProvider = "openai" }, "ai.knowledge.embedding_model"},
		{"embedding base url", EnvDevelopment, func(a *AIConfig) {
			a.Knowledge.EmbeddingProvider, a.Knowledge.EmbeddingModel = "openai-compatible", "m"
		}, "ai.knowledge.embedding_base_url"},
		{"dimensions 0", EnvDevelopment, func(a *AIConfig) { a.Knowledge.Dimensions = 0 }, "ai.knowledge.dimensions"},
		{"dimensions 2001", EnvDevelopment, func(a *AIConfig) { a.Knowledge.Dimensions = 2001 }, "ai.knowledge.dimensions"},
		{"price words", EnvDevelopment, func(a *AIConfig) { a.Prices = []AIPrice{price("openai", "m", "cheap", "1")} }, "ai.prices[0].input_per_million"},
		{"price negative", EnvDevelopment, func(a *AIConfig) { a.Prices = []AIPrice{price("openai", "m", "1", "-1")} }, "ai.prices[0].output_per_million"},
		{"price duplicate", EnvDevelopment, func(a *AIConfig) {
			a.Prices = []AIPrice{price("openai", "m", "1", "1"), price("openai", "m", "2", "2")}
		}, "ai.prices[1]"},
		{"price provider", EnvDevelopment, func(a *AIConfig) { a.Prices = []AIPrice{price("gemini", "m", "1", "1")} }, "ai.prices[0].provider"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := validAI()
			tc.edit(&a)
			err := a.Validate(tc.env)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want an error naming %q", err, tc.want)
			}
		})
	}
	ok := validAI()
	ok.Prices = []AIPrice{price("openai", "m", "3", "0.80"), price("anthropic", "m", "3", "15.000001")}
	ok.Knowledge = AIKnowledgeConfig{EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small", Dimensions: 2000}
	if err := ok.Validate(EnvProduction); err != nil {
		t.Fatalf("valid config refused: %v", err)
	}
}

func TestAIValidateNeverEchoesTheKeys(t *testing.T) {
	a := validAI()
	a.Provider, a.APIKey = "gemini", "sk-secret-chat"
	a.Knowledge.EmbeddingAPIKey, a.Knowledge.EmbeddingProvider = "sk-secret-embed", "anthropic"
	err := a.Validate(EnvProduction)
	if err == nil {
		t.Fatal("expected errors")
	}
	if strings.Contains(err.Error(), "sk-secret") {
		t.Fatalf("error echoes a key: %v", err)
	}
}

func TestAIEnabledWithoutKeyIsValid(t *testing.T) {
	a := validAI()
	if err := a.Validate(EnvProduction); err != nil {
		t.Fatalf("enabled with no api_key refused: %v", err)
	}
}

func TestAIEnabledIsValidatedByLoad(t *testing.T) {
	_, err := FromSource(mapGetter{"database.url": testDSN, "ai.enabled": "true", "ai.provider": "bogus"})
	if err == nil || !strings.Contains(err.Error(), "ai.provider") {
		t.Fatalf("FromSource = %v, want an ai.provider error", err)
	}
}

func TestConfigYAMLWiresAIAgentVariables(t *testing.T) {
	raw, err := os.ReadFile("../../config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`  enabled: "${LOOMPLANE_AI_ENABLED:-false}"`,
		`  provider: "${LOOMPLANE_AI_PROVIDER:-anthropic}"`,
		`  api_key: "${LOOMPLANE_AI_API_KEY:-}"`,
		`  base_url: "${LOOMPLANE_AI_BASE_URL:-}"`,
		`  model: "${LOOMPLANE_AI_MODEL:-claude-sonnet-5-5}"`,
		`  prices: []`,
		`    embedding_provider: "${LOOMPLANE_AI_EMBEDDING_PROVIDER:-}"`,
		`    embedding_api_key: "${LOOMPLANE_AI_EMBEDDING_API_KEY:-}"`,
		`    embedding_base_url: "${LOOMPLANE_AI_EMBEDDING_BASE_URL:-}"`,
		`    embedding_model: "${LOOMPLANE_AI_EMBEDDING_MODEL:-}"`,
		`    dimensions: "${LOOMPLANE_AI_EMBEDDING_DIMENSIONS:-1536}"`,
	}
	inAI := false
	found := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" && line[0] != ' ' && line[0] != '#' {
			inAI = strings.TrimRight(line, " ") == "ai:"
			continue
		}
		if inAI {
			// The spec's block carries a trailing comment on one line.
			line, _, _ = strings.Cut(line, "   #")
			found[strings.TrimRight(line, " ")] = true
		}
	}
	for _, w := range want {
		if !found[w] {
			t.Errorf("config.yaml has no %q under ai:", w)
		}
	}
}
