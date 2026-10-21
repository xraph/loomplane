package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFederationLaunchRequiresIdentityAndRuntime(t *testing.T) {
	c := FederationConfig{Enabled: true, Identity: FederationIdentityConfig{Enabled: true, OrganizationID: "org", TrustDomain: "loomplane.test", AdminSocket: "unix:///run/spire/server.sock"}, Runtime: FederationRuntimeConfig{Enabled: true, OrganizationID: "org", TrustDomain: "loomplane.test", ListenAddress: "127.0.0.1:9443", WorkloadAPI: "unix:///run/spire/agent.sock", SigningKeysFile: "/run/keys.json"}, Launch: FederationLaunchConfig{Enabled: true, ControlEndpoint: "https://control.example:9443", AgentImage: "registry.example/agent@sha256:" + strings.Repeat("a", 64), TrustGeneration: 1}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*FederationConfig){
		"runtime":       func(c *FederationConfig) { c.Runtime.Enabled = false },
		"identity":      func(c *FederationConfig) { c.Identity.Enabled = false },
		"http":          func(c *FederationConfig) { c.Launch.ControlEndpoint = "http://control.example" },
		"userinfo":      func(c *FederationConfig) { c.Launch.ControlEndpoint = "https://user:pass@control.example" },
		"path":          func(c *FederationConfig) { c.Launch.ControlEndpoint = "https://control.example/management" },
		"mutable image": func(c *FederationConfig) { c.Launch.AgentImage = "agent:latest" },
		"generation":    func(c *FederationConfig) { c.Launch.TrustGeneration = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			copyConfig := c
			change(&copyConfig)
			if copyConfig.Validate() == nil {
				t.Fatal("unsafe launch configuration accepted")
			}
		})
	}
	loaded, err := FromSource(mapGetter{"database.url": testDSN, "federation.launch.control_endpoint": c.Launch.ControlEndpoint, "federation.launch.agent_image": c.Launch.AgentImage, "federation.launch.trust_generation": int64(7)})
	if err != nil || loaded.Federation.Launch.Enabled || loaded.Federation.Launch.ControlEndpoint != c.Launch.ControlEndpoint || loaded.Federation.Launch.AgentImage != c.Launch.AgentImage || loaded.Federation.Launch.TrustGeneration != 7 {
		t.Fatal("launch values not read with default-off configuration", err)
	}
}

func TestFederationLaunchEnvironmentConfiguration(t *testing.T) {
	body, err := os.ReadFile("../../config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if writeErr := os.WriteFile(filepath.Join(dir, "config.yaml"), body, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	for key, value := range map[string]string{"LOOMPLANE_DATABASE_URL": testDSN, "LOOMPLANE_FEDERATION_LAUNCH_CONTROL_ENDPOINT": "https://control.example:9443", "LOOMPLANE_FEDERATION_LAUNCH_AGENT_IMAGE": "registry.example/agent@sha256:" + strings.Repeat("a", 64), "LOOMPLANE_FEDERATION_LAUNCH_TRUST_GENERATION": "3"} {
		t.Setenv(key, value)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Federation.Launch.Enabled || cfg.Federation.Launch.TrustGeneration != 3 || cfg.Federation.Launch.ControlEndpoint != "https://control.example:9443" || !strings.Contains(cfg.Federation.Launch.AgentImage, "@sha256:") {
		t.Fatal("launch environment values not loaded")
	}
}
