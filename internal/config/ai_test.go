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
