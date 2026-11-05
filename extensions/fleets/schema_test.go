package fleets

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
)

func TestFleetOpenAPIContracts(t *testing.T) {
	r := forge.NewRouter(forge.WithOpenAPI(forge.OpenAPIConfig{Title: "Fleet contracts", Version: "1", SpecEnabled: true}))
	if err := r.GET("/existing-cloud-operation", func(c forge.Context) error { return c.JSON(http.StatusOK, cloudinfra.Operation{}) }, forge.WithResponseSchema(http.StatusOK, "Cloud operation", cloudinfra.Operation{})); err != nil {
		t.Fatal(err)
	}
	if err := r.GET("/existing-cloud-profile", func(c forge.Context) error { return c.JSON(http.StatusOK, cloudprofiles.Profile{}) }, forge.WithResponseSchema(http.StatusOK, "Cloud profile", cloudprofiles.Profile{})); err != nil {
		t.Fatal(err)
	}
	if err := (&Extension{svc: routeFixture{}}).routes(r, nil); err != nil {
		t.Fatal(err)
	}
	document := r.OpenAPISpec()
	if document.Components.Schemas["Operation"] == nil {
		t.Error("existing cloud Operation schema changed name")
	}
	if document.Components.Schemas["Profile"] == nil {
		t.Error("cloud and fleet profile schema names must stay distinct")
	}
	if document.Components.Schemas["FleetOperation"] == nil {
		t.Error("fleet operation needs its own stable name")
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			Responses map[string]any `json:"responses"`
		} `json:"paths"`
	}
	if err = json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/projects/{project_id}/fleet/instances"
	if _, ok := spec.Paths[path]["post"].Responses["202"]; !ok {
		t.Fatal("create contract must describe asynchronous acceptance")
	}
	for _, status := range []string{"409", "422", "429"} {
		if _, ok := spec.Paths[path]["post"].Responses[status]; !ok {
			t.Errorf("create contract missing %s response", status)
		}
	}
	for _, field := range []string{"idempotency_key", "observed_generation", "next_cursor", "external_ref"} {
		if !strings.Contains(string(raw), `"`+field+`"`) {
			t.Errorf("missing wire contract %s", field)
		}
	}
}
