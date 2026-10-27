package gcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCloudSQLBackupInventoryRejectsContradictoryExpiry(t *testing.T) {
	for _, expiry := range []string{"not-a-time", "0001-01-01T00:00:00Z", "2026-10-21T01:00:00Z", "2026-10-21T02:00:00Z", "2026-10-21T02:00:00.5Z"} {
		t.Run(expiry, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				point := sqlBackupFixture()
				point["expiryTime"] = expiry
				_ = json.NewEncoder(w).Encode(map[string]any{"backups": []any{point}})
			}))
			defer srv.Close()
			s := &Session{c: New(srv.URL), key: &Key{ProjectID: "project"}, http: srv.Client()}
			original := &SQLInstance{Name: "database", Project: "project", Region: "europe-west1", ConnectionName: "project:europe-west1:database", CreateTime: "2026-10-21T00:00:00Z"}
			pin, err := original.Incarnation("project", "europe-west1", "database")
			if err != nil {
				t.Fatal(err)
			}
			if _, complete, readErr := s.ListSQLBackups(t.Context(), "europe-west1", "database", pin); readErr == nil || complete {
				t.Fatalf("untrusted expiry accepted: complete=%v err=%v", complete, readErr)
			}
		})
	}
}

func TestCloudSQLBackupInventoryKeepsReportedExpiryWithoutAssumingRetention(t *testing.T) {
	for _, expiry := range []string{"", "2026-10-21T03:00:00Z", "2099-10-21T03:00:00+01:00"} {
		t.Run(expiry, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				point := sqlBackupFixture()
				point["ttlDays"] = "30"
				if expiry != "" {
					point["expiryTime"] = expiry
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"backups": []any{point}})
			}))
			defer srv.Close()
			s := &Session{c: New(srv.URL), key: &Key{ProjectID: "project"}, http: srv.Client()}
			original := &SQLInstance{Name: "database", Project: "project", Region: "europe-west1", ConnectionName: "project:europe-west1:database", CreateTime: "2026-10-21T00:00:00Z"}
			pin, err := original.Incarnation("project", "europe-west1", "database")
			if err != nil {
				t.Fatal(err)
			}
			points, complete, err := s.ListSQLBackups(t.Context(), "europe-west1", "database", pin)
			if err != nil || !complete || len(points) != 1 {
				t.Fatalf("expiry erased historical point: %+v %v", points, err)
			}
			// Observe the wire projection so this regression fails before the field exists.
			raw, err := json.Marshal(points)
			if err != nil {
				t.Fatal(err)
			}
			var projected []struct{ ExpiresAt *time.Time }
			if err = json.Unmarshal(raw, &projected); err != nil {
				t.Fatal(err)
			}
			if expiry == "" {
				if projected[0].ExpiresAt != nil {
					t.Fatal("input TTL fabricated an absolute expiry")
				}
			} else if wanted, parseErr := time.Parse(time.RFC3339Nano, expiry); parseErr != nil || projected[0].ExpiresAt == nil || !projected[0].ExpiresAt.Equal(wanted) || projected[0].ExpiresAt.Location() != time.UTC {
				t.Fatalf("native expiry was dropped or changed: %s %+v %v", expiry, projected, parseErr)
			}
		})
	}
}

func sqlBackupFixture() map[string]any {
	return map[string]any{
		"name": "projects/project/backups/original", "kind": "sql#backup", "instance": "database", "type": "FINAL", "state": "SUCCESSFUL", "databaseVersion": "POSTGRES_17",
		"backupInterval":   map[string]string{"startTime": "2026-10-21T02:00:00Z", "endTime": "2026-10-21T02:00:01Z"},
		"instanceSettings": map[string]string{"name": "database", "project": "project", "region": "europe-west1", "connectionName": "project:europe-west1:database", "createTime": "2026-10-21T00:00:00Z"},
	}
}

func TestCloudSQLBackupInventoryVerifiesOriginalSource(t *testing.T) {
	for _, scenario := range []string{"original", "reused", "other-region", "missing-proof", "detail-proof", "warning", "foreign-project", "foreign-reference", "bad-time", "before-instance", "bad-interval", "duplicate", "repeated-token", "truncated", "denied", "malformed-proof", "detail-identity", "unsafe-reference", "contradictory-instance", "malformed-instance", "other-instance"} {
		t.Run(scenario, func(t *testing.T) {
			listCalls, detailCalls := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != http.MethodGet {
					t.Errorf("inventory mutated provider: %s", r.Method)
				}
				point := sqlBackupFixture()
				proof := point["instanceSettings"].(map[string]string)
				switch scenario {
				case "reused":
					proof["createTime"] = "2026-10-21T01:00:00Z"
				case "other-region":
					proof["region"] = "us-central1"
					proof["connectionName"] = "project:us-central1:database"
				case "foreign-project":
					proof["project"] = "other"
				case "foreign-reference":
					point["name"] = "projects/other/backups/original"
				case "unsafe-reference":
					point["name"] = "projects/project/backups/encoded%2fpath"
				case "bad-time":
					point["backupInterval"] = map[string]string{"startTime": "yesterday"}
				case "before-instance":
					point["backupInterval"] = map[string]string{"startTime": "2026-10-20T02:00:00Z"}
				case "bad-interval":
					point["backupInterval"] = map[string]string{"startTime": "2026-10-21T02:00:00Z", "endTime": "2026-10-21T01:00:00Z"}
				case "contradictory-instance":
					point["instance"] = "other"
				case "malformed-instance":
					point["instance"] = "database/other"
				case "other-instance":
					point["instance"] = "other"
					proof["name"] = "other"
					proof["connectionName"] = "project:europe-west1:other"
				case "malformed-proof":
					proof["connectionName"] = "other"
				}
				if strings.HasPrefix(r.URL.Path, "/v1/projects/project/backups/") {
					detailCalls++
					if scenario == "missing-proof" {
						delete(point, "instanceSettings")
					}
					if scenario == "detail-identity" {
						point["name"] = "projects/project/backups/another"
					}
					_ = json.NewEncoder(w).Encode(point)
					return
				}
				listCalls++
				if r.URL.Path != "/v1/projects/project/backups" || r.URL.Query().Get("pageSize") != "100" || r.URL.Query().Get("filter") != "instance:database" {
					t.Errorf("unscoped inventory request: %s", r.URL)
				}
				if scenario == "denied" {
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, `{"error":{"message":"private provider diagnostic"}}`)
					return
				}
				if scenario == "missing-proof" || scenario == "detail-proof" || scenario == "detail-identity" {
					delete(point, "instanceSettings")
				}
				points := []any{point}
				body := map[string]any{"backups": points}
				switch scenario {
				case "duplicate":
					body["backups"] = []any{point, point}
				case "warning":
					body["warnings"] = []map[string]string{{"code": "REGION_UNREACHABLE"}}
				case "repeated-token":
					body["backups"] = []any{}
					body["nextPageToken"] = "same"
				case "truncated":
					body["backups"] = []any{}
					body["nextPageToken"] = fmt.Sprint(listCalls)
				}
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer srv.Close()
			s := &Session{c: New(srv.URL), key: &Key{ProjectID: "project"}, http: srv.Client()}
			original := &SQLInstance{Name: "database", Project: "project", Region: "europe-west1", ConnectionName: "project:europe-west1:database", CreateTime: "2026-10-21T00:00:00Z"}
			pin, err := original.Incarnation("project", "europe-west1", "database")
			if err != nil {
				t.Fatal(err)
			}
			points, complete, err := s.ListSQLBackups(t.Context(), "europe-west1", "database", pin)
			switch scenario {
			case "original", "detail-proof", "warning":
				if err != nil || len(points) != 1 || complete == (scenario == "warning") {
					t.Fatalf("original backup missing: points=%+v complete=%v err=%v", points, complete, err)
				}
			case "reused", "other-region", "other-instance":
				if err != nil || len(points) != 0 || !complete {
					t.Fatalf("foreign incarnation included: points=%+v complete=%v err=%v", points, complete, err)
				}
			case "missing-proof", "truncated":
				if err != nil || len(points) != 0 || complete {
					t.Fatalf("unverifiable/incomplete scan declared complete: points=%+v complete=%v err=%v", points, complete, err)
				}
			default:
				if err == nil {
					t.Fatal("untrusted inventory accepted")
				}
			}
			if scenario == "truncated" && listCalls != 10 {
				t.Fatalf("unbounded pagination: %d", listCalls)
			}
			if scenario == "missing-proof" || scenario == "detail-proof" || scenario == "detail-identity" {
				if detailCalls != 1 {
					t.Fatalf("optional source metadata did not use bounded canonical detail read: %d", detailCalls)
				}
			} else if detailCalls != 0 {
				t.Fatal("unexpected detail request")
			}
		})
	}
}

func TestCloudSQLBackupInventoryKeepsPagingAndBoundsInput(t *testing.T) {
	t.Run("pages", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			point := sqlBackupFixture()
			point["name"] = fmt.Sprintf("projects/project/backups/point-%d", calls)
			point["kmsKey"] = "projects/kms-project/locations/europe/keyRings/backup/cryptoKeys/key"
			body := map[string]any{"backups": []any{point}}
			if calls == 1 {
				if r.URL.Query().Get("pageToken") != "" {
					t.Error("first page had token")
				}
				body["nextPageToken"] = "second"
			} else if r.URL.Query().Get("pageToken") != "second" {
				t.Error("lost original continuation token")
			}
			_ = json.NewEncoder(w).Encode(body)
		}))
		defer srv.Close()
		s := &Session{c: New(srv.URL), key: &Key{ProjectID: "project"}, http: srv.Client()}
		original := &SQLInstance{Name: "database", Project: "project", Region: "europe-west1", ConnectionName: "project:europe-west1:database", CreateTime: "2026-10-21T00:00:00Z"}
		pin, err := original.Incarnation("project", "europe-west1", "database")
		if err != nil {
			t.Fatal(err)
		}
		points, complete, err := s.ListSQLBackups(t.Context(), "europe-west1", "database", pin)
		if err != nil || !complete || len(points) != 2 || calls != 2 || points[1].Name != "projects/project/backups/point-2" || points[0].CreatedAt.Format("2006-01-02T15:04:05Z07:00") != "2026-10-21T02:00:00Z" || points[0].DatabaseVersion != "POSTGRES_17" || points[0].State != "SUCCESSFUL" || points[0].Type != "FINAL" || points[0].KMSKey == "" {
			t.Fatalf("lost native metadata/paging: %+v complete=%v calls=%d err=%v", points, complete, calls, err)
		}
	})
	for _, scenario := range []string{"filter-injection", "missing-region", "missing-pin", "malformed-pin"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; fmt.Fprint(w, `{}`) }))
			defer srv.Close()
			s := &Session{c: New(srv.URL), key: &Key{ProjectID: "project"}, http: srv.Client()}
			name, region, pin := "database", "europe-west1", "cloud-sql:"+strings.Repeat("a", 64)
			switch scenario {
			case "filter-injection":
				name = "database AND type:FINAL"
			case "missing-region":
				region = ""
			case "missing-pin":
				pin = ""
			case "malformed-pin":
				pin = "cloud-sql:not-a-digest"
			}
			if _, _, err := s.ListSQLBackups(t.Context(), region, name, pin); err == nil || calls != 0 {
				t.Fatalf("invalid source reached provider: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestCloudSQLBackupInventoryPreservesPartialAndUntrustedResponses(t *testing.T) {
	for _, scenario := range []string{"unknown-kind", "unknown-type", "unknown-state", "missing-interval", "running-no-interval", "missing-end", "large-page", "long-token", "detail-denied", "inconsistent-engine", "source-missing-region"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				point := sqlBackupFixture()
				switch scenario {
				case "unknown-kind":
					point["kind"] = "unknown"
				case "unknown-type":
					point["type"] = "UNKNOWN"
				case "unknown-state":
					point["state"] = "UNKNOWN"
				case "missing-interval":
					delete(point, "backupInterval")
				case "running-no-interval":
					delete(point, "backupInterval")
					point["state"] = "RUNNING"
				case "missing-end":
					point["backupInterval"] = map[string]string{"startTime": "2026-10-21T02:00:00Z"}
				case "detail-denied":
					delete(point, "instanceSettings")
				case "source-missing-region":
					point["instanceSettings"].(map[string]string)["region"] = ""
				case "inconsistent-engine":
					point["instanceSettings"].(map[string]string)["databaseVersion"] = "MYSQL_8_4"
				}
				if strings.Contains(r.URL.Path, "/backups/") {
					if scenario == "detail-denied" {
						w.WriteHeader(http.StatusForbidden)
						fmt.Fprint(w, `{"error":{"message":"detail denied"}}`)
						return
					}
					_ = json.NewEncoder(w).Encode(point)
					return
				}
				body := map[string]any{"backups": []any{point}}
				if scenario == "long-token" {
					body["nextPageToken"] = strings.Repeat("a", 4097)
				}
				if scenario == "large-page" {
					points := make([]any, 101)
					for i := range points {
						points[i] = point
					}
					body["backups"] = points
				}
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer srv.Close()
			s := &Session{c: New(srv.URL), key: &Key{ProjectID: "project"}, http: srv.Client()}
			original := &SQLInstance{Name: "database", Project: "project", Region: "europe-west1", ConnectionName: "project:europe-west1:database", CreateTime: "2026-10-21T00:00:00Z"}
			pin, err := original.Incarnation("project", "europe-west1", "database")
			if err != nil {
				t.Fatal(err)
			}
			points, complete, err := s.ListSQLBackups(t.Context(), "europe-west1", "database", pin)
			switch scenario {
			case "missing-interval", "running-no-interval", "missing-end", "source-missing-region":
				if err != nil || complete || len(points) != 0 {
					t.Fatalf("missing source/interval metadata completed scan: %+v %v %v", points, complete, err)
				}
			default:
				if err == nil || complete {
					t.Fatal("untrusted response completed scan")
				}
			}
			if calls > 2 {
				t.Fatalf("unbounded followup: %d", calls)
			}
		})
	}
}
