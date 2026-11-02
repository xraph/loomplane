package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type retainedSQLBackupDeleter interface {
	DeleteRetainedSQLBackup(context.Context, string, string, string, SQLBackup, bool, func(context.Context) error, func() error) error
}

func TestRetainedSQLBackupDeletionKeepsOriginalIncarnation(t *testing.T) {
	for _, scenario := range []string{"complete", "lost-response", "missing-before-intent", "automated", "replacement", "changed-after-intent", "intent-failed", "guard-revoked", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			exists, intent := scenario != "missing-before-intent", false
			writes, checks := 0, 0
			point := sqlBackupFixture()
			if scenario == "replacement" {
				point["backupInterval"].(map[string]string)["startTime"] = "2026-10-21T03:00:00Z"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/projects/project/backups/original" {
					t.Error("foreign native request", r.URL)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if r.Method == http.MethodGet {
					if !exists {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_ = json.NewEncoder(w).Encode(point)
					return
				}
				if r.Method != http.MethodDelete {
					t.Error("unexpected request", r.Method)
					return
				}
				writes++
				if !intent {
					t.Error("delete without durable intent")
				}
				if scenario == "denied" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				exists = false
				if scenario == "lost-response" {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"name": "delete-original", "operationType": "DELETE_BACKUP", "status": "DONE"})
			}))
			defer server.Close()
			s := &Session{c: New(server.URL), key: &Key{ProjectID: "project"}, http: server.Client()}
			deleter, ok := any(s).(retainedSQLBackupDeleter)
			if !ok {
				t.Fatal("native Cloud SQL backup deletion is missing")
			}
			source := &SQLInstance{Name: "database", Project: "project", Region: "europe-west1", ConnectionName: "project:europe-west1:database", CreateTime: "2026-10-21T00:00:00Z"}
			pin, err := source.Incarnation("project", "europe-west1", "database")
			if err != nil {
				t.Fatal(err)
			}
			selected := SQLBackup{Name: "projects/project/backups/original", Type: "FINAL", DatabaseVersion: "POSTGRES_17", CreatedAt: time.Date(2026, 10, 21, 2, 0, 0, 0, time.UTC)}
			if scenario == "automated" {
				selected.Type = "AUTOMATED"
			}
			check := func(context.Context) error {
				checks++
				if scenario == "guard-revoked" && checks > 1 {
					return errors.New("revoked")
				}
				return nil
			}
			before := func() error {
				if scenario == "intent-failed" {
					return errors.New("checkpoint unavailable")
				}
				intent = true
				if scenario == "changed-after-intent" {
					point["backupInterval"].(map[string]string)["startTime"] = "2026-10-21T03:00:00Z"
				}
				return nil
			}
			err = deleter.DeleteRetainedSQLBackup(t.Context(), "europe-west1", "database", pin, selected, false, check, before)
			if scenario == "lost-response" {
				if err == nil || writes != 1 {
					t.Fatal("lost response hidden or replayed", writes, err)
				}
				err = deleter.DeleteRetainedSQLBackup(t.Context(), "europe-west1", "database", pin, selected, true, check, before)
			}
			success := scenario == "complete" || scenario == "lost-response" || scenario == "missing-before-intent"
			if (err == nil) != success {
				t.Fatal("wrong native deletion result", err)
			}
			if success && !intent {
				t.Fatal("absence was accepted without durable intent")
			}
			want := 0
			if scenario == "complete" || scenario == "lost-response" || scenario == "denied" {
				want = 1
			}
			if writes != want {
				t.Fatal("native delete repeated or missing", writes, want)
			}
		})
	}
}
