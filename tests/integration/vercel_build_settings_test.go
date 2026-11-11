//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/manifests"
)

func TestVercelBuildSettingsPersistThroughManifestAPI(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	firstAccount(t, c, "ada@example.com", "Correct-horse-battery-9")
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var project struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Monorepo"}, &project)
	base := "/api/v1/projects/" + project.ID + "/manifest"
	c.must(http.StatusCreated, http.MethodPut, base, map[string]string{"yaml": "version: 1\nservices:\n  app: {source: apps/web, provider: vercel}\n"}, nil)
	c.must(http.StatusCreated, http.MethodPost, base+"/edits", map[string]any{"placement": map[string]any{
		"section": "services", "name": "app", "environment": "preview", "vercel": map[string]any{
			"services": true, "root": ".", "service_commands": map[string]any{"web": map[string]string{"install": "", "build": "pnpm build:preview"}},
		},
	}}, nil)
	var revision manifests.Revision
	c.must(http.StatusOK, http.MethodGet, base, nil, &revision)
	service := revision.Manifest.Services["app"]
	preview := service.For("preview", revision.Manifest.Defaults)
	if preview.Vercel == nil || !preview.Vercel.Services || preview.Vercel.ServiceCommands["web"].Install == nil || *preview.Vercel.ServiceCommands["web"].Install != "" || *preview.Vercel.ServiceCommands["web"].Build != "pnpm build:preview" {
		t.Fatalf("saved settings: %+v", preview)
	}
	if service.For("production", revision.Manifest.Defaults).Vercel != nil {
		t.Fatal("preview edit changed production")
	}
	c.must(http.StatusUnprocessableEntity, http.MethodPost, base+"/edits", map[string]any{"placement": map[string]any{
		"section": "services", "name": "app", "vercel": map[string]any{"services": true, "root": "../outside"},
	}}, nil)
}
