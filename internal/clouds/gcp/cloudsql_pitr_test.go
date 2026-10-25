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

func pscPITRInstance(t *testing.T) (*SQLInstance, map[string]any) {
	t.Helper()
	raw := `{"kind":"sql#instance","name":"source-db","project":"source-project","region":"us-central1","connectionName":"source-project:us-central1:source-db","createTime":"2026-10-20T00:00:00Z","databaseVersion":"POSTGRES_17","state":"RUNNABLE","settings":{"settingsVersion":"42","edition":"ENTERPRISE","availabilityType":"ZONAL","tier":"db-custom-1-3840","dataDiskSizeGb":"10","dataDiskType":"PD_SSD","storageAutoResize":false,"connectorEnforcement":"REQUIRED","dataApiAccess":"DISALLOW_DATA_API","backupConfiguration":{"enabled":true,"pointInTimeRecoveryEnabled":true,"transactionLogRetentionDays":7},"ipConfiguration":{"ipv4Enabled":false,"pscConfig":{"pscEnabled":true,"allowedConsumerProjects":["worker-project","app-project"]}}}}`
	var in SQLInstance
	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatal(err)
	}
	return &in, fields
}

func pscPITRSpec() SQLInstanceSpec {
	return SQLInstanceSpec{Name: "source-db", DatabaseVersion: "POSTGRES_17", Region: "us-central1", Tier: "db-custom-1-3840", Storage: &SQLStorageSettings{SizeGB: 10, Type: "PD_SSD", RecoveryDays: 7}}
}

func TestSQLPITRIsolationRefusesInheritedNetworkAndCapacityExposure(t *testing.T) {
	for _, scenario := range []string{"psc-only", "public", "missing-ipv4", "missing-network", "no-psc", "psc-disabled", "outbound-attachment", "auto-endpoint", "auto-policy", "psa", "google-path", "authorized-network", "data-api", "unknown-data-api", "connector-disabled", "public-address", "missing-version", "wrong-engine", "wrong-edition", "replica", "disk-growth", "autoresize", "backups-disabled", "logs-disabled", "retention-changed"} {
		t.Run(scenario, func(t *testing.T) {
			_, fields := pscPITRInstance(t)
			settings := fields["settings"].(map[string]any)
			network := settings["ipConfiguration"].(map[string]any)
			psc := network["pscConfig"].(map[string]any)
			backup := settings["backupConfiguration"].(map[string]any)
			switch scenario {
			case "public":
				network["ipv4Enabled"] = true
			case "missing-ipv4":
				delete(network, "ipv4Enabled")
			case "missing-network":
				delete(settings, "ipConfiguration")
			case "no-psc":
				delete(network, "pscConfig")
			case "psc-disabled":
				psc["pscEnabled"] = false
			case "outbound-attachment":
				psc["networkAttachmentUri"] = "projects/source-project/regions/us-central1/networkAttachments/production"
			case "auto-endpoint":
				psc["pscAutoConnections"] = []any{map[string]string{"consumerNetwork": "projects/source-project/global/networks/production"}}
			case "auto-policy":
				psc["pscAutoConnectionPolicyEnabled"] = true
			case "psa":
				network["privateNetwork"] = "projects/source-project/global/networks/production"
			case "google-path":
				network["enablePrivatePathForGoogleCloudServices"] = true
			case "authorized-network":
				network["authorizedNetworks"] = []any{map[string]string{"value": "0.0.0.0/0"}}
			case "data-api":
				settings["dataApiAccess"] = "ALLOW_DATA_API"
			case "unknown-data-api":
				settings["dataApiAccess"] = "FUTURE_ACCESS_MODE"
			case "connector-disabled":
				settings["connectorEnforcement"] = "NOT_REQUIRED"
			case "public-address":
				fields["ipAddresses"] = []any{map[string]string{"type": "PRIMARY", "ipAddress": "198.51.100.1"}}
			case "missing-version":
				delete(settings, "settingsVersion")
			case "wrong-engine":
				fields["databaseVersion"] = "POSTGRES_18"
			case "wrong-edition":
				settings["edition"] = "ENTERPRISE_PLUS"
			case "replica":
				fields["masterInstanceName"] = "production-primary"
			case "disk-growth":
				settings["dataDiskSizeGb"] = "1000"
			case "autoresize":
				settings["storageAutoResize"] = true
			case "backups-disabled":
				backup["enabled"] = false
			case "logs-disabled":
				backup["pointInTimeRecoveryEnabled"] = false
			case "retention-changed":
				backup["transactionLogRetentionDays"] = 1
			}
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			var in SQLInstance
			if err = json.Unmarshal(raw, &in); err != nil {
				t.Fatal(err)
			}
			digest, err := in.PITRIsolation(pscPITRSpec())
			if scenario == "psc-only" {
				if err != nil || len(digest) != 64 {
					t.Fatalf("reviewed PSC source refused: %q %v", digest, err)
				}
			} else if !errors.Is(err, ErrSQLPITRUnavailable) || digest != "" {
				t.Fatalf("unsafe inherited settings admitted (%s): %q %v", scenario, digest, err)
			}
		})
	}
}

func TestSQLPITRWindowBindsFreshBoundsToOneIsolatedIncarnation(t *testing.T) {
	for _, scenario := range []string{"available", "unsafe-before", "unsafe-after", "replacement", "settings-revision", "allowlist-change", "allowlist-reorder", "missing-source", "wrong-kind", "missing-earliest", "future-latest", "inverted", "pre-creation", "window-error"} {
		t.Run(scenario, func(t *testing.T) {
			original, _ := pscPITRInstance(t)
			pin, err := original.Incarnation("source-project", "us-central1", "source-db")
			if err != nil {
				t.Fatal(err)
			}
			reads, windows := 0, 0
			earliest, latest := time.Date(2026, 10, 21, 0, 0, 0, 123, time.UTC), time.Now().UTC().Add(-time.Minute)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != http.MethodGet {
					t.Fatalf("window read mutated the source: %s", r.Method)
				}
				if strings.HasSuffix(r.URL.Path, "/getLatestRecoveryTime") {
					windows++
					if r.URL.Path != "/v1/projects/source-project/instances/source-db/getLatestRecoveryTime" || r.URL.Query().Get("location") != "us-central1" {
						t.Errorf("window read escaped captured placement: %s", r.URL)
					}
					body := map[string]any{"kind": "sql#getLatestRecoveryTime", "earliestRecoveryTime": earliest, "latestRecoveryTime": latest}
					switch scenario {
					case "wrong-kind":
						body["kind"] = "sql#instance"
					case "missing-earliest":
						delete(body, "earliestRecoveryTime")
					case "future-latest":
						body["latestRecoveryTime"] = time.Now().Add(time.Hour)
					case "inverted":
						body["earliestRecoveryTime"] = latest.Add(time.Second)
					case "pre-creation":
						body["earliestRecoveryTime"] = time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
					case "window-error":
						w.WriteHeader(http.StatusForbidden)
						return
					}
					_ = json.NewEncoder(w).Encode(body)
					return
				}
				reads++
				_, fields := pscPITRInstance(t)
				settings := fields["settings"].(map[string]any)
				network := settings["ipConfiguration"].(map[string]any)
				if scenario == "missing-source" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if scenario == "unsafe-before" || reads > 1 && scenario == "unsafe-after" {
					network["ipv4Enabled"] = true
				}
				if reads > 1 {
					switch scenario {
					case "replacement":
						fields["createTime"] = "2026-10-20T00:01:00Z"
					case "settings-revision":
						settings["settingsVersion"] = "43"
					case "allowlist-change":
						network["pscConfig"].(map[string]any)["allowedConsumerProjects"] = []string{"another-project"}
					case "allowlist-reorder":
						network["pscConfig"].(map[string]any)["allowedConsumerProjects"] = []string{"app-project", "worker-project"}
					}
				}
				_ = json.NewEncoder(w).Encode(fields)
			}))
			defer server.Close()
			s := &Session{c: New(server.URL), key: &Key{ProjectID: "source-project"}, http: server.Client()}
			window, err := s.ReadSQLPITRWindow(t.Context(), "us-central1", "source-db", pin, pscPITRSpec())
			if scenario == "available" || scenario == "allowlist-reorder" {
				if err != nil || window == nil || window.PhysicalID != pin || len(window.SettingsDigest) != 64 || !window.Earliest.Equal(earliest) || !window.Latest.Equal(latest) || !window.CreatedAt.Equal(time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)) || reads != 2 || windows != 1 {
					t.Fatalf("fresh window lost its source or exact boundaries: %+v %v reads=%d windows=%d", window, err, reads, windows)
				}
			} else if err == nil || window != nil {
				t.Fatalf("unverifiable PITR window admitted: %+v %v", window, err)
			}
			if (scenario == "unsafe-before" || scenario == "missing-source") && windows != 0 {
				t.Fatal("unsafe source was queried for a recoverable window")
			}
		})
	}
}

func TestSQLPITRCloneUsesReviewedTimeAndCapturedOperation(t *testing.T) {
	for _, scenario := range []string{"clone", "resume", "existing", "foreign-project", "unsafe-source", "changed-settings", "expired-time", "missing-operation", "foreign-operation", "failed-operation", "wrong-kind", "missing-timing", "reused-name", "unsafe-copy", "inspect-failure", "checkpoint-failure", "capture-failure", "guard-failure"} {
		t.Run(scenario, func(t *testing.T) {
			source, _ := pscPITRInstance(t)
			pin, err := source.Incarnation("source-project", "us-central1", "source-db")
			if err != nil {
				t.Fatal(err)
			}
			digest, err := source.PITRIsolation(pscPITRSpec())
			if err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 10, 22, 12, 0, 0, 123456789, time.UTC)
			now := time.Now().UTC().Add(-time.Minute)
			op := map[string]any{"kind": "sql#operation", "name": "clone-original", "status": "DONE", "targetProject": "source-project", "targetId": "recovery-copy", "operationType": "CLONE", "insertTime": now.Add(-time.Second), "endTime": now.Add(time.Second)}
			writes, inspected := 0, 0
			captured, before := scenario == "resume" || scenario == "missing-operation", false
			exists := captured || scenario == "existing"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "/operations/") {
					if !captured {
						t.Error("operation polled before durable capture")
					}
					if scenario == "missing-operation" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_ = json.NewEncoder(w).Encode(op)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/getLatestRecoveryTime") {
					earliest := at.Add(-time.Hour)
					if scenario == "expired-time" {
						earliest = at.Add(time.Hour)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"kind": "sql#getLatestRecoveryTime", "earliestRecoveryTime": earliest, "latestRecoveryTime": now})
					return
				}
				if r.Method == http.MethodPost {
					writes++
					if !before || r.URL.Path != "/v1/projects/source-project/instances/source-db/clone" {
						t.Errorf("uncheckpointed or misrouted clone: %s %s", r.Method, r.URL)
					}
					var body map[string]map[string]any
					if decodeErr := json.NewDecoder(r.Body).Decode(&body); decodeErr != nil {
						t.Error(decodeErr)
					}
					clone := body["cloneContext"]
					if len(body) != 1 || len(clone) != 3 || clone["kind"] != "sql#cloneContext" || clone["destinationInstanceName"] != "recovery-copy" || clone["pointInTime"] != at.Format(time.RFC3339Nano) {
						t.Error("clone lost exact UTC selection or invented settings overrides", body)
					}
					exists = true
					switch scenario {
					case "foreign-operation":
						op["targetId"] = "someone-elses-copy"
					case "failed-operation":
						op["error"] = map[string]any{"errors": []any{map[string]string{"message": "refused"}}}
					case "wrong-kind":
						op["kind"] = "sql#instance"
					case "missing-timing":
						delete(op, "endTime")
					}
					_ = json.NewEncoder(w).Encode(op)
					return
				}
				if r.Method != http.MethodGet {
					t.Fatalf("unexpected source/copy mutation: %s %s", r.Method, r.URL)
				}
				_, fields := pscPITRInstance(t)
				if strings.HasSuffix(r.URL.Path, "/source-db") {
					if scenario == "unsafe-source" {
						fields["settings"].(map[string]any)["ipConfiguration"].(map[string]any)["ipv4Enabled"] = true
					}
					if scenario == "changed-settings" {
						fields["settings"].(map[string]any)["settingsVersion"] = "43"
					}
				} else {
					if !exists {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					fields["name"], fields["connectionName"], fields["createTime"] = "recovery-copy", "source-project:us-central1:recovery-copy", now
					if scenario == "reused-name" {
						fields["createTime"] = now.Add(time.Hour)
					}
					if scenario == "unsafe-copy" {
						fields["settings"].(map[string]any)["ipConfiguration"].(map[string]any)["ipv4Enabled"] = true
					}
				}
				_ = json.NewEncoder(w).Encode(fields)
			}))
			defer server.Close()
			s := &Session{c: New(server.URL), key: &Key{ProjectID: "source-project"}, http: server.Client()}
			target := pscPITRSpec()
			target.Name, target.Intent = "recovery-copy", "copy-intent"
			target.Inspect = func(*SQLInstance) error {
				inspected++
				if scenario == "inspect-failure" {
					return errors.New("capture unavailable")
				}
				return nil
			}
			track := SQLMutation{Before: func() error {
				before = true
				if scenario == "checkpoint-failure" {
					return errors.New("checkpoint unavailable")
				}
				return nil
			}, Started: func(string) error {
				if scenario == "capture-failure" {
					return errors.New("capture unavailable")
				}
				captured = true
				return nil
			}, Check: func(context.Context) error {
				if scenario == "guard-failure" {
					return errors.New("worker revoked")
				}
				return nil
			}}
			if scenario == "resume" || scenario == "missing-operation" {
				track.Resume = "clone-original"
			}
			if scenario == "foreign-project" {
				s.key.ProjectID = "another-project"
			}
			in, err := s.CloneSQLPointInTime(t.Context(), pscPITRSpec(), pin, digest, at, target, track)
			if scenario == "clone" || scenario == "resume" {
				if err != nil || in == nil || inspected != 1 {
					t.Fatalf("captured clone did not complete: %+v %v inspected=%d", in, err, inspected)
				}
			} else if err == nil || in != nil {
				t.Fatalf("unsafe clone succeeded: %+v %v", in, err)
			}
			if scenario == "existing" || scenario == "foreign-project" || scenario == "unsafe-source" || scenario == "changed-settings" || scenario == "expired-time" || scenario == "missing-operation" || scenario == "resume" || scenario == "checkpoint-failure" || scenario == "guard-failure" {
				if writes != 0 {
					t.Fatal("unsafe or resumed clone sent a mutation", writes)
				}
			} else if writes != 1 {
				t.Fatal("new clone did not send exactly one mutation", writes)
			}
			if (scenario == "reused-name" || scenario == "missing-timing") && inspected != 0 {
				t.Fatal("unproven creation was captured for cleanup")
			}
			if scenario == "unsafe-copy" && inspected != 1 {
				t.Fatal("failed isolation lost the captured original copy needed for cleanup")
			}
		})
	}
}
