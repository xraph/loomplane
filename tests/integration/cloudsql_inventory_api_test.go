//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/recovery"
	lpclient "github.com/xraph/loomplane/pkg/client"
)

func TestNativeCloudSQLInventoryKeepsPinnedHistoryAcrossRemovalAndRestart(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	google := newFakeGoogle(t)
	cloud := newFakeCloud(google.srv.URL)
	cfg.Clouds.GoogleEndpoint = google.srv.URL
	var mu sync.Mutex
	mode := "original"
	reads, instanceReads := 0, 0
	var originalSource map[string]any
	google.deploy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/projects/acme-prod/backups") {
			mu.Lock()
			defer mu.Unlock()
			reads++
			if r.Method != http.MethodGet {
				t.Errorf("inventory sent mutation: %s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			if mode == "denied" {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"error":{"message":"private provider diagnostic"}}`)
				return
			}
			raw, _ := json.Marshal(originalSource)
			var proof map[string]any
			if err := json.Unmarshal(raw, &proof); err != nil {
				t.Error(err)
			}
			if mode == "foreign-project" {
				proof["project"] = "another"
			}
			point := func(name string) map[string]any {
				return map[string]any{"name": "projects/acme-prod/backups/" + name, "kind": "sql#backup", "instance": originalSource["name"], "type": "FINAL", "state": "SUCCESSFUL", "databaseVersion": "POSTGRES_17", "instanceSettings": proof, "backupInterval": map[string]string{"startTime": "2026-10-21T02:00:00Z", "endTime": "2026-10-21T02:00:01Z"}}
			}
			first := point("daily")
			if mode == "contradictory-instance" {
				first["instance"] = "other"
			}
			if mode == "malformed-instance" {
				first["instance"] = "database/other"
			}
			if mode == "wrong-engine" {
				proof["databaseVersion"] = "MYSQL_8_4"
				first["databaseVersion"] = "MYSQL_8_4"
			}
			if mode == "no-proof" {
				delete(first, "instanceSettings")
			}
			if strings.HasPrefix(r.URL.Path, "/v1/projects/acme-prod/backups/") {
				_ = json.NewEncoder(w).Encode(first)
				return
			}
			if r.URL.Query().Get("filter") != "instance:"+originalSource["name"].(string) {
				t.Errorf("inventory lost source filter: %s", r.URL)
			}
			body := map[string]any{"backups": []any{first}}
			if mode == "warning-empty" {
				body["backups"] = []any{}
				body["warnings"] = []any{map[string]string{"code": "REGION_UNREACHABLE"}}
			}
			if mode == "empty" {
				body["backups"] = []any{}
			}
			if mode == "reused" {
				replacement := point("replacement")
				replacementProof := map[string]any{}
				for key, value := range proof {
					replacementProof[key] = value
				}
				replacementProof["createTime"] = "2026-10-21T01:00:00Z"
				replacement["instanceSettings"] = replacementProof
				body["backups"] = []any{first, replacement}
			}
			_ = json.NewEncoder(w).Encode(body)
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/projects/acme-prod/instances/") {
			mu.Lock()
			instanceReads++
			mu.Unlock()
		}
		cloud.ServeHTTP(w, r)
	})
	running := start(t, cfg)
	defer func() { running.stop() }()
	c := &client{t: t, base: running.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var connection connections.Connection
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]string{"cloud": "gcp", "name": "Native Cloud SQL", "credential": serviceAccountKey(t, google.srv.URL+"/token")}, &connection)
	var key keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "Native Google CLI", "access": "write"}, &key)
	lc := loomctl{t: t, url: running.srv.URL, key: key.Secret, config: filepath.Join(t.TempDir(), "loomctl.json")}
	var plan lpclient.Plan
	if err := json.Unmarshal([]byte(lc.must("services", "create", "Native Google orders", "--provider", "cloud-sql", "--target", connection.ID, "--region", "europe-west1", "--json")), &plan); err != nil {
		t.Fatal(err)
	}
	lc.must("approve", plan.ID, "--yes")
	cloud.mu.Lock()
	for _, instance := range cloud.sql {
		raw, err := json.Marshal(instance)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &originalSource); err != nil {
			t.Fatal(err)
		}
	}
	cloud.mu.Unlock()
	if originalSource == nil {
		t.Fatal("service did not provision original source")
	}
	base := "/api/v1/projects/" + plan.ProjectID
	var sources recovery.NativeSourcesPage
	c.must(http.StatusOK, http.MethodGet, base+"/native-recovery-sources", nil, &sources)
	if len(sources.Sources) != 1 || sources.Sources[0].PhysicalID == "" || sources.Sources[0].Provider != "cloud-sql" {
		t.Fatalf("original source not captured: %+v", sources)
	}
	source := sources.Sources[0]
	path := "/api/v1/native-recovery-sources/" + source.ID
	var inventory recovery.NativeInventory
	c.must(http.StatusOK, http.MethodPost, path+"/refresh", nil, &inventory)
	if !inventory.Source.Complete || len(inventory.Points) != 1 || !inventory.Points[0].Encrypted || inventory.Points[0].Engine != "postgres" || inventory.Points[0].Version != "17" {
		t.Fatalf("native backup missing: %+v", inventory)
	}
	original := inventory.Points[0].ID
	if !strings.Contains(lc.must("backups", "native-inventory", source.ID, "--json"), original) || !strings.Contains(lc.must("backups", "native-refresh", source.ID, "--json"), original) {
		t.Fatal("shared CLI lost Cloud SQL backup")
	}
	for _, partial := range []string{"no-proof", "warning-empty"} {
		mu.Lock()
		mode = partial
		mu.Unlock()
		c.must(http.StatusOK, http.MethodPost, path+"/refresh", nil, &inventory)
		if inventory.Source.Complete || inventory.Source.State != "ready" || len(inventory.Points) != 1 || inventory.Points[0].ID != original || !inventory.Points[0].Present {
			t.Fatalf("partial scan erased prior observation: %+v", inventory)
		}
	}
	for _, failure := range []string{"denied", "foreign-project", "wrong-engine", "contradictory-instance", "malformed-instance"} {
		mu.Lock()
		mode = failure
		mu.Unlock()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, c.base+path+"/refresh", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Publishable-Key", c.key)
		req.Header.Set("Authorization", "Bearer "+c.token)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("native failure status: %d", response.StatusCode)
		}
		c.must(http.StatusOK, http.MethodGet, path, nil, &inventory)
		if inventory.Source.State != "failed" || inventory.Source.Complete || len(inventory.Points) != 1 || inventory.Points[0].ID != original || strings.Contains(inventory.Source.Error, "private") {
			t.Fatal("failed scan erased history or leaked provider diagnostic")
		}
	}
	mu.Lock()
	mode = "reused"
	mu.Unlock()
	c.must(http.StatusOK, http.MethodPost, path+"/refresh", nil, &inventory)
	if !inventory.Source.Complete || len(inventory.Points) != 1 || inventory.Points[0].ID != original {
		t.Fatal("new instance backup entered original source history")
	}
	// Remove the service through the real plan and native protected-delete path.
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": "version: 1\nresources: {}\n"}, nil)
	var removal planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]any{"environment": "production", "catalog": true}, &removal)
	waitPlanned(t, c, &removal)
	if !removal.Executable || len(removal.Actions) != 1 || removal.Actions[0].Op != "delete" {
		t.Fatalf("no explicit native removal plan: %+v", removal)
	}
	if d := approveAndWait(t, c, removal); d.Status != "succeeded" {
		t.Fatalf("protected native removal failed: %+v", d)
	}
	running.stop()
	running = start(t, cfg)
	c.base, lc.url = running.srv.URL, running.srv.URL
	mu.Lock()
	beforeReads := instanceReads
	mu.Unlock()
	c.must(http.StatusOK, http.MethodPost, path+"/refresh", nil, &inventory)
	mu.Lock()
	afterReads := instanceReads
	mu.Unlock()
	if beforeReads != afterReads || inventory.Source.PhysicalID != source.PhysicalID || len(inventory.Points) != 1 || inventory.Points[0].ID != original {
		t.Fatal("deleted original required reusable instance lookup or lost its history")
	}
	mu.Lock()
	mode = "empty"
	mu.Unlock()
	c.must(http.StatusOK, http.MethodPost, path+"/refresh", nil, &inventory)
	if !inventory.Source.Complete || len(inventory.Points) != 1 || inventory.Points[0].Present {
		t.Fatal("complete empty scan lost retained history")
	}
	mu.Lock()
	mode = "original"
	mu.Unlock()
	if !strings.Contains(lc.must("backups", "native-refresh", source.ID, "--json"), original) {
		t.Fatal("restarted CLI lost retained point identity")
	}
	// Source authorization happens before cloud credentials or provider reads.
	other := &client{t: t, base: c.base, key: c.key}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	other.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "foreign-cloudsql@example.com", "password": "Correct-horse-battery-9"}, &auth)
	other.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	other.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Other Google", "slug": "other-google"}, &org)
	other.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	mu.Lock()
	before := reads
	mu.Unlock()
	other.must(http.StatusNotFound, http.MethodGet, path, nil, nil)
	other.must(http.StatusNotFound, http.MethodPost, path+"/refresh", nil, nil)
	mu.Lock()
	defer mu.Unlock()
	if reads != before {
		t.Fatal("foreign organization reached native backup API")
	}
}
