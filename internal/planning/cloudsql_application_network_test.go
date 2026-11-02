package planning

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestCloudSQLApplicationNetworkReviewPreservesRoute(t *testing.T) {
	in := managedCatalogInput(t, "cloud-sql")
	in.Graph = polyglot(t)
	route := &provider.CloudSQLApplicationNetwork{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps", Subnetwork: "projects/app-project/regions/europe-west1/subnetworks/run"}
	in.Manifest.Services = map[string]manifest.Service{"api": {Source: "services/api", Settings: manifest.Settings{Provider: "cloud-run", Region: "europe-west1", CloudSQLNetwork: route}, Needs: []string{"database"}}}
	r := in.Manifest.Resources["database"]
	r.CloudSQLPSC = &provider.CloudSQLSourceEndpoint{ConsumerProject: route.ConsumerProject, Network: route.Network, ForwardingRule: "projects/app-project/regions/europe-west1/forwardingRules/database"}
	in.Manifest.Resources["database"] = r
	result := Compute(in)
	api := findAction(result, "service:api")
	if api == nil || api.Service == nil {
		t.Fatal("service review absent", result.Problems)
	}
	route.Network = "projects/app-project/global/networks/replaced"
	raw, err := json.Marshal(api.Service)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"cloud_sql_network":{"consumer_project":"app-project","network":"projects/app-project/global/networks/apps","subnetwork":"projects/app-project/regions/europe-west1/subnetworks/run"}`) {
		t.Fatal("reviewed route lost or aliases manifest", string(raw))
	}
	if !result.Executable {
		t.Fatal("reviewed PSC application rejected", result.Problems)
	}
}

func TestCloudSQLApplicationNetworkReviewRejectsWrongRegion(t *testing.T) {
	in := managedCatalogInput(t, "cloud-sql")
	in.Graph = polyglot(t)
	in.Manifest.Services = map[string]manifest.Service{"api": {Source: "services/api", Settings: manifest.Settings{Provider: "cloud-run", Region: "europe-west1", CloudSQLNetwork: &provider.CloudSQLApplicationNetwork{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps", Subnetwork: "projects/app-project/regions/us-central1/subnetworks/run"}}}}
	result := Compute(in)
	if result.Executable || !slices.ContainsFunc(result.Problems, func(f Finding) bool { return f.Code == "cloud-sql-network-invalid" }) {
		t.Fatal("route ignored instead of checking effective placement", result.Problems)
	}
}

func TestCloudSQLApplicationRouteAdmission(t *testing.T) {
	for _, scenario := range []string{"accepted", "missing-route", "wrong-network", "wrong-region", "fractional-cpu", "unused-route", "memorystore"} {
		t.Run(scenario, func(t *testing.T) {
			in := managedCatalogInput(t, "cloud-sql")
			in.Graph = polyglot(t)
			route := &provider.CloudSQLApplicationNetwork{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps", Subnetwork: "projects/app-project/regions/europe-west1/subnetworks/run"}
			service := manifest.Service{Source: "services/api", Settings: manifest.Settings{Provider: "cloud-run", Region: "europe-west1", CloudSQLNetwork: route}, Needs: []string{"database"}}
			resource := in.Manifest.Resources["database"]
			resource.CloudSQLPSC = &provider.CloudSQLSourceEndpoint{ConsumerProject: route.ConsumerProject, Network: route.Network, ForwardingRule: "projects/app-project/regions/europe-west1/forwardingRules/database"}
			in.Manifest.Resources["database"] = resource
			switch scenario {
			case "missing-route":
				service.CloudSQLNetwork = nil
			case "wrong-network":
				route.Network += "-other"
			case "wrong-region":
				service.Region = "us-central1"
				route.Subnetwork = "projects/app-project/regions/us-central1/subnetworks/run"
			case "fractional-cpu":
				service.Size = &manifest.Size{CPU: 0.5, Memory: 512}
			case "unused-route":
				service.Needs = nil
			case "memorystore":
				in.Manifest.Resources["cache"] = manifest.Resource{Kind: "redis", Provider: "memorystore", Region: "europe-west1"}
				service.Needs = append(service.Needs, "cache")
			}
			in.Manifest.Services = map[string]manifest.Service{"api": service}
			result := Compute(in)
			if result.Executable != (scenario == "accepted") {
				t.Fatal("incorrect application admission", result.Problems)
			}
		})
	}
}
