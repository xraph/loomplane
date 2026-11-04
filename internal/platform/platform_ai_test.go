package platform

import (
	"slices"
	"testing"

	cortexext "github.com/xraph/cortex/extension"
	nexusext "github.com/xraph/nexus/extension"
	shieldext "github.com/xraph/shield/extension"
	weaveext "github.com/xraph/weave/extension"
	"github.com/xraph/weave/vectorstore/pgvector"

	"github.com/xraph/loomplane/extensions/agentwatch"
	aiext "github.com/xraph/loomplane/extensions/ai"
	"github.com/xraph/loomplane/extensions/aiagent"
	"github.com/xraph/loomplane/extensions/identity"
	notificationsext "github.com/xraph/loomplane/extensions/notifications"
	planningext "github.com/xraph/loomplane/extensions/planning"
	releaseprepext "github.com/xraph/loomplane/extensions/releaseprep"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/orchestration"
)

// names lists the extensions' names in the order Extensions returns them.
func names(cfg config.Config) []string {
	exts := Extensions(cfg)
	out := make([]string, 0, len(exts))
	for _, ext := range exts {
		out = append(out, ext.Name())
	}
	return out
}

var libraries = []string{nexusext.ExtensionName, shieldext.ExtensionName, weaveext.ExtensionName, cortexext.ExtensionName, aiagent.Name}

func TestExtensionsWithoutAI(t *testing.T) {
	got := names(config.Defaults())
	for _, name := range libraries {
		if slices.Contains(got, name) {
			t.Errorf("AI is off and %s is registered", name)
		}
	}
	if !slices.Contains(got, aiext.OffName) {
		t.Errorf("AI is off and the AI-off extension isn't registered: %v", got)
	}
	if slices.Contains(queues(config.Defaults()), aiagent.Queue) {
		t.Errorf("AI is off and Dispatch works the %s queue", aiagent.Queue)
	}
}

func TestExtensionsWithAI(t *testing.T) {
	cfg := config.Defaults()
	cfg.AI.Enabled = true
	got := names(cfg)
	for _, name := range []string{nexusext.ExtensionName, shieldext.ExtensionName, cortexext.ExtensionName, aiagent.Name, aiext.Name} {
		if !slices.Contains(got, name) {
			t.Errorf("AI is on and %s isn't registered: %v", name, got)
		}
	}
	if slices.Contains(got, weaveext.ExtensionName) {
		t.Error("knowledge is off and weave is registered")
	}
	if slices.Contains(got, aiext.OffName) {
		t.Error("AI is on and the AI-off extension is registered")
	}
	if q := queues(cfg); !slices.Contains(q, aiagent.Queue) || !slices.Contains(q, orchestration.Queue) {
		t.Errorf("queues %v", q)
	}

	cfg.AI.Knowledge.EmbeddingProvider = config.AIProviderOpenAI
	cfg.AI.Knowledge.EmbeddingModel = "text-embedding-3-small"
	got = names(cfg)
	weave, cortex := slices.Index(got, weaveext.ExtensionName), slices.Index(got, cortexext.ExtensionName)
	if weave < 0 || cortex < 0 || weave > cortex {
		t.Fatalf("with knowledge: weave at %d, cortex at %d: %v", weave, cortex, got)
	}
	// Each library waits for what it reads from the container: the
	// database, and nexus and shield (and weave) for cortex's adapters.
	for _, ext := range Extensions(cfg) {
		deps := ext.Dependencies()
		switch ext.Name() {
		case cortexext.ExtensionName:
			for _, d := range []string{"grove", nexusext.ExtensionName, shieldext.ExtensionName, weaveext.ExtensionName} {
				if !slices.Contains(deps, d) {
					t.Errorf("cortex doesn't wait for %s: %v", d, deps)
				}
			}
		case nexusext.ExtensionName, shieldext.ExtensionName:
			if !slices.Contains(deps, "grove") {
				t.Errorf("%s doesn't wait for the database: %v", ext.Name(), deps)
			}
		case aiagent.Name:
			for _, d := range libraries[:4] {
				if !slices.Contains(deps, d) {
					t.Errorf("the agent doesn't wait for %s: %v", d, deps)
				}
			}
			if !slices.Contains(deps, aiext.Name) {
				t.Errorf("the agent doesn't wait for the AI section: %v", deps)
			}
		}
	}
}

// The watcher exists only with AI on, and comes after the agent, which
// provides the reports, runs and binder it works through.
func TestWatchExtensionOnlyWithAI(t *testing.T) {
	if got := names(config.Defaults()); slices.Contains(got, agentwatch.Name) {
		t.Fatalf("AI is off and the watcher is registered: %v", got)
	}
	cfg := config.Defaults()
	cfg.AI.Enabled = true
	got := names(cfg)
	watch, agent := slices.Index(got, agentwatch.Name), slices.Index(got, aiagent.Name)
	if watch < 0 || agent < 0 || watch < agent {
		t.Fatalf("AI is on: the watcher at %d, the agent at %d: %v", watch, agent, got)
	}
	for _, ext := range Extensions(cfg) {
		if ext.Name() == agentwatch.Name && !slices.Contains(ext.Dependencies(), aiagent.Name) {
			t.Fatalf("the watcher doesn't wait for the agent: %v", ext.Dependencies())
		}
	}
}

// Release preparation exists only with AI on, after the agent, whose
// runs, requests, binder and authorizer it works through. With AI off the
// AI-off extension answers its jobs and adds nothing else; the routes and
// the settle cron are the extension's, so with it gone they are too.
func TestReleasePreparationOnlyWithAI(t *testing.T) {
	off := names(config.Defaults())
	if slices.Contains(off, releaseprepext.Name) {
		t.Fatalf("AI is off and release preparation is registered: %v", off)
	}
	if !slices.Contains(off, aiext.OffName) {
		t.Fatalf("AI is off and nothing answers its jobs: %v", off)
	}
	cfg := config.Defaults()
	cfg.AI.Enabled = true
	got := names(cfg)
	prep, agent, watch := slices.Index(got, releaseprepext.Name), slices.Index(got, aiagent.Name), slices.Index(got, agentwatch.Name)
	if prep < 0 || agent < 0 || prep < agent || prep < watch {
		t.Fatalf("AI is on: release preparation at %d, the agent at %d, the watcher at %d: %v", prep, agent, watch, got)
	}
	for _, ext := range Extensions(cfg) {
		if ext.Name() != releaseprepext.Name {
			continue
		}
		for _, d := range []string{aiagent.Name, planningext.Name, notificationsext.Name, identity.Name} {
			if !slices.Contains(ext.Dependencies(), d) {
				t.Errorf("release preparation doesn't wait for %s: %v", d, ext.Dependencies())
			}
		}
	}
}

func TestEmbeddingLimitMatchesPgvector(t *testing.T) {
	if config.MaxEmbeddingDimensions != pgvector.MaxIndexedDimensions {
		t.Fatalf("config allows %d dimensions, pgvector indexes %d", config.MaxEmbeddingDimensions, pgvector.MaxIndexedDimensions)
	}
}
