//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/config"
)

// The delete dialog asks what a project's deletion would remove before
// anyone types its name, and the spec the client is generated from says
// the same as the server.
func TestProjectDeletionPreview(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)

	var raw json.RawMessage
	c.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &raw)
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Required   []string                   `json:"required"`
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if op := doc.Paths["/api/v1/projects/{id}/deletion"]["get"].OperationID; op != "projects.deletion" {
		t.Fatalf("operation %q", op)
	}
	schema := doc.Components.Schemas["DeletionPreview"]
	if !slices.Equal(schema.Required, []string{"removes"}) || len(schema.Properties) != 2 {
		t.Fatalf("schema %+v", schema)
	}

	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Shop"}, &proj)
	var got struct {
		Removes []string `json:"removes"`
		Blocked *string  `json:"blocked"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+proj.ID+"/deletion", nil, &got)
	if got.Removes == nil || len(got.Removes) != 0 || got.Blocked != nil {
		t.Fatalf("a project with nothing deployed: %+v", got)
	}
	// Previewing changed nothing.
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+proj.ID, nil, nil)
	c.must(http.StatusNotFound, http.MethodGet, "/api/v1/projects/proj_missing/deletion", nil, nil)
}
