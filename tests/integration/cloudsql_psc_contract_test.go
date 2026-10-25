//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/config"
)

func TestCloudSQLPSCReviewedEndpointPublishedContract(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	app := start(t, cfg)
	defer app.stop()
	c := &client{t: t, base: app.srv.URL, key: cfg.Identity.PublishableKey}
	for _, route := range []string{"/openapi.json", "/asyncapi.json"} {
		var raw json.RawMessage
		c.must(http.StatusOK, http.MethodGet, route, nil, &raw)
		var spec struct {
			Components struct {
				Schemas map[string]struct {
					Properties map[string]map[string]any `json:"properties"`
					Required   []string                  `json:"required"`
				} `json:"schemas"`
			} `json:"components"`
		}
		if err := json.Unmarshal(raw, &spec); err != nil {
			t.Fatal(err)
		}
		if route == "/openapi.json" {
			placement := spec.Components.Schemas["EditInputBody"].Properties["placement"]
			properties, _ := placement["properties"].(map[string]any)
			network, _ := properties["cloud_sql_network"].(map[string]any)
			if network["$ref"] != "#/components/schemas/CloudSQLApplicationNetwork" {
				t.Fatal("missing application route placement edit", placement)
			}
			credentials := spec.Components.Schemas["NativeCopyCredentials"]
			if credentials.Properties["private_service_connect"]["type"] != "boolean" || slices.Contains(credentials.Required, "private_service_connect") {
				t.Fatal("missing optional PSC credential transport")
			}
		}
		binding := spec.Components.Schemas["Binding"]
		if binding.Properties["owned_resources"]["type"] != "object" || slices.Contains(binding.Required, "owned_resources") {
			t.Fatal(route, "missing optional binding ownership object", binding.Properties["owned_resources"])
		}
		service := spec.Components.Schemas["ServiceConfig"]
		if service.Properties["cloud_sql_network"]["$ref"] != "#/components/schemas/CloudSQLApplicationNetwork" || slices.Contains(service.Required, "cloud_sql_network") {
			t.Fatal(route, "missing optional application route review")
		}
		applicationNetwork := spec.Components.Schemas["CloudSQLApplicationNetwork"]
		for _, field := range []string{"consumer_project", "network", "subnetwork"} {
			if applicationNetwork.Properties[field]["type"] != "string" || !slices.Contains(applicationNetwork.Required, field) {
				t.Fatal(route, "incomplete application route contract", field)
			}
		}
		managed := spec.Components.Schemas["ManagedDatabaseConfig"]
		if managed.Properties["cloud_sql_psc"]["$ref"] != "#/components/schemas/CloudSQLSourceEndpoint" || slices.Contains(managed.Required, "cloud_sql_psc") {
			t.Fatal(route, "missing optional endpoint review")
		}
		endpoint := spec.Components.Schemas["CloudSQLSourceEndpoint"]
		for _, field := range []string{"consumer_project", "network", "forwarding_rule"} {
			if endpoint.Properties[field]["type"] != "string" || !slices.Contains(endpoint.Required, field) {
				t.Fatal(route, "incomplete endpoint contract", field)
			}
		}
		if dir := os.Getenv("LOOMPLANE_TEST_CONTRACT_OUTPUT"); dir != "" {
			if err := os.WriteFile(filepath.Join(dir, filepath.Base(route)), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
