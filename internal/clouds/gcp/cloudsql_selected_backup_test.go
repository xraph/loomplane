package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type selectedSQLBackupReader interface {
	ReadSQLBackup(context.Context, string, string, string, SQLBackup) (*SQLBackup, error)
}

func TestSelectedSQLBackupRevalidatesItsOwnExactIdentity(t *testing.T) {
	for _, scenario := range []string{"original", "reused-source", "changed-point-time", "changed-name", "changed-engine", "changed-kind", "changed-key", "missing-proof", "pending", "failed", "expired", "missing", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			reads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reads++
				if r.Method != http.MethodGet || r.URL.Path != "/v1/projects/project/backups/original" || r.URL.RawQuery != "" {
					t.Errorf("selected point used another source or a write: %s %s", r.Method, r.URL)
				}
				point := sqlBackupFixture()
				switch scenario {
				case "reused-source":
					point["instanceSettings"].(map[string]string)["createTime"] = "2026-10-21T01:00:00Z"
				case "changed-point-time":
					point["backupInterval"].(map[string]string)["startTime"] = "2026-10-21T02:00:00.000000001Z"
				case "changed-name":
					point["name"] = "projects/project/backups/replacement"
				case "changed-engine":
					point["databaseVersion"] = "POSTGRES_18"
				case "changed-kind":
					point["type"] = "ON_DEMAND"
				case "changed-key":
					point["kmsKey"] = "another-key"
				case "missing-proof":
					delete(point, "instanceSettings")
				case "pending":
					point["state"] = "RUNNING"
				case "failed":
					point["state"] = "FAILED"
				case "expired":
					point["expiryTime"] = "2026-10-21T03:00:00Z"
				case "missing":
					w.WriteHeader(http.StatusNotFound)
					return
				case "denied":
					w.WriteHeader(http.StatusForbidden)
					return
				}
				_ = json.NewEncoder(w).Encode(point)
			}))
			defer server.Close()
			s := &Session{c: New(server.URL), key: &Key{ProjectID: "project"}, http: server.Client()}
			reader, ok := any(s).(selectedSQLBackupReader)
			if !ok {
				t.Fatal("selected Cloud SQL recovery points cannot be reread by exact reference")
			}
			original := &SQLInstance{Name: "database", Project: "project", Region: "europe-west1", ConnectionName: "project:europe-west1:database", CreateTime: "2026-10-21T00:00:00Z"}
			pin, err := original.Incarnation("project", "europe-west1", "database")
			if err != nil {
				t.Fatal(err)
			}
			created, err := time.Parse(time.RFC3339Nano, "2026-10-21T02:00:00Z")
			if err != nil {
				t.Fatal(err)
			}
			selected := SQLBackup{Name: "projects/project/backups/original", Type: "FINAL", DatabaseVersion: "POSTGRES_17", CreatedAt: created}
			point, err := reader.ReadSQLBackup(t.Context(), "europe-west1", "database", pin, selected)
			if scenario == "original" {
				if err != nil || point == nil || point.Name != selected.Name || !point.CreatedAt.Equal(selected.CreatedAt) || point.State != "SUCCESSFUL" {
					t.Fatalf("original point unavailable: %+v %v", point, err)
				}
			} else if err == nil || point != nil {
				t.Fatalf("changed or unavailable point admitted: %+v %v", point, err)
			}
			if reads != 1 {
				t.Fatal("selected read did not use exactly one backup detail request", reads)
			}
		})
	}
}

func TestSelectedSQLBackupRejectsUnscopedInputBeforeReads(t *testing.T) {
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reads++ }))
	defer server.Close()
	s := &Session{c: New(server.URL), key: &Key{ProjectID: "project"}, http: server.Client()}
	reader, ok := any(s).(selectedSQLBackupReader)
	if !ok {
		t.Fatal("selected Cloud SQL point validation is missing")
	}
	for _, reference := range []string{"", "projects/other/backups/original", "projects/project/backups/../original", "projects/project/backups/encoded%2fpath"} {
		if _, err := reader.ReadSQLBackup(t.Context(), "europe-west1", "database", "cloud-sql:invalid", SQLBackup{Name: reference}); err == nil {
			t.Fatal("unscoped native point accepted", reference)
		}
	}
	if reads != 0 {
		t.Fatal("invalid selected point reached Google", reads)
	}
}
