package agentinstall

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Keep the operator example on the same resource builder as controller delivery.
func TestKubernetesExampleManifest(t *testing.T) {
	p, err := NewKubernetesPlan("workloads", "01234567-89ab-4def-8123-456789abcdef", "registry.example/loomplane-agent@sha256:"+strings.Repeat("0", 64), true)
	if err != nil {
		t.Fatal(err)
	}
	role, binding := p.ObserverRBAC()
	var body bytes.Buffer
	body.WriteString("# Example only. Replace the namespace, attempt UUID and image digest before use.\n# Create a fresh attempt for every process. The pod waits for trusted launch delivery.\n# Regenerate from internal/agentinstall; see docs/federation-agent.md.\n")
	for i, obj := range []any{p.ServiceAccount(), role, binding, p.Pod()} {
		if i > 0 {
			body.WriteString("---\n")
		}
		raw, marshalErr := yaml.Marshal(obj)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		body.Write(raw)
	}
	path := filepath.Join("..", "..", "deployments", "federation", "agent.yaml")
	if os.Getenv("LOOMPLANE_UPDATE_AGENT_MANIFEST") == "1" {
		if writeErr := os.WriteFile(path, body.Bytes(), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, body.Bytes()) {
		t.Fatal("agent manifest differs from the installer resource builder; regenerate the example")
	}
}
