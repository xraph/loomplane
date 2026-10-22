//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	lpclient "github.com/xraph/loomplane/pkg/client"
)

// This crosses the durable worker, deployment store, explicit Retry API and a
// complete app restart. Provider HTTP is controlled; it is not a live SQL drill.
func TestCloudSQLAPIRetryAfterRestartKeepsCapturedOperation(t *testing.T) {
	for _, stage := range []string{"instance", "database", "user", "remove", "instance-uncertain", "instance-reused", "database-reused"} {
		t.Run(stage, func(t *testing.T) {
			uncertain := strings.HasSuffix(stage, "-uncertain")
			reused := strings.HasSuffix(stage, "-reused")
			wantedStage := strings.TrimSuffix(strings.TrimSuffix(stage, "-uncertain"), "-reused")
			cfg := config.Defaults()
			cfg.Database.URL = freshDatabase(t)
			cfg.Orchestration.PollInterval = 100 * time.Millisecond
			cfg.Storage.URL = "file://" + t.TempDir()
			google := newFakeGoogle(t)
			cloud := newFakeCloud(google.srv.URL)
			var mu sync.Mutex
			blocked := true
			writes := map[string]int{}
			google.deploy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				prefix := "/v1/projects/acme-prod/operations/"
				if strings.HasPrefix(r.URL.Path, prefix) {
					op := strings.TrimPrefix(r.URL.Path, prefix)
					w.Header().Set("Content-Type", "application/json")
					if blocked && op == wantedStage {
						w.WriteHeader(http.StatusServiceUnavailable)
						fmt.Fprint(w, `{"error":{"message":"operation temporarily unavailable"}}`)
						return
					}
					fmt.Fprintf(w, `{"name":%q,"status":"DONE"}`, op)
					return
				}
				record := httptest.NewRecorder()
				cloud.ServeHTTP(record, r)
				if (r.Method == http.MethodPost || r.Method == http.MethodDelete) && strings.HasPrefix(r.URL.Path, "/v1/projects/acme-prod/instances") && record.Code < 300 {
					kind := "instance"
					if r.Method == http.MethodDelete {
						kind = "remove"
						if r.URL.Query().Get("enableFinalBackup") != "true" || r.URL.Query().Get("finalBackupTtlDays") != "30" {
							t.Error("native removal lost final-backup protection")
						}
					}
					if strings.HasSuffix(r.URL.Path, "/databases") {
						kind = "database"
					}
					if strings.HasSuffix(r.URL.Path, "/users") {
						kind = "user"
					}
					writes[kind]++
					if uncertain && kind == "instance" {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"status":"PENDING"}`)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"name":%q,"status":"PENDING"}`, kind)
					return
				}
				for key, values := range record.Header() {
					w.Header()[key] = values
				}
				w.WriteHeader(record.Code)
				_, _ = w.Write(record.Body.Bytes())
			})
			cfg.Clouds.GoogleEndpoint = google.srv.URL
			running := start(t, cfg)
			defer func() { running.stop() }()
			c := &client{t: t, base: running.srv.URL, key: cfg.Identity.PublishableKey}
			signedIn(t, c)
			var selected connections.Connection
			c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]string{"cloud": "gcp", "name": "Cloud SQL", "credential": serviceAccountKey(t, google.srv.URL+"/token")}, &selected)
			var key keyBody
			c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "restart catalog", "access": "write"}, &key)
			lc := loomctl{t: t, url: running.srv.URL, key: key.Secret, config: filepath.Join(t.TempDir(), "loomctl.json")}
			var created lpclient.Plan
			if err := json.Unmarshal([]byte(lc.must("services", "create", "Restart orders", "--provider", "cloud-sql", "--target", selected.ID, "--region", "europe-west1", "--json")), &created); err != nil {
				t.Fatal(err)
			}
			var plan planBody
			c.must(http.StatusOK, http.MethodGet, "/api/v1/plans/"+created.ID, nil, &plan)
			d := approveAndWait(t, c, plan)
			if stage == "remove" {
				if d.Status != "succeeded" {
					t.Fatalf("initial database creation failed: %+v", d)
				}
				base := "/api/v1/projects/" + created.ProjectID
				c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": "version: 1\nresources: {}\n"}, nil)
				c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]any{"environment": "production", "catalog": true}, &plan)
				waitPlanned(t, c, &plan)
				if !plan.Executable || len(plan.Actions) != 1 || plan.Actions[0].Op != "delete" {
					t.Fatalf("removal was not explicitly planned: %+v", plan)
				}
				d = approveAndWait(t, c, plan)
			}
			expectedError := "operation temporarily unavailable"
			if uncertain {
				expectedError = "uncertain request"
			}
			if d.Status != "failed" || !strings.Contains(d.Error, expectedError) {
				t.Fatalf("expected captured operation read failure: %+v", d)
			}
			db, err := pgx.Connect(t.Context(), cfg.Database.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close(t.Context())
			var raw []byte
			if err = db.QueryRow(t.Context(), "SELECT steps FROM loomplane_deployments WHERE id=$1 AND project_id=$2", d.ID, created.ProjectID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var steps []struct {
				Checkpoint []byte `json:"checkpoint"`
			}
			if err = json.Unmarshal(raw, &steps); err != nil {
				t.Fatal(err)
			}
			if len(steps) != 1 {
				t.Fatal("native operation has no deployment step")
			}
			checkpointRaw := steps[0].Checkpoint
			if stage != "remove" {
				var envelope struct {
					Apply   []byte `json:"apply"`
					Replace bool   `json:"replace"`
				}
				if json.Unmarshal(checkpointRaw, &envelope) != nil || !envelope.Replace {
					t.Fatal("native operation was not saved in the deployment step")
				}
				checkpointRaw = envelope.Apply
			}
			var checkpoint struct {
				Version    int    `json:"cloud_sql_checkpoint"`
				Stage      string `json:"stage"`
				Operation  string `json:"operation"`
				Uncertain  bool   `json:"uncertain"`
				ResourceID string `json:"resource_id"`
			}
			wantedOperation := wantedStage
			if uncertain {
				wantedOperation = ""
			}
			if err = json.Unmarshal(checkpointRaw, &checkpoint); err != nil || checkpoint.Version != 2 || checkpoint.Stage != wantedStage || checkpoint.Operation != wantedOperation || !checkpoint.Uncertain {
				t.Fatalf("saved step lost its captured operation: %s %v", checkpointRaw, err)
			}
			if !uncertain && checkpoint.ResourceID == "" {
				t.Fatal("operation polling started without a durable incarnation pin")
			}
			if stage == "remove" {
				var stillBound struct {
					Bindings []struct {
						ExternalID string `json:"external_id"`
					} `json:"bindings"`
				}
				c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+created.ProjectID+"/bindings?environment=production", nil, &stillBound)
				if len(stillBound.Bindings) != 1 || stillBound.Bindings[0].ExternalID == "" {
					t.Fatal("failed protected removal lost its binding before completion")
				}
			}
			running.stop()
			mu.Lock()
			blocked = false
			if reused {
				cloud.mu.Lock()
				for _, in := range cloud.sql {
					in["createTime"] = "2026-10-21T01:00:00Z"
				}
				cloud.mu.Unlock()
			}
			mu.Unlock()
			running = start(t, cfg)
			c.base = running.srv.URL
			c.must(http.StatusOK, http.MethodPost, "/api/v1/deployments/"+d.ID+"/retry", nil, &d)
			deadline := time.Now().Add(readyTimeout)
			for d.Status == "running" {
				if time.Now().After(deadline) {
					t.Fatalf("retry did not finish: %+v", d)
				}
				time.Sleep(100 * time.Millisecond)
				c.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+d.ID, nil, &d)
			}
			if uncertain {
				if d.Status != "failed" || !strings.Contains(d.Error, "uncertain request") {
					t.Fatalf("uncertain request was replayed: %+v", d)
				}
				mu.Lock()
				defer mu.Unlock()
				if writes["instance"] != 1 || writes["database"] != 0 || writes["user"] != 0 {
					t.Fatalf("lost operation identity allowed another mutation: %v", writes)
				}
				return
			}
			if reused {
				if d.Status != "failed" || !strings.Contains(d.Error, "incarnation") {
					t.Fatalf("reused name resumed saved operation: %+v", d)
				}
				mu.Lock()
				defer mu.Unlock()
				if writes["instance"] != 1 || writes["user"] != 0 || (wantedStage == "instance" && writes["database"] != 0) || (wantedStage == "database" && writes["database"] != 1) {
					t.Fatalf("reused name received mutations after restart: %v", writes)
				}
				return
			}
			if d.Status != "succeeded" {
				t.Fatalf("saved operation did not resume: %+v", d)
			}
			var archivedPin, archivedBodyPin string
			var hasURL bool
			if err = db.QueryRow(t.Context(), "SELECT physical_id,source->>'physical_id',source ? 'url' FROM loomplane_native_sources WHERE project_id=$1 AND provider='cloud-sql'", created.ProjectID).Scan(&archivedPin, &archivedBodyPin, &hasURL); err != nil || archivedPin != checkpoint.ResourceID || archivedBodyPin != checkpoint.ResourceID || hasURL {
				t.Fatalf("native source archive lost scoped creation identity: pin=%q body=%q url=%v err=%v", archivedPin, archivedBodyPin, hasURL, err)
			}
			if stage == "remove" {
				var after struct {
					Bindings []struct{} `json:"bindings"`
				}
				c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+created.ProjectID+"/bindings?environment=production", nil, &after)
				if len(after.Bindings) != 0 {
					t.Fatal("completed protected removal retained a live binding")
				}
			}
			if stage != "remove" {
				var bound struct {
					Bindings []struct {
						PhysicalID string `json:"physical_id"`
					} `json:"bindings"`
				}
				c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+created.ProjectID+"/bindings?environment=production", nil, &bound)
				if len(bound.Bindings) != 1 || bound.Bindings[0].PhysicalID != checkpoint.ResourceID {
					t.Fatal("binding lost checkpointed creation identity")
				}
			}
			mu.Lock()
			defer mu.Unlock()
			for _, kind := range []string{"instance", "database", "user"} {
				if writes[kind] != 1 {
					t.Fatalf("restart/retry repeated %s mutation: %v", kind, writes)
				}
			}
			cloud.mu.Lock()
			defer cloud.mu.Unlock()
			expectedInstances := 1
			if stage == "remove" {
				expectedInstances = 0
				if writes["remove"] != 1 {
					t.Fatalf("restart repeated protected removal: %v", writes)
				}
			}
			if len(cloud.sql) != expectedInstances || len(cloud.passwords) != 1 {
				t.Fatal("resumed database has no single instance and application credential")
			}
		})
	}
}
