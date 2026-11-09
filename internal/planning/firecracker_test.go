package planning

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/buildpolicy"
)

func TestFirecrackerPlacementPinsCheckedRuntime(t *testing.T) {
	if !OwnedCloud("firecracker") {
		t.Fatal("Firecracker is not an owned deployment target")
	}
	details := map[string]string{"endpoint": "https://host.example:9443", "host_id": "host", "host_ca": "ca", "architecture": "amd64", "runtime_identity": strings.Repeat("a", 64), "build_isolation": "ready"}
	pinned := ReviewedConnectionSettings("firecracker", details)
	if pinned["runtime_identity"] != details["runtime_identity"] || pinned["host_id"] != "host" {
		t.Fatal("host identity missing from review")
	}
	if err := buildpolicy.CheckBackend(details, "firecracker", true); err != nil {
		t.Fatal(err)
	}
	delete(details, "runtime_identity")
	if err := buildpolicy.CheckBackend(details, "firecracker", true); err == nil {
		t.Fatal("unchecked isolation accepted")
	}
}
