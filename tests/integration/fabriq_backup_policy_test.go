//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/recovery"
)

func assertFabriqDefaultBackups(t *testing.T, f catalogFixture, c *client, project, provider string) {
	t.Helper()
	base := "/api/v1/projects/" + project
	var policies struct {
		Policies []recovery.Policy `json:"policies"`
	}
	c.must(http.StatusOK, http.MethodGet, base+"/backup-policies", nil, &policies)
	if provider == "kubernetes" {
		if len(policies.Policies) != 0 {
			t.Fatalf("unqualified Kubernetes schedule: %+v", policies)
		}
		return
	}
	if len(policies.Policies) != 1 {
		t.Fatalf("default backup policy missing: %+v", policies)
	}
	policy := policies.Policies[0]
	if policy.Resource != "fabriq--postgres" || policy.EveryHours != 24 || !policy.Enabled || policy.CreatedBy == "" || policy.Environment != "production" || !policy.NextAt.After(time.Now().Add(23*time.Hour)) {
		t.Fatalf("default schedule: %+v", policy)
	}
	// Move only this disposable project's policy due, then let the real cron
	// request and execute it through Dispatch and the database backup adapter.
	db, err := forge.InjectType[*grove.DB](f.running.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pgdriver.Unwrap(db).Exec(t.Context(), `UPDATE loomplane_backup_policies SET next_at=now()-interval '1 minute' WHERE id=$1 AND project_id=$2`, policy.ID, project); err != nil {
		t.Fatal(err)
	}
	var scheduled struct {
		Backups []recovery.Backup `json:"backups"`
	}
	for deadline := time.Now().Add(100 * time.Second); ; {
		c.must(http.StatusOK, http.MethodGet, base+"/service-backups", nil, &scheduled)
		for _, b := range scheduled.Backups {
			if b.Name != "fabriq--postgres" {
				continue
			}
			if b.Status == "failed" {
				t.Fatalf("default scheduled backup failed: %+v", b)
			}
			if b.Status == "succeeded" {
				if b.Bytes == 0 || b.CreatedBy != policy.CreatedBy {
					t.Fatalf("scheduled evidence: %+v", b)
				}
				c.must(http.StatusOK, http.MethodGet, base+"/backup-policies", nil, &policies)
				if len(policies.Policies) != 1 || policies.Policies[0].LastError != "" || !policies.Policies[0].NextAt.After(time.Now()) {
					t.Fatalf("schedule did not advance: %+v", policies)
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("default schedule did not execute: %+v", scheduled)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestFabriqBackupSchedulePublishedContract(t *testing.T) {
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
				} `json:"schemas"`
			} `json:"components"`
		}
		if err := json.Unmarshal(raw, &spec); err != nil {
			t.Fatal(err)
		}
		if spec.Components.Schemas["Action"].Properties["backup_every_hours"]["type"] != "integer" {
			t.Fatalf("%s omits reviewed schedule", route)
		}
		if dir := os.Getenv("LOOMPLANE_TEST_CONTRACT_OUTPUT"); dir != "" {
			if err := os.WriteFile(filepath.Join(dir, filepath.Base(route)), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
