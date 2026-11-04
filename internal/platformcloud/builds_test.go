package platformcloud

import (
	"strings"
	"testing"

	fc "github.com/xraph/loomplane/internal/firecracker"
)

// A build host is checked against the platform policy, and the refusal a
// workspace reads never names the host.
func TestCheckBuildHost(t *testing.T) {
	caps := fc.Capabilities{HostID: "hw-build-1", Architecture: "amd64", RuntimeIdentity: strings.Repeat("a", 64), IngressAddress: "203.0.113.7",
		Isolation: true, Platform: true, DeniedPorts: []int{25, 465, 587}, Limits: fc.Limits{PerOrg: fc.OrgLimits{VMs: 1, VCPUs: 1, MemoryMiB: 1}}}
	if err := CheckBuildHost(t.Context(), "org_a", caps); err != nil {
		t.Fatal(err)
	}
	caps.SMTActive = true
	err := CheckBuildHost(t.Context(), "org_a", caps)
	if err == nil {
		t.Fatal("a host with SMT on passed")
	}
	for _, leak := range []string{"hw-build-1", "203.0.113.7", "SMT"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("the refusal names %q: %v", leak, err)
		}
	}
}
