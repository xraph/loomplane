package gcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func deletedPITRBackup(t *testing.T, deleted time.Time) map[string]any {
	t.Helper()
	_, settings := pscPITRInstance(t)
	return map[string]any{
		"kind": "sql#backup", "name": "projects/source-project/backups/final-source", "instance": "source-db",
		"type": "FINAL", "state": "SUCCESSFUL", "databaseVersion": "POSTGRES_17",
		"instanceDeletionTime": deleted, "instanceSettings": settings,
		"backupInterval": map[string]any{"startTime": deleted.Add(-time.Minute), "endTime": deleted},
		"expiryTime":     time.Now().UTC().Add(24 * time.Hour),
	}
}

func TestSQLDeletedPITRWindowUsesOriginalFinalBackup(t *testing.T) {
	for _, scenario := range []string{"available", "missing-backup", "denied", "wrong-reference", "wrong-source", "reused-name", "wrong-region", "missing-settings", "old-backup", "not-ready", "expired", "missing-deletion", "future-deletion", "before-creation", "unsafe-settings", "changed-settings", "changed-deletion", "changed-backup", "post-deletion-window", "pre-creation-window", "window-error"} {
		t.Run(scenario, func(t *testing.T) {
			original, _ := pscPITRInstance(t)
			pin, err := original.Incarnation("source-project", "us-central1", "source-db")
			if err != nil {
				t.Fatal(err)
			}
			deleted := time.Now().UTC().Add(-time.Hour)
			earliest, latest := deleted.Add(-time.Hour), deleted.Add(-time.Second)
			reads, windows := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != http.MethodGet {
					t.Error("deleted source discovery mutated a resource", r.Method)
					w.WriteHeader(500)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/getLatestRecoveryTime") {
					windows++
					if r.URL.Path != "/v1/projects/source-project/instances/source-db/getLatestRecoveryTime" || r.URL.Query().Get("sourceInstanceDeletionTime") != deleted.Format(time.RFC3339Nano) {
						t.Error("window did not identify the exact deleted source", r.URL)
					}
					if scenario == "window-error" {
						w.WriteHeader(403)
						return
					}
					low, high := earliest, latest
					if scenario == "post-deletion-window" {
						high = deleted.Add(time.Second)
					}
					if scenario == "pre-creation-window" {
						low = time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"kind": "sql#getLatestRecoveryTime", "earliestRecoveryTime": low, "latestRecoveryTime": high})
					return
				}
				if r.URL.Path != "/v1/projects/source-project/backups/final-source" {
					t.Error("read a reusable source name", r.URL)
					w.WriteHeader(500)
					return
				}
				reads++
				body := deletedPITRBackup(t, deleted)
				source := body["instanceSettings"].(map[string]any)
				switch scenario {
				case "missing-backup":
					w.WriteHeader(404)
					return
				case "denied":
					w.WriteHeader(403)
					return
				case "wrong-reference":
					body["name"] = "projects/source-project/backups/other"
				case "wrong-source":
					body["instance"] = "another-db"
				case "reused-name":
					source["createTime"] = "2026-10-21T00:00:00Z"
				case "wrong-region":
					source["region"] = "europe-west1"
				case "missing-settings":
					delete(body, "instanceSettings")
				case "old-backup":
					body["type"] = "AUTOMATED"
				case "not-ready":
					body["state"] = "RUNNING"
				case "expired":
					body["expiryTime"] = deleted.Add(time.Minute)
				case "missing-deletion":
					delete(body, "instanceDeletionTime")
				case "future-deletion":
					body["instanceDeletionTime"] = time.Now().Add(time.Hour)
				case "before-creation":
					body["instanceDeletionTime"] = "2026-10-19T00:00:00Z"
				case "unsafe-settings":
					source["settings"].(map[string]any)["ipConfiguration"].(map[string]any)["ipv4Enabled"] = true
				case "changed-settings":
					if reads > 1 {
						source["settings"].(map[string]any)["settingsVersion"] = "43"
					}
				case "changed-deletion":
					if reads > 1 {
						body["instanceDeletionTime"] = deleted.Add(time.Second)
					}
				case "changed-backup":
					if reads > 1 {
						body["backupInterval"].(map[string]any)["startTime"] = deleted.Add(-2 * time.Minute)
					}
				}
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			s := &Session{c: New(server.URL), key: &Key{ProjectID: "source-project"}, http: server.Client()}
			window, err := s.ReadSQLDeletedPITRWindow(t.Context(), "us-central1", "source-db", pin, "projects/source-project/backups/final-source", pscPITRSpec())
			if scenario == "available" {
				if err != nil || window == nil || window.DeletedSource == nil || window.DeletedSource.Backup != "projects/source-project/backups/final-source" || !window.DeletedSource.DeletedAt.Equal(deleted) || !window.Earliest.Equal(earliest) || !window.Latest.Equal(latest) || window.PhysicalID != pin || len(window.SettingsDigest) != 64 || reads != 2 || windows != 1 {
					t.Fatal("deleted PITR lost original source evidence", window, err, reads, windows)
				}
			} else if err == nil || window != nil {
				t.Fatal("unproven deleted source admitted", scenario, window, err)
			}
		})
	}
}
