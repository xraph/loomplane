package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/manageddb"
)

func TestSQLPITRSourceCreatesOnlyManualPSCIngress(t *testing.T) {
	for _, scenario := range []string{"create", "resume", "legacy-public", "bad-profile", "missing-guard", "checkpoint-failed", "foreign-operation", "observed-public", "missing-inspector", "inspector-refused", "source-label-mismatch", "consumer-project-changed", "denied", "existing-denied", "numeric-intent"} {
		t.Run(scenario, func(t *testing.T) {
			_, fields := pscPITRInstance(t)
			settings := fields["settings"].(map[string]any)
			settings["userLabels"] = map[string]string{"managed-by": "loomplane", SQLIntentLabel: "source-intent"}
			settings["ipConfiguration"].(map[string]any)["pscConfig"].(map[string]any)["allowedConsumerProjects"] = []string{"app-project"}
			created, posts, calls, saved := false, 0, 0, ""
			if scenario == "resume" || scenario == "legacy-public" || scenario == "existing-denied" {
				created = true
			}
			if scenario == "legacy-public" || scenario == "observed-public" {
				settings["ipConfiguration"].(map[string]any)["ipv4Enabled"] = true
			}
			if scenario == "source-label-mismatch" {
				settings["userLabels"] = map[string]string{"managed-by": "loomplane", SQLIntentLabel: "another-intent"}
			}
			if scenario == "consumer-project-changed" {
				settings["ipConfiguration"].(map[string]any)["pscConfig"].(map[string]any)["allowedConsumerProjects"] = []string{"other-project"}
			}
			var request map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/source-project/instances":
					posts++
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					created = true
					target := "source-db"
					if scenario == "foreign-operation" {
						target = "another-db"
					}
					fmt.Fprintf(w, `{"kind":"sql#operation","name":"create-source","status":"DONE","targetProject":"source-project","targetId":%q,"operationType":"CREATE"}`, target)
				case r.URL.Path == "/v1/projects/source-project/operations/create-source":
					fmt.Fprint(w, `{"kind":"sql#operation","name":"create-source","status":"DONE","targetProject":"source-project","targetId":"source-db","operationType":"CREATE"}`)
				case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/source-project/instances/source-db":
					if !created {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_ = json.NewEncoder(w).Encode(fields)
				default:
					t.Errorf("unexpected provider call %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer srv.Close()
			s := &Session{c: New(srv.URL), key: &Key{ProjectID: "source-project"}, http: srv.Client()}
			spec := pscPITRSpec()
			spec.Storage.RetainedBackups = 7
			inspections := 0
			spec.Intent, spec.Inspect = "source-intent", func(*SQLInstance) error { inspections++; return nil }
			if scenario == "numeric-intent" {
				spec.Intent = strings.Repeat("1", 60)
				settings["userLabels"] = map[string]string{"managed-by": "loomplane", SQLIntentLabel: spec.Intent}
			}
			track := SQLMutation{Before: func() error {
				if scenario == "checkpoint-failed" {
					return errors.New("store unavailable")
				}
				return nil
			}, Started: func(op string) error { saved = op; return nil }, Check: func(context.Context) error { return nil }}
			switch scenario {
			case "resume":
				track.Resume = "create-source"
			case "legacy-public", "existing-denied":
				spec.Existing = true
			case "bad-profile":
				spec.Storage.RecoveryDays = 0
			case "missing-guard":
				track.Check = nil
			case "missing-inspector":
				spec.Inspect = nil
			case "inspector-refused":
				spec.Inspect = func(*SQLInstance) error { return errors.New("incarnation rejected") }
			}
			if scenario == "denied" || scenario == "existing-denied" {
				track.Check = func(context.Context) error { return errors.New("access revoked") }
			}
			_, err := s.EnsureSQLPITRSource(t.Context(), spec, manageddb.CloudSQLSourceEndpoint{ConsumerProject: "app-project", Network: "projects/app-project/global/networks/apps", ForwardingRule: "projects/app-project/regions/us-central1/forwardingRules/database"}, track)
			good := scenario == "create" || scenario == "resume" || scenario == "numeric-intent"
			if good != (err == nil) {
				t.Fatalf("source contract: %v", err)
			}
			if scenario == "bad-profile" || scenario == "missing-guard" || scenario == "missing-inspector" || scenario == "denied" || scenario == "existing-denied" {
				if calls != 0 {
					t.Fatal("invalid source contract reached provider")
				}
			}
			if scenario == "checkpoint-failed" || scenario == "legacy-public" || scenario == "resume" {
				if posts != 0 {
					t.Fatal("source was recreated or patched")
				}
			}
			if scenario == "source-label-mismatch" && inspections != 0 {
				t.Fatal("unowned source was checkpointed")
			}
			if scenario == "create" {
				if saved != "create-source" || posts != 1 {
					t.Fatal("source operation not captured", saved, posts)
				}
				got := request["settings"].(map[string]any)
				network := got["ipConfiguration"].(map[string]any)
				psc := network["pscConfig"].(map[string]any)
				if network["ipv4Enabled"] != false || network["privateNetwork"] != "" || network["enablePrivatePathForGoogleCloudServices"] != false || len(network["authorizedNetworks"].([]any)) != 0 || psc["pscEnabled"] != true || psc["pscAutoConnectionPolicyEnabled"] != false || psc["pscAutoDnsEnabled"] != false || psc["pscWriteEndpointDnsEnabled"] != false || psc["networkAttachmentUri"] != "" || len(psc["pscAutoConnections"].([]any)) != 0 || fmt.Sprint(psc["allowedConsumerProjects"]) != "[app-project]" || got["connectorEnforcement"] != "REQUIRED" || got["dataApiAccess"] != "DISALLOW_DATA_API" {
					t.Fatalf("source can inherit an unreviewed network path: %#v", got)
				}
			}
		})
	}
}

func TestCloudSQLPSCProfileIsExplicitAndPreservesDefault(t *testing.T) {
	p, err := manageddb.CloudSQLPSCPostgres("17")
	if err != nil || p.ID != "postgresql-17-cloud-sql-psc" || p.Access != "cloud-sql-psc-connector" {
		t.Fatal(p, err)
	}
	legacy, err := manageddb.Postgres("cloud-sql", "17")
	if err != nil || legacy.Access != "cloud-sql-connector" || strings.Contains(legacy.ID, "psc") {
		t.Fatal("legacy profile changed", legacy, err)
	}
	listed := manageddb.Profiles("cloud-sql")
	if len(listed) != 2 || listed[0] != *legacy || listed[1] != *p {
		t.Fatal("catalog must offer explicit PSC without changing the default", listed)
	}
	if _, err = manageddb.CloudSQLPSCPostgres("16"); err == nil {
		t.Fatal("unsupported engine admitted")
	}
}
