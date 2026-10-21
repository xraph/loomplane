package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFederationRuntimeRequiresExplicitConfiguration(t *testing.T) {
	defaults, err := FromSource(mapGetter{"database.url": testDSN, "federation.enabled": true})
	if err != nil || defaults.Federation.Runtime.Enabled {
		t.Fatal("management enabled runtime", err)
	}
	input := mapGetter{"database.url": testDSN, "federation.enabled": true, "federation.runtime.enabled": true, "federation.runtime.listen_address": "127.0.0.1:9443", "federation.runtime.organization_id": "org", "federation.runtime.trust_domain": "loomplane.test", "federation.runtime.workload_api": "unix:///run/spire/agent.sock", "federation.runtime.signing_keys_file": "/run/secrets/federation-keys.json"}
	configured, err := FromSource(input)
	if err != nil || !configured.Federation.Runtime.Enabled || configured.Federation.Runtime.OrganizationID != "org" {
		t.Fatal("valid runtime rejected", err)
	}
	for key, value := range map[string]any{"federation.enabled": false, "federation.runtime.listen_address": "https://control:9443", "federation.runtime.organization_id": "../org", "federation.runtime.trust_domain": "spiffe://loomplane.test", "federation.runtime.workload_api": "http://spire/agent", "federation.runtime.signing_keys_file": "relative.json"} {
		t.Run(key, func(t *testing.T) {
			copyInput := mapGetter{}
			for k, v := range input {
				copyInput[k] = v
			}
			copyInput[key] = value
			if _, configErr := FromSource(copyInput); configErr == nil {
				t.Fatal("unsafe runtime accepted")
			}
		})
	}
}

func TestFederationRuntimeEnvironmentConfiguration(t *testing.T) {
	body, err := os.ReadFile("../../config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if writeErr := os.WriteFile(filepath.Join(dir, "config.yaml"), body, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	for key, value := range map[string]string{
		"LOOMPLANE_DATABASE_URL":                         testDSN,
		"LOOMPLANE_FEDERATION_ENABLED":                   "true",
		"LOOMPLANE_FEDERATION_RUNTIME_ENABLED":           "true",
		"LOOMPLANE_FEDERATION_RUNTIME_LISTEN_ADDRESS":    "127.0.0.1:9443",
		"LOOMPLANE_FEDERATION_RUNTIME_ORGANIZATION_ID":   "org",
		"LOOMPLANE_FEDERATION_RUNTIME_TRUST_DOMAIN":      "loomplane.test",
		"LOOMPLANE_FEDERATION_RUNTIME_WORKLOAD_API":      "unix:///run/spire/agent.sock",
		"LOOMPLANE_FEDERATION_RUNTIME_SIGNING_KEYS_FILE": "/run/secrets/keys.json",
	} {
		t.Setenv(key, value)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := FederationRuntimeConfig{Enabled: true, ListenAddress: "127.0.0.1:9443", OrganizationID: "org", TrustDomain: "loomplane.test", WorkloadAPI: "unix:///run/spire/agent.sock", SigningKeysFile: "/run/secrets/keys.json"}
	if !cfg.Federation.Enabled || cfg.Federation.Runtime != want {
		t.Fatal("runtime environment configuration was not loaded")
	}
}

func TestFederationIdentityConfiguration(t *testing.T) {
	input := mapGetter{"database.url": testDSN, "federation.enabled": true, "federation.identity.enabled": true, "federation.identity.organization_id": "org", "federation.identity.trust_domain": "loomplane.test", "federation.identity.admin_socket": "unix:///run/spire/server.sock"}
	cfg, err := FromSource(input)
	if err != nil || !cfg.Federation.Identity.Enabled || cfg.Federation.Identity.OrganizationID != "org" || cfg.Federation.Runtime.Enabled {
		t.Fatal("cleanup configuration failed", err)
	}
	for key, value := range map[string]any{"federation.enabled": false, "federation.identity.organization_id": "../org", "federation.identity.trust_domain": "spiffe://loomplane.test", "federation.identity.admin_socket": "http://spire:8081"} {
		t.Run(key, func(t *testing.T) {
			copyInput := mapGetter{}
			for k, v := range input {
				copyInput[k] = v
			}
			copyInput[key] = value
			if _, configErr := FromSource(copyInput); configErr == nil {
				t.Fatal("unsafe identity config accepted")
			}
		})
	}
	defaults, err := FromSource(mapGetter{"database.url": testDSN, "federation.enabled": true})
	if err != nil || defaults.Federation.Identity.Enabled {
		t.Fatal("identity admin enabled implicitly", err)
	}
	cfg.Federation.Runtime = FederationRuntimeConfig{Enabled: true, ListenAddress: "127.0.0.1:9443", OrganizationID: "foreign", TrustDomain: "loomplane.test", WorkloadAPI: "unix:///run/spire/agent.sock", SigningKeysFile: "/run/keys.json"}
	if err := cfg.Federation.Validate(); err == nil {
		t.Fatal("cross-organization identity/runtime accepted")
	}
	cfg.Federation.Runtime.OrganizationID = "org"
	cfg.Federation.Runtime.TrustDomain = "foreign.test"
	if err := cfg.Federation.Validate(); err == nil {
		t.Fatal("cross-domain identity/runtime accepted")
	}
	cfg.Federation.Runtime.TrustDomain = "loomplane.test"
	if err := cfg.Federation.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestFederationIdentityEnvironmentConfiguration(t *testing.T) {
	body, err := os.ReadFile("../../config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if writeErr := os.WriteFile(filepath.Join(dir, "config.yaml"), body, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	for key, value := range map[string]string{"LOOMPLANE_DATABASE_URL": testDSN, "LOOMPLANE_FEDERATION_ENABLED": "true", "LOOMPLANE_FEDERATION_IDENTITY_ENABLED": "true", "LOOMPLANE_FEDERATION_IDENTITY_ORGANIZATION_ID": "org", "LOOMPLANE_FEDERATION_IDENTITY_TRUST_DOMAIN": "loomplane.test", "LOOMPLANE_FEDERATION_IDENTITY_ADMIN_SOCKET": "unix:///run/spire/server.sock"} {
		t.Setenv(key, value)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := FederationIdentityConfig{Enabled: true, OrganizationID: "org", TrustDomain: "loomplane.test", AdminSocket: "unix:///run/spire/server.sock"}
	if cfg.Federation.Identity != want {
		t.Fatal("identity environment configuration was not loaded")
	}
}
