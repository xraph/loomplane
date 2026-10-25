package manifest

import (
	"encoding/json"
	"strings"
	"testing"
)

const applicationNetworkManifest = `version: 1
defaults: {provider: cloud-run, region: us-central1}
services:
  api:
    source: services/api
    cloud_sql_network:
      consumer_project: app-project
      network: projects/app-project/global/networks/apps
      subnetwork: projects/app-project/regions/us-central1/subnetworks/run
    environments:
      staging:
        cloud_sql_network:
          consumer_project: app-project
          network: projects/app-project/global/networks/staging
          subnetwork: projects/app-project/regions/us-central1/subnetworks/staging
      preview: {provider: render}
`

func TestCloudSQLApplicationNetworkManifestRoundTrip(t *testing.T) {
	m, err := Parse([]byte(applicationNetworkManifest))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	for env, network := range map[string]string{"production": "apps", "staging": "staging", "preview": ""} {
		settings := restored.Services["api"].For(env, restored.Defaults)
		encoded, err := json.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		route := string(fields["cloud_sql_network"])
		if network == "" {
			if route != "" {
				t.Fatal("provider override retained Cloud Run route", route)
			}
			continue
		}
		if !strings.Contains(route, `"network":"projects/app-project/global/networks/`+network+`"`) {
			t.Fatal("route lost on roundtrip/environment override", env, route)
		}
	}
}

func TestCloudSQLApplicationNetworkManifestRejectsInvalidRoutes(t *testing.T) {
	for _, change := range []struct{ from, to string }{
		{"consumer_project: app-project", "consumer_project: other-project"},
		{"subnetwork: projects/app-project/regions/us-central1/subnetworks/run", "subnetwork: https://example.com/subnet"},
		{"network: projects/app-project/global/networks/apps", "network: projects/app-project/global/networks/../apps"},
	} {
		_, err := Parse([]byte(strings.Replace(applicationNetworkManifest, change.from, change.to, 1)))
		if err == nil {
			t.Fatal("invalid route accepted", change.to)
		}
	}
}

func TestCloudSQLApplicationNetworkEnvironmentDoesNotAliasManifest(t *testing.T) {
	m, err := Parse([]byte(applicationNetworkManifest))
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"production", "staging"} {
		settings := m.Services["api"].For(env, m.Defaults)
		original := *settings.CloudSQLNetwork
		settings.CloudSQLNetwork.Network = "changed"
		again := m.Services["api"].For(env, m.Defaults)
		if *again.CloudSQLNetwork != original {
			t.Fatal("resolved settings mutated the saved manifest", env)
		}
	}
}

func TestCloudSQLApplicationNetworkFirstProviderSelection(t *testing.T) {
	raw := strings.Replace(applicationNetworkManifest, "defaults: {provider: cloud-run, region: us-central1}\n", "", 1)
	raw = strings.Replace(raw, "staging:\n        cloud_sql_network:", "staging:\n        provider: cloud-run\n        region: us-central1\n        cloud_sql_network:", 1)
	// The environment selects placement but inherits the service route.
	raw = raw[:strings.Index(raw, "        cloud_sql_network:")]
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	got := m.Services["api"].For("staging", m.Defaults)
	if got.CloudSQLNetwork == nil || got.CloudSQLNetwork.Network != "projects/app-project/global/networks/apps" {
		t.Fatal("first Cloud Run selection lost inherited route", got)
	}
}
