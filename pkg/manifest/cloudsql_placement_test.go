package manifest

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCloudSQLRoutePlacementEdit(t *testing.T) {
	var edit Placement
	if err := json.Unmarshal([]byte(`{"section":"services","name":"api","environment":"staging","cloud_sql_network":{"consumer_project":"app-project","network":"projects/app-project/global/networks/updated","subnetwork":"projects/app-project/regions/us-central1/subnetworks/updated"}}`), &edit); err != nil {
		t.Fatal(err)
	}
	out, err := WithPlacement([]byte("# Keep this comment\n"+applicationNetworkManifest), edit)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Services["api"].For("staging", m.Defaults).CloudSQLNetwork; got == nil || !strings.HasSuffix(got.Network, "/updated") {
		t.Fatal("placement did not save the reviewed route", got)
	}
	if !strings.Contains(string(out), "# Keep this comment") || !strings.HasSuffix(m.Services["api"].CloudSQLNetwork.Network, "/apps") {
		t.Fatal("edit changed the service route or removed comments", string(out))
	}
	if err = json.Unmarshal([]byte(`{"section":"services","name":"api","environment":"staging","cloud_sql_network":{"consumer_project":"","network":"","subnetwork":""}}`), &edit); err != nil {
		t.Fatal(err)
	}
	out, err = WithPlacement(out, edit)
	if err != nil {
		t.Fatal(err)
	}
	m, err = Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if m.Services["api"].Environments["staging"].CloudSQLNetwork != nil || !strings.HasSuffix(m.Services["api"].For("staging", m.Defaults).CloudSQLNetwork.Network, "/apps") {
		t.Fatal("clearing an override must restore the service route", string(out))
	}
}

func TestCloudSQLRoutePlacementRejectsPartialAndResourceRoutes(t *testing.T) {
	for _, raw := range []string{
		`{"section":"services","name":"api","cloud_sql_network":{"consumer_project":"app-project","network":"projects/app-project/global/networks/apps"}}`,
		`{"section":"resources","name":"db","cloud_sql_network":{"consumer_project":"app-project","network":"projects/app-project/global/networks/apps","subnetwork":"projects/app-project/regions/us-central1/subnetworks/apps"}}`,
	} {
		var edit Placement
		if err := json.Unmarshal([]byte(raw), &edit); err != nil {
			t.Fatal(err)
		}
		if _, err := WithPlacement([]byte(applicationNetworkManifest+"resources:\n  db: {kind: postgres}\n"), edit); err == nil {
			t.Fatal("invalid edit accepted", raw)
		}
	}
}

func TestCloudSQLRoutePlacementChecksEffectiveRegion(t *testing.T) {
	for _, raw := range []string{
		`{"section":"services","name":"api","environment":"production","cloud_sql_network":{"consumer_project":"app-project","network":"projects/app-project/global/networks/apps","subnetwork":"projects/app-project/regions/europe-west1/subnetworks/apps"}}`,
		`{"section":"services","name":"api","environment":"production","region":"europe-west1"}`,
	} {
		var edit Placement
		if err := json.Unmarshal([]byte(raw), &edit); err != nil {
			t.Fatal(err)
		}
		if out, err := WithPlacement([]byte(applicationNetworkManifest), edit); err == nil {
			t.Fatal("edit accepted a route outside the effective region", string(out))
		}
	}
}
