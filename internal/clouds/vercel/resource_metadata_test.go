package vercel

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProvisioningMetadataParsesNonSecretFlatOptions(t *testing.T) {
	got, err := ParseProvisioningMetadata(`{"region":"iad1","replicas":2,"enabled":true,"regions":["iad1","fra1"],"sizes":[1,2]}`)
	if err != nil || got["region"] != "iad1" || got["replicas"] != json.Number("2") || got["enabled"] != true {
		t.Fatalf("metadata parse: %v", err)
	}
	encoded, err := json.Marshal(got)
	if err != nil || !strings.Contains(string(encoded), `"regions":["iad1","fra1"]`) {
		t.Fatalf("metadata arrays were lost: %v", err)
	}
}

func TestProvisioningMetadataRejectsAmbiguousNestedOrCredentialValues(t *testing.T) {
	for _, raw := range []string{`{"region":"iad1","region":"fra1"}`, `{"options":{"region":"iad1"}}`, `{"region":null}`, `{"regions":[null]}`, `{"regions":[["iad1"]]}`, `{"password":"private"}`, `{"apiToken":"private"}`, `{"api_key":"private"}`, `{"secret":"private"}`, `{"credentials":"private"}`, `{"connection_string":"private"}`, `{"region":"postgres://user:private@host/db"}`, `{"regions":["https://user:private@example.test"]}`, `{"region":"https://example.test?token=private"}`, `{"bad.key":"iad1"}`, `null`, `[]`, `{} {}`, strings.Repeat(" ", 8193) + `{}`} {
		if _, err := ParseProvisioningMetadata(raw); err == nil {
			t.Fatal("unsafe provisioning metadata was accepted")
		}
	}
}

func TestProvisioningMetadataRejectsMalformedAndOpaqueCredentialURIs(t *testing.T) {
	for _, raw := range []string{`{"endpoint":"postgres://app:pass%word@db.test/main"}`, `{"endpoint":"https://example.test?token=private%zz"}`, `{"endpoint":"https://example.test#access_token=private"}`, `{"endpoint":"jdbc:postgresql://app:private@host/db"}`} {
		if _, err := ParseProvisioningMetadata(raw); err == nil {
			t.Fatal("credential-bearing URI was accepted")
		}
	}
}
