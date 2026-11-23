package deployments

import (
	"testing"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

// TestBuilderBackendFor pins which backend builds each kind of service, and
// what Build does around it: the order the backends were tried in when they
// were branches in Build.
func TestBuilderBackendFor(t *testing.T) {
	managedCluster := map[string]string{"registry_hostname": "registry.example.com"}
	for _, tc := range []struct {
		name                           string
		cfg                            planning.ServiceConfig
		want                           string
		whole, platformRegistry, gated bool
	}{
		{"managed kubernetes", planning.ServiceConfig{Provider: "kubernetes", TargetSettings: managedCluster, BuildTarget: &planning.BuildTarget{}}, "kubernetes", true, false, false},
		{"kubernetes with its own builder", planning.ServiceConfig{Provider: "kubernetes", TargetSettings: managedCluster, BuildTarget: &planning.BuildTarget{Builder: "conn_builder", Registry: "conn_registry"}}, "docker", false, false, true},
		{"loomplane cloud", planning.ServiceConfig{BuildTarget: &planning.BuildTarget{BuilderCloud: platformProvider, Builder: platformProvider}}, "platform", false, true, false},
		{"loomplane cloud with a pinned image", planning.ServiceConfig{BuildTarget: &planning.BuildTarget{BuilderCloud: platformProvider, Builder: platformProvider, Image: "registry.example.com/app@sha256:" + sha64}}, "docker", false, false, true},
		{"firecracker builder", planning.ServiceConfig{Provider: "server", BuildTarget: &planning.BuildTarget{BuilderCloud: connections.Firecracker, Builder: "conn_fc", Registry: "conn_registry"}}, "firecracker", false, false, true},
		{"runner pool", planning.ServiceConfig{Provider: "server", BuildTarget: &planning.BuildTarget{BuilderCloud: connections.Runner, Builder: "conn_pool", Registry: "conn_registry"}}, "runner", false, false, true},
		{"CodeBuild in an AWS account", planning.ServiceConfig{Provider: "server", BuildTarget: &planning.BuildTarget{BuilderCloud: connections.AWS, Builder: "conn_aws", Registry: "conn_ecr"}}, "codebuild", false, false, true},
		{"docker server builder", planning.ServiceConfig{Provider: "server", BuildTarget: &planning.BuildTarget{Builder: "conn_server", Registry: "conn_registry"}}, "docker", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := builderBackendFor(&tc.cfg)
			if got.name != tc.want || got.whole != tc.whole || got.platformRegistry != tc.platformRegistry || got.gated != tc.gated {
				t.Fatalf("backend = %s (whole %v, platform registry %v, gated %v), want %s (whole %v, platform registry %v, gated %v)",
					got.name, got.whole, got.platformRegistry, got.gated, tc.want, tc.whole, tc.platformRegistry, tc.gated)
			}
		})
	}
}

const sha64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestECRHost(t *testing.T) {
	for endpoint, ok := range map[string]bool{
		"https://123456789012.dkr.ecr.us-east-1.amazonaws.com": true,
		"https://ghcr.io": false,
		"https://123456789012.dkr.ecr.us-east-1.amazonaws.com.evil.example": false,
		"https://dkr.ecr.us-east-1.amazonaws.com":                           false,
	} {
		account, region, got := ecrHost(endpoint)
		if got != ok || (ok && (account != "123456789012" || region != "us-east-1")) {
			t.Errorf("%s: %q %q %v", endpoint, account, region, got)
		}
	}
}
