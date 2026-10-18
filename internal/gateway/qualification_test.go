package gateway

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestFederationGatewayQualification runs against the generated gateway's
// exact module pins, without adding Bastion to the control plane's modules.
func TestFederationGatewayQualification(t *testing.T) {
	if testing.Short() {
		t.Skip("builds pinned gateway qualification fixture")
	}
	data, _, sourceErr := Source(routes)
	if sourceErr != nil {
		t.Fatal(sourceErr)
	}
	dir := t.TempDir()
	for name, b := range unpack(t, data) {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixture, readErr := os.ReadFile("testdata/federation_test.go.txt")
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err := os.WriteFile(filepath.Join(dir, "gateway", "federation_test.go"), fixture, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "go", "test", "-v", "-run", "TestQualification", "-count=1", ".")
	cmd.Dir = filepath.Join(dir, "gateway")
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOWORK=off")
	out, err := cmd.CombinedOutput()
	t.Log(string(out))
	if err != nil {
		t.Fatalf("pinned gateway qualification: %v", err)
	}
}
