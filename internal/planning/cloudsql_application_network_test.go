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
	if result.Executable {
		t.Fatal("provider wiring alone must not enable unfinished app integration")
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
