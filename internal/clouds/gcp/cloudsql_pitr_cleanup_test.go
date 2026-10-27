package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSQLPITRCleanupKeepsFailedCloneWitnessAndRefusesReplacement(t *testing.T) {
	for _, scenario := range []string{"delete", "failed-clone", "already-absent", "resume-delete", "replacement", "wrong-operation", "missing-operation", "incomplete-operation", "before-failure", "started-failure", "guard-failure"} {
		t.Run(scenario, func(t *testing.T) {
			created := time.Now().UTC().Add(-time.Minute)
			original, fields := pscPITRInstance(t)
			original.Name, original.ConnectionName, original.CreateTime = "recovery-copy", "source-project:us-central1:recovery-copy", created.Format(time.RFC3339Nano)
			pin, err := original.Incarnation("source-project", "us-central1", "recovery-copy")
			if err != nil {
				t.Fatal(err)
			}
			fields["name"], fields["connectionName"], fields["createTime"] = original.Name, original.ConnectionName, original.CreateTime
			// No recovery labels: a failed clone may never reach the first patch.
			fields["settings"].(map[string]any)["userLabels"] = map[string]string{SQLIntentLabel: "inherited-source-intent"}
			exists, writes := scenario != "already-absent" && scenario != "resume-delete", 0
			before, captured := false, scenario == "resume-delete"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/operations/clone-original") {
					if scenario == "missing-operation" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					op := map[string]any{"kind": "sql#operation", "name": "clone-original", "status": "DONE", "targetProject": "source-project", "targetId": "recovery-copy", "operationType": "CLONE", "insertTime": created.Add(-time.Second), "endTime": created.Add(time.Second)}
					switch scenario {
					case "failed-clone":
						op["error"] = map[string]any{"errors": []any{map[string]string{"message": "copy initialization failed"}}}
					case "wrong-operation":
						op["targetId"] = "someone-elses-copy"
					case "incomplete-operation":
						delete(op, "endTime")
					}
					_ = json.NewEncoder(w).Encode(op)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/operations/delete-original") {
					if !captured {
						t.Error("deletion polled before durable capture")
					}
					exists = false
					_ = json.NewEncoder(w).Encode(map[string]string{"kind": "sql#operation", "name": "delete-original", "status": "DONE", "targetProject": "source-project", "targetId": "recovery-copy", "operationType": "DELETE"})
					return
				}
				if r.Method == http.MethodGet {
					if !exists {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					if scenario == "replacement" {
						fields["createTime"] = created.Add(time.Hour)
					}
					_ = json.NewEncoder(w).Encode(fields)
					return
				}
				writes++
				if !before || r.Method != http.MethodDelete || r.URL.Path != "/v1/projects/source-project/instances/recovery-copy" || r.URL.Query().Get("enableFinalBackup") != "false" {
					t.Errorf("uncheckpointed or misrouted deletion: %s %s", r.Method, r.URL)
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"kind": "sql#operation", "name": "delete-original", "status": "PENDING", "targetProject": "source-project", "targetId": "recovery-copy", "operationType": "DELETE"})
			}))
			defer server.Close()
			s := &Session{c: New(server.URL), key: &Key{ProjectID: "source-project"}, http: server.Client()}
			track := SQLMutation{Check: func(context.Context) error {
				if scenario == "guard-failure" {
					return errors.New("worker lost authority")
				}
				return nil
			}, Before: func() error {
				before = true
				if scenario == "before-failure" {
					return errors.New("checkpoint unavailable")
				}
				return nil
			}, Started: func(string) error {
				if scenario == "started-failure" {
					return errors.New("capture unavailable")
				}
				captured = true
				return nil
			}}
			if scenario == "resume-delete" {
				track.Resume = "delete-original"
			}
			err = s.DeleteSQLPITRCopy(t.Context(), "recovery-copy", "us-central1", pin, "clone-original", track)
			success := scenario == "delete" || scenario == "failed-clone" || scenario == "already-absent" || scenario == "resume-delete"
			if success && (err != nil || exists) || !success && err == nil {
				t.Fatalf("cleanup result did not preserve original clone ownership: %v exists=%v", err, exists)
			}
			wantWrites := 0
			if scenario == "delete" || scenario == "failed-clone" || scenario == "started-failure" {
				wantWrites = 1
			}
			if writes != wantWrites {
				t.Fatalf("unsafe or replayed delete: got %d want %d", writes, wantWrites)
			}
		})
	}
}
