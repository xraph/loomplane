//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/projects"
)

func TestCloudSQLRoutePlacementPersistsThroughAPI(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	app := start(t, cfg)
	c := &client{t: t, base: app.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "route@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Routes", "slug": "routes"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var project projects.Project
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Routes"}, &project)
	base := "/api/v1/projects/" + project.ID + "/manifest"
	const yaml = "# Keep service settings\nversion: 1\nservices:\n  api:\n    source: go:cmd/api\n    provider: cloud-run\n    region: us-central1\n"
	var revision manifests.Revision
	c.must(http.StatusCreated, http.MethodPut, base, map[string]string{"yaml": yaml}, &revision)
	route := map[string]string{"consumer_project": "app-project", "network": "projects/app-project/global/networks/apps", "subnetwork": "projects/app-project/regions/us-central1/subnetworks/apps"}
	edit := map[string]any{"placement": map[string]any{"section": "services", "name": "api", "environment": "production", "cloud_sql_network": route}}
	c.must(http.StatusCreated, http.MethodPost, base+"/edits", edit, &revision)
	if revision.Revision != 2 || revision.Manifest.Services["api"].Environments["production"].CloudSQLNetwork == nil {
		t.Fatal("route was not saved", revision)
	}
	c.must(http.StatusOK, http.MethodPost, base+"/edits", edit, &revision)
	if revision.Revision != 2 {
		t.Fatal("identical route created a revision", revision.Revision)
	}
	app.stop()
	app = start(t, cfg)
	defer app.stop()
	c.base = app.srv.URL
	c.must(http.StatusOK, http.MethodGet, base, nil, &revision)
	settings := revision.Manifest.Services["api"].For("production", revision.Manifest.Defaults)
	if settings.CloudSQLNetwork == nil || settings.CloudSQLNetwork.Subnetwork != route["subnetwork"] || !strings.Contains(revision.YAML, "# Keep service settings") {
		t.Fatal("route or comments lost after restart", revision.YAML)
	}
	route["subnetwork"] = "projects/app-project/regions/europe-west1/subnetworks/apps"
	c.must(http.StatusUnprocessableEntity, http.MethodPost, base+"/edits", edit, nil)
	c.must(http.StatusOK, http.MethodGet, base, nil, &revision)
	if revision.Revision != 2 {
		t.Fatal("invalid route wrote a revision", revision.Revision)
	}
	anonymous := &client{t: t, base: app.srv.URL, key: cfg.Identity.PublishableKey}
	anonymous.must(http.StatusUnauthorized, http.MethodPost, base+"/edits", edit, nil)
	edit["placement"].(map[string]any)["cloud_sql_network"] = map[string]string{"consumer_project": "", "network": "", "subnetwork": ""}
	c.must(http.StatusCreated, http.MethodPost, base+"/edits", edit, &revision)
	if revision.Manifest.Services["api"].For("production", revision.Manifest.Defaults).CloudSQLNetwork != nil {
		t.Fatal("route removal not persisted", revision.YAML)
	}
}
