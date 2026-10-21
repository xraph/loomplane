package gcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCloudSQLReviewedStorageRequest(t *testing.T) {
	for _, reviewed := range []bool{false, true} {
		t.Run(fmt.Sprint(reviewed), func(t *testing.T) {
			created := false
			var request map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					created = true
					fmt.Fprint(w, `{"name":"create","status":"DONE"}`)
					return
				}
				if !created {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				fmt.Fprint(w, `{"name":"database","state":"RUNNABLE","databaseVersion":"POSTGRES_17"}`)
			}))
			defer srv.Close()
			s := &Session{c: New(srv.URL), key: &Key{ProjectID: "project-test"}, http: srv.Client()}
			spec := SQLInstanceSpec{Name: "database", DatabaseVersion: "POSTGRES_17", Region: "europe-west1", Tier: "db-custom-1-3840"}
			if reviewed {
				spec.Storage = &SQLStorageSettings{SizeGB: 10, Type: "PD_SSD", RetainedBackups: 7, RecoveryDays: 7}
			}
			if _, err := s.EnsureSQLInstance(t.Context(), spec, SQLMutation{Before: func() error { return nil }, Started: func(string) error { return nil }}); err != nil {
				t.Fatal(err)
			}
			settings := request["settings"].(map[string]any)
			backup := settings["backupConfiguration"].(map[string]any)
			if backup["enabled"] != true {
				t.Fatal("backups disabled")
			}
			if !reviewed {
				if settings["dataDiskSizeGb"] != nil || backup["pointInTimeRecoveryEnabled"] != nil {
					t.Fatal("legacy request was silently changed")
				}
				return
			}
			retention, ok := backup["backupRetentionSettings"].(map[string]any)
			if !ok || retention["retentionUnit"] != "COUNT" || retention["retainedBackups"] != float64(7) || backup["pointInTimeRecoveryEnabled"] != true || backup["transactionLogRetentionDays"] != float64(7) || settings["dataDiskSizeGb"] != "10" || settings["dataDiskType"] != "PD_SSD" || settings["storageAutoResize"] != false || settings["availabilityType"] != "ZONAL" {
				t.Fatalf("reviewed request did not reach Cloud SQL: %#v", settings)
			}
			ip := settings["ipConfiguration"].(map[string]any)
			if ip["ipv4Enabled"] != true || ip["authorizedNetworks"] != nil {
				t.Fatal("connector access gained authorized public networks")
			}
		})
	}
}

func TestCloudSQLRemovalRequiresFinalBackup(t *testing.T) {
	for _, state := range []string{"accepted", "pending", "missing", "refused", "backup-failed"} {
		t.Run(state, func(t *testing.T) {
			deletes, polls := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodDelete && r.URL.Path == "/v1/projects/project-test/instances/database":
					deletes++
					q := r.URL.Query()
					if q.Get("enableFinalBackup") != "true" || q.Get("finalBackupTtlDays") != "30" || q.Get("finalBackupExpiryTime") != "" {
						t.Errorf("removal has no bounded final backup: %s", r.URL.RawQuery)
					}
					body, err := io.ReadAll(r.Body)
					if err != nil || len(body) != 0 {
						t.Errorf("deletion must have an empty request body: %q, %v", body, err)
					}
					switch state {
					case "missing":
						w.WriteHeader(http.StatusNotFound)
						fmt.Fprint(w, `{"error":{"message":"already gone"}}`)
					case "refused":
						w.WriteHeader(http.StatusForbidden)
						fmt.Fprint(w, `{"error":{"message":"final backup refused"}}`)
					case "backup-failed":
						fmt.Fprint(w, `{"name":"remove-operation","status":"DONE","error":{"errors":[{"message":"final backup failed"}]}}`)
					case "pending":
						fmt.Fprint(w, `{"name":"remove-operation","status":"PENDING"}`)
					default:
						fmt.Fprint(w, `{"name":"remove-operation","status":"DONE"}`)
					}
				case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/project-test/operations/remove-operation":
					polls++
					fmt.Fprint(w, `{"name":"remove-operation","status":"DONE"}`)
				default:
					t.Errorf("unexpected cloud request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer srv.Close()
			s := &Session{c: New(srv.URL), key: &Key{ProjectID: "project-test"}, http: srv.Client()}
			err := s.DeleteSQLInstance(t.Context(), "database", SQLMutation{Before: func() error { return nil }, Started: func(string) error { return nil }})
			switch state {
			case "refused", "backup-failed":
				if err == nil || !strings.Contains(err.Error(), "final backup") {
					t.Fatalf("backup failure was ignored: %v", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			if deletes != 1 {
				t.Fatalf("removal retried with weaker options: %d deletions", deletes)
			}
			wantPolls := 0
			if state == "pending" {
				wantPolls = 1
			}
			if polls != wantPolls {
				t.Fatalf("operation polls: %d, want %d", polls, wantPolls)
			}
		})
	}
}
