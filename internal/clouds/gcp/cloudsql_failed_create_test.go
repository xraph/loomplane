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

func TestSQLFailedCreateRequiresOriginalTerminalEvidence(t *testing.T) {
	for _, scenario := range []string{"partial", "absent", "success", "pending", "denied", "missing-operation", "foreign-target", "foreign-project", "foreign-type", "foreign-caller", "warning", "missing-time", "before-intent", "replacement", "label", "pinned-replacement", "lease"} {
		t.Run(scenario, func(t *testing.T) {
			at := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
			req := SQLCreateRequest{Instance: "database", Region: "europe-west1", Intent: "intent", DatabaseVersion: "POSTGRES_17", RequestedAt: at, Caller: "creator@example.com"}
			if scenario == "pinned-replacement" {
				req.PhysicalID = "foreign"
			}
			reads := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("cleanup inspection wrote to provider")
				}
				reads++
				if strings.Contains(r.URL.Path, "/operations/") {
					if scenario == "missing-operation" {
						w.WriteHeader(404)
						return
					}
					op := map[string]any{"kind": "sql#operation", "name": "create", "targetProject": "project", "targetId": "database", "operationType": "CREATE", "status": "DONE", "user": req.Caller, "insertTime": at.Add(time.Second), "startTime": at.Add(2 * time.Second), "endTime": at.Add(4 * time.Second), "error": map[string]any{}}
					switch scenario {
					case "success":
						delete(op, "error")
					case "pending":
						op["status"] = "RUNNING"
					case "foreign-target":
						op["targetId"] = "other"
					case "foreign-project":
						op["targetProject"] = "other"
					case "foreign-type":
						op["operationType"] = "CREATE_USER"
					case "foreign-caller":
						op["user"] = "other"
					case "warning":
						op["apiWarning"] = map[string]any{}
					case "missing-time":
						delete(op, "endTime")
					case "before-intent":
						op["insertTime"] = at.Add(-time.Second)
					}
					_ = json.NewEncoder(w).Encode(op)
					return
				}
				if scenario == "absent" {
					w.WriteHeader(404)
					return
				}
				if scenario == "denied" {
					w.WriteHeader(403)
					return
				}
				created, label := at.Add(3*time.Second), req.Intent
				if scenario == "replacement" {
					created = created.Add(time.Hour)
				}
				if scenario == "label" {
					label = "other"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"kind": "sql#instance", "name": req.Instance, "project": "project", "region": req.Region, "connectionName": "project:" + req.Region + ":" + req.Instance, "createTime": created, "databaseVersion": req.DatabaseVersion, "state": "FAILED", "settings": map[string]any{"userLabels": map[string]string{SQLIntentLabel: label, "managed-by": "loomplane"}}})
			}))
			defer srv.Close()
			s := &Session{c: New(srv.URL), key: &Key{ProjectID: "project"}, http: srv.Client()}
			proof, err := s.InspectSQLFailedCreate(t.Context(), req, "create", func(context.Context) error {
				if scenario == "lease" {
					return errors.New("lease lost")
				}
				return nil
			})
			switch scenario {
			case "partial", "absent":
				if err != nil || proof == nil || proof.Absent != (scenario == "absent") {
					t.Fatal(proof, err)
				}
				if err = proof.Validate("project", req.Region, req.Instance, proof.PhysicalID, req.Intent, req.DatabaseVersion); err != nil {
					t.Fatal(err)
				}
			case "success", "pending":
				if err != nil || proof != nil {
					t.Fatal(proof, err)
				}
			default:
				if err == nil {
					t.Fatal("unproven cleanup accepted", proof)
				}
			}
			if scenario == "lease" && reads != 0 {
				t.Fatal("read after lease loss")
			}
		})
	}
}
