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

func TestCloudSQLCreateDiscoveryRequiresUniqueNativeEvidence(t *testing.T) {
	for _, scenario := range []string{"original", "rotated-caller", "missing", "label", "reused-pin", "replacement-history", "pending-history", "malformed-history", "ambiguous", "no-operations", "caller", "type", "pending", "failed", "warning", "before-request", "after-creation", "end-before-creation", "foreign-project", "foreign-instance", "missing-time", "malformed-reference", "truncated", "repeated-token", "denied", "changed-detail", "changed-source"} {
		t.Run(scenario, func(t *testing.T) {
			requested := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
			created := requested.Add(2 * time.Second)
			reads, lists, details := 0, 0, 0
			op := func() map[string]any {
				return map[string]any{"kind": "sql#operation", "name": "original", "targetId": "database", "targetProject": "project", "operationType": "CREATE", "status": "DONE", "user": "original@example.com", "insertTime": requested.Add(time.Second).Format(time.RFC3339Nano), "startTime": created.Format(time.RFC3339Nano), "endTime": created.Add(time.Second).Format(time.RFC3339Nano)}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("discovery wrote to Google")
				}
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "/instances/") {
					reads++
					if scenario == "missing" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					label, date := "intent", created
					if scenario == "label" {
						label = "another"
					}
					if scenario == "reused-pin" || scenario == "replacement-history" || (scenario == "changed-source" && reads > 1) {
						date = date.Add(time.Hour)
					}
					fmt.Fprintf(w, `{"name":"database","project":"project","region":"europe-west1","connectionName":"project:europe-west1:database","createTime":%q,"state":"RUNNABLE","databaseVersion":"POSTGRES_17","settings":{"userLabels":{"loomplane-create-intent":%q}}}`, date.Format(time.RFC3339Nano), label)
					return
				}
				point := op()
				switch scenario {
				case "caller":
					point["user"] = "another@example.com"
				case "type":
					point["operationType"] = "CREATE_USER"
				case "pending":
					point["status"] = "PENDING"
					delete(point, "endTime")
				case "failed":
					point["error"] = map[string]any{"errors": []any{map[string]string{"message": "failed"}}}
				case "warning":
					point["apiWarning"] = map[string]string{"code": "INTERNAL_STATE_FAILURE"}
				case "before-request":
					point["insertTime"] = requested.Add(-time.Second).Format(time.RFC3339Nano)
				case "after-creation":
					point["startTime"] = created.Add(time.Second).Format(time.RFC3339Nano)
				case "end-before-creation":
					point["endTime"] = created.Add(-time.Second).Format(time.RFC3339Nano)
				case "foreign-project":
					point["targetProject"] = "other"
				case "foreign-instance":
					point["targetId"] = "other"
				case "missing-time":
					delete(point, "insertTime")
				case "malformed-reference":
					point["name"] = "escaped%2foperation"
				}
				if strings.Contains(r.URL.Path, "/operations/") {
					details++
					if scenario == "replacement-history" {
						point["name"] = "replacement"
						point["insertTime"] = requested.Add(time.Hour).Format(time.RFC3339Nano)
						point["startTime"] = created.Add(time.Hour).Format(time.RFC3339Nano)
						point["endTime"] = created.Add(time.Hour + time.Second).Format(time.RFC3339Nano)
					}
					if scenario == "changed-detail" {
						point["user"] = "another@example.com"
					}
					_ = json.NewEncoder(w).Encode(point)
					return
				}
				lists++
				if r.URL.Path != "/v1/projects/project/operations" || r.URL.Query().Get("instance") != "database" || r.URL.Query().Get("location") != "europe-west1" || r.URL.Query().Get("maxResults") != "100" {
					t.Errorf("unscoped operation query: %s", r.URL)
				}
				if scenario == "denied" {
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, `{"error":{"message":"private"}}`)
					return
				}
				body := map[string]any{"kind": "sql#operationsList", "items": []any{point}}
				if scenario == "replacement-history" {
					other := op()
					other["name"] = "replacement"
					other["insertTime"] = requested.Add(time.Hour).Format(time.RFC3339Nano)
					other["startTime"] = created.Add(time.Hour).Format(time.RFC3339Nano)
					other["endTime"] = created.Add(time.Hour + time.Second).Format(time.RFC3339Nano)
					body["items"] = []any{other, point}
				}
				if scenario == "pending-history" || scenario == "malformed-history" {
					other := op()
					other["name"] = "unproven-attempt"
					if scenario == "pending-history" {
						other["status"] = "PENDING"
						delete(other, "endTime")
					} else {
						delete(other, "insertTime")
					}
					body["items"] = []any{point, other}
				}
				if scenario == "ambiguous" {
					other := op()
					other["name"] = "another"
					body["items"] = []any{point, other}
				}
				if scenario == "no-operations" {
					body["items"] = []any{}
				}
				if scenario == "truncated" {
					body["items"] = []any{}
					body["nextPageToken"] = fmt.Sprint(lists)
				}
				if scenario == "repeated-token" {
					body["items"] = []any{}
					body["nextPageToken"] = "same"
				}
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer srv.Close()
			caller := "original@example.com"
			if scenario == "rotated-caller" {
				caller = "rotated@example.com"
			}
			s := &Session{c: New(srv.URL), key: &Key{ProjectID: "project", ClientEmail: caller}, http: srv.Client()}
			source := &SQLInstance{Name: "database", Project: "project", Region: "europe-west1", ConnectionName: "project:europe-west1:database", CreateTime: created.Format(time.RFC3339Nano)}
			pin, err := source.Incarnation("project", "europe-west1", "database")
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "replacement-history" {
				pin = ""
			}
			proof, err := s.FindSQLCreate(t.Context(), SQLCreateRequest{Instance: "database", Region: "europe-west1", PhysicalID: pin, Intent: "intent", DatabaseVersion: "POSTGRES_17", RequestedAt: requested, Caller: "original@example.com"})
			if scenario == "original" || scenario == "rotated-caller" {
				if err != nil || proof == nil || proof.Operation != "original" || proof.PhysicalID != pin || details != 1 || reads != 2 {
					t.Fatalf("lost exact original native evidence: %+v reads=%d details=%d err=%v", proof, reads, details, err)
				}
			} else if err == nil {
				t.Fatalf("unproven create recovered: %+v", proof)
			}
			if lists > 10 || scenario == "truncated" && lists != 10 {
				t.Fatalf("unbounded discovery: %d", lists)
			}
		})
	}
}

func TestCloudSQLMutationRejectsNoncanonicalOperationIDs(t *testing.T) {
	for _, id := range []string{"encoded%2Fpath", "space in id", ".", "..", "line\nfeed", strings.Repeat("a", 257)} {
		for _, resume := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/resume=%v", id, resume), func(t *testing.T) {
				calls, started := 0, 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls++
					_ = json.NewEncoder(w).Encode(map[string]string{"name": id, "status": "DONE"})
				}))
				defer srv.Close()
				s := &Session{c: New(srv.URL), key: &Key{ProjectID: "project"}, http: srv.Client()}
				track := SQLMutation{Before: func() error { return nil }, Started: func(string) error { started++; return nil }}
				if resume {
					track.Resume = id
				}
				err := s.EnsureDatabase(t.Context(), "database", "app", track)
				if err == nil || started != 0 || resume && calls != 0 {
					t.Fatalf("unsafe operation reference accepted: calls=%d started=%d err=%v", calls, started, err)
				}
			})
		}
	}
}
