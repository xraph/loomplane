package config

import (
	"os"
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
