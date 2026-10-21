package config

import "testing"

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
			if _, err := FromSource(copyInput); err == nil {
				t.Fatal("unsafe runtime accepted")
			}
		})
	}
}
