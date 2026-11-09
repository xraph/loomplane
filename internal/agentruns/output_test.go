package agentruns

import (
	"testing"

	cortexagent "github.com/xraph/cortex/agent"
	"github.com/xraph/cortex/execution"
)

// The API caps one call's output (Anthropic at 128,000 tokens for
// claude-sonnet-5-5). A task's whole token budget is far larger, so it must
// never reach a call as max_tokens, or the first call of every chat fails.
func TestAModelCallAsksForTheOutputCapNotTheTaskBudget(t *testing.T) {
	spec := ChatSpec()
	var cfg cortexagent.Config
	(&Service{}).describe(&cfg, spec, nil, "def")
	if cfg.MaxTokens != defaultMaxOutputTokens {
		t.Fatalf("the chat agent's per-call max_tokens = %d, want %d", cfg.MaxTokens, defaultMaxOutputTokens)
	}
	// What cortex sends on the first call of a task on the default budget.
	first := execution.Budget{MaxTokens: spec.DefaultMaxTokens}.OutputLimit(cfg.MaxTokens, execution.Usage{})
	if first > 128_000 {
		t.Fatalf("the first call asks for %d output tokens, over the API's 128000 cap", first)
	}

	spec.MaxOutputTokens = 4_096
	(&Service{}).describe(&cfg, spec, nil, "def")
	if cfg.MaxTokens != 4_096 {
		t.Fatalf("a kind's own output cap = %d, want 4096", cfg.MaxTokens)
	}
}
