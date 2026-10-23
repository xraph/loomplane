//go:build integration

package integration

import (
	"encoding/base64"
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

func sqlStepCheckpoint(t *testing.T, raw []byte) (map[string]any, []map[string]any, map[string]json.RawMessage) {
	t.Helper()
	var steps []map[string]any
	if err := json.Unmarshal(raw, &steps); err != nil || len(steps) != 1 {
		t.Fatalf("missing durable native step: %s %v", raw, err)
	}
	encoded, ok := steps[0]["checkpoint"].(string)
	if !ok {
		t.Fatal("native step has no checkpoint")
	}
	envelope, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(envelope, &wrapper); err != nil {
		t.Fatal(err)
	}
	var checkpoint []byte
	if err := json.Unmarshal(wrapper["apply"], &checkpoint); err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(checkpoint, &state); err != nil {
		t.Fatal(err)
	}
	return state, steps, wrapper
}

func TestCloudSQLLostCreateDiscoveryAcrossAPIRestart(t *testing.T) {
	for _, scenario := range []string{"original", "ambiguous", "reused", "replacement-history", "proof-save-failure", "v2-unknown", "v2-captured"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Database.URL = freshDatabase(t)
			cfg.Storage.URL = "file://" + t.TempDir()
			cfg.Orchestration.PollInterval = 100 * time.Millisecond
			google := newFakeGoogle(t)
			cloud := newFakeCloud(google.srv.URL)
			cfg.Clouds.GoogleEndpoint = google.srv.URL
			var mu sync.Mutex
			blocked := true
			writes, lists := map[string]int{}, 0
			var queued time.Time
			instance := ""
			operation := func() map[string]any {
				return map[string]any{"kind": "sql#operation", "name": "original-create", "targetId": instance, "targetProject": "acme-prod", "operationType": "CREATE", "status": "DONE", "user": "loomplane@acme-prod.iam.gserviceaccount.com", "insertTime": queued.Format(time.RFC3339Nano), "startTime": queued.Add(time.Millisecond).Format(time.RFC3339Nano), "endTime": queued.Add(3 * time.Millisecond).Format(time.RFC3339Nano)}
			}
			google.deploy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.URL.Path == "/v1/projects/acme-prod/operations" {
					lists++
					w.Header().Set("Content-Type", "application/json")
					if blocked {
						w.WriteHeader(http.StatusServiceUnavailable)
						fmt.Fprint(w, `{"error":{"message":"discovery temporarily unavailable"}}`)
						return
					}
					points := []any{operation()}
					if scenario == "ambiguous" {
						second := operation()
						second["name"] = "another-create"
						points = append(points, second)
					}
					if scenario == "replacement-history" {
						second := operation()
						second["name"] = "replacement-create"
						second["insertTime"] = queued.Add(time.Hour - time.Millisecond).Format(time.RFC3339Nano)
						second["startTime"] = queued.Add(time.Hour).Format(time.RFC3339Nano)
						second["endTime"] = queued.Add(time.Hour + time.Millisecond).Format(time.RFC3339Nano)
						points = append(points, second)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"kind": "sql#operationsList", "items": points})
					return
				}
				if r.URL.Path == "/v1/projects/acme-prod/operations/original-create" || r.URL.Path == "/v1/projects/acme-prod/operations/replacement-create" {
					w.Header().Set("Content-Type", "application/json")
					if blocked {
						w.WriteHeader(http.StatusServiceUnavailable)
						fmt.Fprint(w, `{"error":{"message":"operation temporarily unavailable"}}`)
						return
					}
					point := operation()
					if strings.HasSuffix(r.URL.Path, "/replacement-create") {
						point["name"] = "replacement-create"
						point["insertTime"] = queued.Add(time.Hour - time.Millisecond).Format(time.RFC3339Nano)
						point["startTime"] = queued.Add(time.Hour).Format(time.RFC3339Nano)
						point["endTime"] = queued.Add(time.Hour + time.Millisecond).Format(time.RFC3339Nano)
					}
					_ = json.NewEncoder(w).Encode(point)
					return
				}
				record := httptest.NewRecorder()
				cloud.ServeHTTP(record, r)
				if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/projects/acme-prod/instances") && record.Code < 300 {
					stage := "instance"
					if strings.HasSuffix(r.URL.Path, "/databases") {
						stage = "database"
					}
					if strings.HasSuffix(r.URL.Path, "/users") {
						stage = "user"
					}
					writes[stage]++
					if stage == "instance" {
						queued = time.Now().UTC()
						cloud.mu.Lock()
						for name, in := range cloud.sql {
							instance = name
							in["createTime"] = queued.Add(2 * time.Millisecond).Format(time.RFC3339Nano)
						}
						cloud.mu.Unlock()
						w.Header().Set("Content-Type", "application/json")
						if scenario == "v2-captured" {
							fmt.Fprint(w, `{"name":"original-create","status":"PENDING"}`)
						} else {
							fmt.Fprint(w, `{"status":"PENDING"}`)
						}
						return
					}
				}
				for key, values := range record.Header() {
					w.Header()[key] = values
				}
				w.WriteHeader(record.Code)
				_, _ = w.Write(record.Body.Bytes())
			})
			running := start(t, cfg)
			defer func() { running.stop() }()
			c := &client{t: t, base: running.srv.URL, key: cfg.Identity.PublishableKey}
			signedIn(t, c)
			var selected connections.Connection
			c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]string{"cloud": "gcp", "name": "Lost create", "credential": serviceAccountKey(t, google.srv.URL+"/token")}, &selected)
			var key keyBody
			c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]string{"name": "Lost create CLI", "access": "write"}, &key)
			lc := loomctl{t: t, url: running.srv.URL, key: key.Secret, config: filepath.Join(t.TempDir(), "loomctl.json")}
			var created lpclient.Plan
			if err := json.Unmarshal([]byte(lc.must("services", "create", "Lost orders", "--provider", "cloud-sql", "--target", selected.ID, "--region", "europe-west1", "--json")), &created); err != nil {
				t.Fatal(err)
			}
			var plan planBody
			c.must(http.StatusOK, http.MethodGet, "/api/v1/plans/"+created.ID, nil, &plan)
			d := approveAndWait(t, c, plan)
			expected := "uncertain request"
			if scenario == "v2-captured" {
				expected = "operation temporarily unavailable"
			}
			if d.Status != "failed" || !strings.Contains(d.Error, expected) {
				t.Fatalf("did not retain original uncertainty: %+v", d)
			}
			db, err := pgx.Connect(t.Context(), cfg.Database.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close(t.Context())
			readState := func() (map[string]any, []map[string]any, map[string]json.RawMessage) {
				var raw []byte
				if queryErr := db.QueryRow(t.Context(), "SELECT steps FROM loomplane_deployments WHERE id=$1", d.ID).Scan(&raw); queryErr != nil {
					t.Fatal(queryErr)
				}
				return sqlStepCheckpoint(t, raw)
			}
			state, steps, wrapper := readState()
			if state["cloud_sql_checkpoint"] != float64(3) || state["caller"] != "loomplane@acme-prod.iam.gserviceaccount.com" || state["requested_at"] == nil || state["recovered_create"] != nil {
				t.Fatalf("request context was not durable: %+v", state)
			}
			if strings.HasPrefix(scenario, "v2-") {
				state["cloud_sql_checkpoint"] = 2
				delete(state, "caller")
				delete(state, "requested_at")
				var raw []byte
				raw, err = json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
				wrapper["apply"], err = json.Marshal(raw)
				if err != nil {
					t.Fatal(err)
				}
				var envelope []byte
				envelope, err = json.Marshal(wrapper)
				if err != nil {
					t.Fatal(err)
				}
				steps[0]["checkpoint"] = envelope
				raw, err = json.Marshal(steps)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec(t.Context(), "UPDATE loomplane_deployments SET steps=$2 WHERE id=$1", d.ID, raw); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "proof-save-failure" {
				if _, err = db.Exec(t.Context(), `CREATE FUNCTION fail_native_create_proof() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.steps) item WHERE convert_from(decode((convert_from(decode(item->>'checkpoint','base64'),'UTF8')::jsonb)->>'apply','base64'),'UTF8')::jsonb ? 'recovered_create') THEN RAISE EXCEPTION 'proof persistence unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_native_create_proof BEFORE UPDATE ON loomplane_deployments FOR EACH ROW EXECUTE FUNCTION fail_native_create_proof()`); err != nil {
					t.Fatal(err)
				}
			}
			running.stop()
			mu.Lock()
			blocked = false
			beforeLists := lists
			if scenario == "reused" || scenario == "replacement-history" {
				cloud.mu.Lock()
				cloud.sql[instance]["createTime"] = queued.Add(time.Hour).Format(time.RFC3339Nano)
				cloud.mu.Unlock()
			}
			mu.Unlock()
			running = start(t, cfg)
			c.base = running.srv.URL
			retry := func() {
				c.must(http.StatusOK, http.MethodPost, "/api/v1/deployments/"+d.ID+"/retry", nil, &d)
				deadline := time.Now().Add(3 * readyTimeout)
				for d.Status == "running" {
					if time.Now().After(deadline) {
						t.Fatalf("native discovery retry did not settle: %+v", d)
					}
					time.Sleep(100 * time.Millisecond)
					c.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+d.ID, nil, &d)
				}
			}
			retry()
			failed := scenario == "ambiguous" || scenario == "reused" || scenario == "replacement-history" || scenario == "v2-unknown" || scenario == "proof-save-failure"
			if failed {
				if d.Status != "failed" {
					t.Fatalf("unproven recovery was accepted: %+v", d)
				}
				mu.Lock()
				if writes["instance"] != 1 || writes["database"] != 0 || writes["user"] != 0 {
					t.Error("unproven recovery sent a mutation", writes)
				}
				if scenario == "v2-unknown" && lists != beforeLists {
					t.Error("legacy unknown request invented discovery context")
				}
				mu.Unlock()
				state, _, _ = readState()
				if state["recovered_create"] != nil {
					t.Fatal("failed proof save or ambiguous lookup changed durable evidence")
				}
				if scenario != "proof-save-failure" {
					return
				}
				if _, err = db.Exec(t.Context(), `DROP TRIGGER fail_native_create_proof ON loomplane_deployments; DROP FUNCTION fail_native_create_proof()`); err != nil {
					t.Fatal(err)
				}
				retry()
			}
			if d.Status != "succeeded" {
				t.Fatalf("original create did not resume: %+v", d)
			}
			state, _, _ = readState()
			if state["stage"] != "ready" {
				t.Fatal("resumed creation did not retain final checkpoint")
			}
			if scenario != "v2-captured" {
				proof, ok := state["recovered_create"].(map[string]any)
				if !ok || proof["operation"] != "original-create" || proof["physical_id"] != state["resource_id"] || proof["caller"] != "loomplane@acme-prod.iam.gserviceaccount.com" {
					t.Fatal("recovered creation lost durable native evidence", state)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if writes["instance"] != 1 || writes["database"] != 1 || writes["user"] != 1 || scenario == "v2-captured" && lists != 0 {
				t.Fatalf("resume replayed original create: %+v lists=%d", writes, lists)
			}
		})
	}
}
