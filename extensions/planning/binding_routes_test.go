package planning

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/forge"
)

func TestBindingAliasRouteRegistered(t *testing.T) {
	r := forge.NewRouter(forge.WithOpenAPI(forge.OpenAPIConfig{Title: "Bindings", Version: "1", SpecEnabled: true}))
	if err := (&Extension{}).routes(r, nil); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/api/v1/projects/project/bindings/binding", strings.NewReader(`{"alias":"worker"}`))
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	if out.Code != http.StatusUnauthorized {
		t.Fatalf("binding alias must reach authentication: status=%d body=%s", out.Code, out.Body.String())
	}
	path := r.OpenAPISpec().Paths["/api/v1/projects/{id}/bindings/{binding}"]
	if path == nil || path.Patch == nil || path.Patch.OperationID != "bindings.setAlias" {
		t.Fatal("binding alias PATCH is missing from the registered API")
	}
}

func TestBindingConfigSchemaAllowsJSON(t *testing.T) {
	r := forge.NewRouter(forge.WithOpenAPI(forge.OpenAPIConfig{Title: "Bindings", Version: "1", SpecEnabled: true}))
	if err := (&Extension{}).routes(r, nil); err != nil {
		t.Fatal(err)
	}
	binding := r.OpenAPISpec().Components.Schemas["Binding"]
	if binding == nil || binding.Properties["config"] == nil {
		t.Fatal("binding configuration schema is missing")
	}
	if config := binding.Properties["config"]; config.Type != "" || config.Ref != "" {
		t.Fatalf("binding configuration must accept its JSON value, got type=%q ref=%q", config.Type, config.Ref)
	}
}
