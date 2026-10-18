package manifest

import (
	"strings"
	"testing"
)

const federationExample = `version: 2
services:
  api: {source: cmd/api, provider: kubernetes, needs: [worker]}
  worker: {source: cmd/worker, provider: kubernetes}
federation:
  destinations:
    east: {id: dest_01m4va49zwe9pvk24sm9h8v0ta}
    west: {id: dest_01m4va49zwe9pvk24sm9h8v0tb}
  network_links:
    east-west: {from: east, to: west, transport: imported}
  service_exports:
    worker-http: {service: worker, destination: west, exposure: private, port: 8080, protocol: http, identity: workload}
  access_grants:
    api-worker: {source_service: api, source_destination: east, export: worker-http, expires_at: '2027-01-01T00:00:00Z', methods: [GET], paths: [/health]}
  placements:
    api-east: {service: api, destination: east}
    worker-west: {service: worker, destination: west}
`

func TestFederationManifestV2(t *testing.T) {
	if _, err := Parse([]byte(federationExample)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, old, replacement string }{
		{"v1 rejects federation", "version: 2", "version: 1"},
		{"bad port", "port: 8080", "port: 65536"},
		{"udp", "protocol: http", "protocol: udp"},
		{"unknown source", "source_service: api", "source_service: stranger"},
		{"unknown destination", "destination: west", "destination: nowhere"},
		{"unknown field", "exposure: private", "exposure: private, token: secret"},
		{"explicit exposure", "exposure: private, ", ""},
		{"public exposure", "exposure: private", "exposure: public"},
		{"bad expiry", "2027-01-01T00:00:00Z", "tomorrow"},
		{"tcp selectors", "protocol: http", "protocol: tcp"},
		{"wildcard source", "source_service: api", "source_service: '*'"},
		{"selector too long", "paths: [/health]", "paths: [/" + strings.Repeat("a", 1025) + "]"},
		{"duplicate export", "  access_grants:", "    duplicate: {service: worker, destination: west, exposure: private, port: 8080, protocol: http, identity: workload}\n  access_grants:"},
		{"mixed links", "federation:", "links:\n  vpn: {between: [gcp, aws]}\nfederation:"},
		{"cross org", "source_service: api", "organization: other, source_service: api"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(strings.Replace(federationExample, tc.old, tc.replacement, 1))); err == nil {
				t.Fatal("accepted invalid federation intent")
			}
		})
	}
}

func TestV1RoundTripUnchanged(t *testing.T) {
	m, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}
	first, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(first), "federation:") {
		t.Fatal("v1 gained federation intent")
	}
	again, err := Parse(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Marshal(again)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("v1 changed on round trip")
	}
}

func TestFederationManifestEditor(t *testing.T) {
	doc := "# keep this comment\n" + federationExample
	region := "us-east-1"
	edited, err := WithPlacement([]byte(doc), Placement{Section: "services", Name: "api", Environment: "preview", Region: &region})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(edited), "# keep this comment") {
		t.Fatal("lost comment")
	}
	parsed, err := Parse(edited)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Version != 2 || len(parsed.Federation.AccessGrants) != 1 {
		t.Fatal("editor lost federation")
	}
}
