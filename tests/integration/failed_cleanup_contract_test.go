//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

func TestFailedCleanupContract(t *testing.T) {
	_, app, c, _, _ := cloudAPISetup(t)
	defer app.stop()
	var raw json.RawMessage
	c.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &raw)
	var doc struct {
		Paths      map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/deployments/{id}/cleanup", "/api/v1/deployments/{id}/cleanup/preview", "/api/v1/deployments/{id}/cleanup/apply", "/api/v1/deployments/{id}/cleanup/retry"} {
		if doc.Paths[path] == nil {
			t.Fatal("missing cleanup route", path)
		}
	}
	for _, key := range []string{"actor", "checkpoint"} {
		if doc.Components.Schemas["FailedCleanup"].Properties[key] != nil {
			t.Fatal("private cleanup evidence in public contract", key)
		}
	}
	if path := os.Getenv("LOOMPLANE_TEST_OPENAPI_OUTPUT"); path != "" {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
