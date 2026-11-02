//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

func TestManifest_editsFromAPlanKeepTheComments(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	r := start(t, cfg)
	defer r.stop()

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	firstAccount(t, c, "ada@example.com", "Correct-horse-battery-9")
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID

	// No manifest yet: nothing to edit.
	c.must(http.StatusNotFound, http.MethodPost, base+"/manifest/edits", map[string]any{"ignore": "kafka"}, nil)

	const yaml = "# Ours, with notes.\nversion: 1\nservices:\n  # The API.\n  api:\n    source: go:services/api # its module\n    provider: cloud-run\n"
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": yaml}, nil)

	type revision struct {
		Revision int    `json:"revision"`
		YAML     string `json:"yaml"`
	}
	var rev revision
	c.must(http.StatusCreated, http.MethodPost, base+"/manifest/edits",
		map[string]any{"add_resource": map[string]string{"name": "db", "kind": "postgres", "provider": "cloud-sql"}}, &rev)
	c.must(http.StatusCreated, http.MethodPost, base+"/manifest/edits", map[string]any{"ignore": "kafka"}, &rev)
	for _, want := range []string{"# Ours, with notes.", "# The API.", "# its module", "db:", "kind: postgres", "kafka"} {
		if !strings.Contains(rev.YAML, want) {
			t.Errorf("the edited manifest lacks %q:\n%s", want, rev.YAML)
		}
	}
	if rev.Revision != 3 {
		t.Fatalf("revision %d", rev.Revision)
	}
	// Ignoring it again changes nothing.
	c.must(http.StatusOK, http.MethodPost, base+"/manifest/edits", map[string]any{"ignore": "kafka"}, &rev)

	c.must(http.StatusUnprocessableEntity, http.MethodPost, base+"/manifest/edits",
		map[string]any{"add_resource": map[string]string{"name": "api", "kind": "redis"}}, nil)
	c.must(http.StatusUnprocessableEntity, http.MethodPost, base+"/manifest/edits", map[string]any{}, nil)

	c.must(http.StatusCreated, http.MethodPost, base+"/manifest/edits", map[string]any{"placement": map[string]any{
		"section": "services", "name": "api", "environment": "preview", "provider": "ecs-fargate", "region": "us-east-1", "enabled": false,
	}}, &rev)
	for _, want := range []string{"# its module", "provider: cloud-run", "preview:", "provider: ecs-fargate", "enabled: false"} {
		if !strings.Contains(rev.YAML, want) {
			t.Errorf("placement edit lost %q: %s", want, rev.YAML)
		}
	}
	c.must(http.StatusUnprocessableEntity, http.MethodPost, base+"/manifest/edits", map[string]any{"placement": map[string]string{
		"section": "services", "name": "missing", "provider": "vercel",
	}}, nil)
	c.must(http.StatusUnprocessableEntity, http.MethodPost, base+"/manifest/edits", map[string]any{"ignore": "redis", "placement": map[string]string{
		"section": "services", "name": "api", "provider": "vercel",
	}}, nil)

	// A raw editor that read revision 1 must not overwrite later placement edits.
	c.must(http.StatusConflict, http.MethodPut, base+"/manifest", map[string]any{"yaml": yaml, "base_revision": 1}, nil)
	var latest revision
	c.must(http.StatusOK, http.MethodGet, base+"/manifest", nil, &latest)
	if latest.Revision != rev.Revision || latest.YAML != rev.YAML {
		t.Fatal("a stale raw save overwrote the latest placement")
	}
}
