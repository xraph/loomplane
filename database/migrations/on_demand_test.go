package migrations

import (
	"testing"

	"github.com/xraph/weave/vectorstore/pgvector"

	"github.com/xraph/loomplane/internal/config"
)

type settings map[string]any

func (s settings) Get(key string) any { return s[key] }

// loaded is config.yaml's AI block as Loomplane reads it.
func loaded(t *testing.T, ai settings) config.Config {
	t.Helper()
	ai["database.url"] = "postgres://u:p@localhost:5432/db?sslmode=disable"
	cfg, err := config.FromSource(ai)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

// With AI off, an ai.knowledge block left in config.yaml turns nothing on
// and isn't read: an out-of-range size, which only an AI-on server
// validates, doesn't make OnDemand (and so every ops command) fail.
func TestOnDemandWithAIOffIgnoresKnowledge(t *testing.T) {
	cfg := loaded(t, settings{
		"ai.enabled": false, "ai.knowledge.embedding_provider": config.AIProviderOpenAI,
		"ai.knowledge.embedding_model": "text-embedding-3-small", "ai.knowledge.dimensions": 5000,
	})
	if !cfg.AI.Knowledge.Configured() || cfg.AI.Knowledge.Dimensions != 5000 {
		t.Fatalf("the leftover knowledge block didn't load: %+v", cfg.AI.Knowledge)
	}
	groups, err := OnDemand(cfg.AI)
	if err != nil || len(groups) != 0 {
		t.Fatalf("AI off: on-demand groups %v, %v", groups, err)
	}
}

func TestOnDemandNeedsAIAndKnowledge(t *testing.T) {
	on := config.AIConfig{Enabled: true, Knowledge: config.AIKnowledgeConfig{
		EmbeddingProvider: config.AIProviderOpenAI, EmbeddingModel: "text-embedding-3-small", Dimensions: 768,
	}}
	groups, err := OnDemand(on)
	if err != nil || len(groups) != 1 || groups[0].Name() != pgvector.GroupName {
		t.Fatalf("AI and knowledge on: %v, %v", groups, err)
	}
	unconfigured := on
	unconfigured.Knowledge = config.AIKnowledgeConfig{Dimensions: 768}
	if groups, err = OnDemand(unconfigured); err != nil || len(groups) != 0 {
		t.Fatalf("AI on, knowledge unconfigured: %v, %v", groups, err)
	}
	off := on
	off.Enabled = false
	if groups, err = OnDemand(off); err != nil || len(groups) != 0 {
		t.Fatalf("AI off, knowledge configured: %v, %v", groups, err)
	}
}
