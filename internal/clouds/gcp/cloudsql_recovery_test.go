package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

type sqlRecoveryCreator interface {
	RestoreSQLBackup(context.Context, string, SQLInstanceSpec, SQLMutation) (*SQLInstance, error)
}

func TestCloudSQLRecoveryCreatesOnlyNewConnectorIsolatedCopies(t *testing.T) {
	for _, scenario := range []string{"new", "resume", "waiting-copy", "existing", "before-failure", "started-failure", "guard-failure", "inspect-failure", "foreign-operation", "missing-operation", "missing-operation-metadata", "wrong-operation-type", "failed-operation", "foreign-instance", "missing-creation", "wrong-intent", "wrong-engine", "authorized-network", "connector-disabled", "private-source-network"} {
		t.Run(scenario, func(t *testing.T) {
			writes, polls, inspected, reads := 0, 0, 0, 0
			before, captured := false, scenario == "resume" || scenario == "missing-operation"
			exists := scenario == "existing" || captured
			interrupted := errors.New("durable checkpoint refused")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "/operations/") {
					polls++
					if !captured {
						t.Error("restore polled before the operation was durably saved")
					}
					if scenario == "missing-operation" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					fmt.Fprint(w, `{"kind":"sql#operation","name":"restore-original","status":"DONE","targetProject":"recovery-project","targetId":"recovery-copy","operationType":"RESTORE_VOLUME"}`)
					return
				}
				if r.Method == http.MethodGet {
					reads++
					if r.URL.Path != "/v1/projects/recovery-project/instances/recovery-copy" {
						t.Errorf("restore read another instance: %s", r.URL)
					}
					if !exists {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					project, created, intent, version, state := "recovery-project", "2026-10-23T00:00:00.000000001Z", "original-intent", "POSTGRES_17", "RUNNABLE"
					if scenario == "foreign-instance" {
						project = "another-project"
					}
					if scenario == "missing-creation" {
						created = ""
					}
					if scenario == "wrong-intent" {
						intent = "another-intent"
					}
					if scenario == "wrong-engine" {
						version = "POSTGRES_18"
					}
					if scenario == "waiting-copy" && inspected == 0 {
						state = "PENDING_CREATE"
					}
					network := map[string]any{"ipv4Enabled": true, "authorizedNetworks": []any{}}
					enforcement := "REQUIRED"
					if scenario == "authorized-network" {
						network["authorizedNetworks"] = []any{map[string]string{"value": "0.0.0.0/0"}}
					}
					if scenario == "private-source-network" {
						network["privateNetwork"] = "projects/source-project/global/networks/source"
					}
					if scenario == "connector-disabled" {
						enforcement = "NOT_REQUIRED"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"kind": "sql#instance", "name": "recovery-copy", "project": project, "region": "europe-west1", "connectionName": project + ":europe-west1:recovery-copy", "createTime": created, "databaseVersion": version, "state": state, "settings": map[string]any{"tier": "db-custom-1-3840", "connectorEnforcement": enforcement, "ipConfiguration": network, "userLabels": map[string]string{SQLIntentLabel: intent, "managed-by": "loomplane"}}})
					return
				}
				writes++
				if !before || r.Method != http.MethodPost || r.URL.Path != "/v1/projects/recovery-project/instances/recovery-copy/restoreBackup" {
					t.Errorf("restore write was not checkpointed or used the wrong target: %s %s", r.Method, r.URL)
				}
				var body struct {
					Backup   string `json:"backup"`
					Settings struct {
						Region   string `json:"region"`
						Settings struct {
							Tier      string `json:"tier"`
							Connector string `json:"connectorEnforcement"`
							Network   struct {
								IPv4       bool  `json:"ipv4Enabled"`
								Authorized []any `json:"authorizedNetworks"`
							} `json:"ipConfiguration"`
							Labels map[string]string `json:"userLabels"`
						} `json:"settings"`
					} `json:"restoreInstanceSettings"`
					Clear []string `json:"restoreInstanceClearOverridesFieldNames"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Backup != "projects/source-project/backups/original-backup" || body.Settings.Region != "europe-west1" || body.Settings.Settings.Tier != "db-custom-1-3840" || body.Settings.Settings.Connector != "REQUIRED" || !body.Settings.Settings.Network.IPv4 || len(body.Settings.Settings.Network.Authorized) != 0 || body.Settings.Settings.Labels[SQLIntentLabel] != "original-intent" || !slices.Contains(body.Clear, "settings.ipConfiguration.privateNetwork") {
					t.Error("native restore lost its original backup or isolated destination settings", body)
				}
				exists = true
				op := map[string]any{"kind": "sql#operation", "name": "restore-original", "status": "PENDING", "targetProject": "recovery-project", "targetId": "recovery-copy", "operationType": "RESTORE_VOLUME"}
				switch scenario {
				case "foreign-operation":
					op["targetId"] = "another-copy"
				case "missing-operation-metadata":
					delete(op, "targetProject")
				case "wrong-operation-type":
					op["operationType"] = "CREATE"
				case "failed-operation":
					op["status"], op["error"] = "DONE", map[string]any{"errors": []any{map[string]string{"message": "restore rejected"}}}
				}
				_ = json.NewEncoder(w).Encode(op)
			}))
			defer server.Close()
			s := &Session{c: New(server.URL), key: &Key{ProjectID: "recovery-project"}, http: server.Client()}
			creator, ok := any(s).(sqlRecoveryCreator)
			if !ok {
				t.Fatal("native Cloud SQL backup copies cannot be executed")
			}
			track := SQLMutation{Before: func() error {
				before = true
				if scenario == "before-failure" {
					return interrupted
				}
				return nil
			}, Started: func(id string) error {
				if id != "restore-original" {
					t.Error("captured another operation", id)
				}
				if scenario == "started-failure" {
					return interrupted
				}
				captured = true
				return nil
			}, Check: func(context.Context) error {
				if scenario == "guard-failure" {
					return interrupted
				}
				return nil
			}}
			if scenario == "resume" || scenario == "missing-operation" {
				track.Resume = "restore-original"
			}
			spec := SQLInstanceSpec{Name: "recovery-copy", DatabaseVersion: "POSTGRES_17", Region: "europe-west1", Tier: "db-custom-1-3840", Intent: "original-intent", Inspect: func(*SQLInstance) error {
				inspected++
				if scenario == "inspect-failure" {
					return interrupted
				}
				return nil
			}}
			restored, err := creator.RestoreSQLBackup(t.Context(), "projects/source-project/backups/original-backup", spec, track)
			if scenario == "new" || scenario == "resume" || scenario == "waiting-copy" {
				if err != nil || restored == nil || restored.State != "RUNNABLE" || inspected == 0 || polls == 0 {
					t.Fatalf("native copy did not finish through its captured operation: %+v %v", restored, err)
				}
			} else if err == nil || restored != nil {
				t.Fatalf("unsafe or interrupted native copy succeeded: %+v %v", restored, err)
			}
			if scenario == "resume" || scenario == "missing-operation" || scenario == "existing" || scenario == "before-failure" || scenario == "guard-failure" {
				if writes != 0 {
					t.Fatal("unsafe copy request sent a native write", writes)
				}
			} else if writes != 1 {
				t.Fatal("new copy did not send exactly one restore mutation", writes)
			}
			if scenario == "started-failure" && polls != 0 {
				t.Fatal("unsaved operation was polled")
			}
			if reads > 5 {
				t.Fatal("copy readiness did not converge", reads)
			}
		})
	}
}

type sqlRecoveryRemover interface {
	DeleteSQLRecovery(context.Context, string, string, string, string, SQLMutation) error
}

func TestCloudSQLRecoveryDisposalRequiresOriginalCopyAndCapturedDeletion(t *testing.T) {
	for _, scenario := range []string{"delete", "already-absent", "resume", "reused-name", "wrong-intent", "before-failure", "started-failure", "guard-failure", "missing-operation", "foreign-operation"} {
		t.Run(scenario, func(t *testing.T) {
			writes, polls := 0, 0
			before, captured := false, scenario == "resume" || scenario == "missing-operation"
			exists := scenario != "already-absent" && scenario != "resume"
			interrupted := errors.New("cleanup checkpoint refused")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "/operations/") {
					polls++
					if !captured {
						t.Error("cleanup polled before saving its operation")
					}
					if scenario == "missing-operation" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					exists = false
					fmt.Fprint(w, `{"kind":"sql#operation","name":"delete-original","status":"DONE","targetProject":"recovery-project","targetId":"recovery-copy","operationType":"DELETE"}`)
					return
				}
				if r.Method == http.MethodGet {
					if !exists {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					created, intent := "2026-10-23T00:00:00Z", "original-intent"
					if scenario == "reused-name" {
						created = "2026-10-23T01:00:00Z"
					}
					if scenario == "wrong-intent" {
						intent = "replacement-intent"
					}
					fmt.Fprintf(w, `{"kind":"sql#instance","name":"recovery-copy","project":"recovery-project","region":"europe-west1","connectionName":"recovery-project:europe-west1:recovery-copy","createTime":%q,"state":"RUNNABLE","databaseVersion":"POSTGRES_17","settings":{"userLabels":{"managed-by":"loomplane","loomplane-create-intent":%q}}}`, created, intent)
					return
				}
				writes++
				if !before || r.Method != http.MethodDelete || r.URL.Path != "/v1/projects/recovery-project/instances/recovery-copy" || r.URL.Query().Get("enableFinalBackup") != "false" || r.URL.Query().Has("finalBackupTtlDays") {
					t.Errorf("temporary copy used live-source deletion semantics: %s %s", r.Method, r.URL)
				}
				target := "recovery-copy"
				if scenario == "foreign-operation" {
					target = "another-copy"
				}
				fmt.Fprintf(w, `{"kind":"sql#operation","name":"delete-original","status":"PENDING","targetProject":"recovery-project","targetId":%q,"operationType":"DELETE"}`, target)
			}))
			defer server.Close()
			s := &Session{c: New(server.URL), key: &Key{ProjectID: "recovery-project"}, http: server.Client()}
			remover, ok := any(s).(sqlRecoveryRemover)
			if !ok {
				t.Fatal("native recovery copies have no identity-checked disposal path")
			}
			original := &SQLInstance{Name: "recovery-copy", Project: "recovery-project", Region: "europe-west1", ConnectionName: "recovery-project:europe-west1:recovery-copy", CreateTime: "2026-10-23T00:00:00Z"}
			pin, err := original.Incarnation("recovery-project", "europe-west1", "recovery-copy")
			if err != nil {
				t.Fatal(err)
			}
			track := SQLMutation{Before: func() error {
				before = true
				if scenario == "before-failure" {
					return interrupted
				}
				return nil
			}, Started: func(id string) error {
				if id != "delete-original" {
					t.Error("saved another deletion operation", id)
				}
				if scenario == "started-failure" {
					return interrupted
				}
				captured = true
				return nil
			}, Check: func(context.Context) error {
				if scenario == "guard-failure" {
					return interrupted
				}
				return nil
			}}
			if scenario == "resume" || scenario == "missing-operation" {
				track.Resume = "delete-original"
			}
			err = remover.DeleteSQLRecovery(t.Context(), "recovery-copy", "europe-west1", pin, "original-intent", track)
			if scenario == "delete" || scenario == "already-absent" || scenario == "resume" {
				if err != nil || exists {
					t.Fatal("owned copy disposal did not finish", err)
				}
			} else if err == nil {
				t.Fatal("unsafe or interrupted cleanup succeeded")
			}
			if scenario == "delete" || scenario == "started-failure" || scenario == "foreign-operation" {
				if writes != 1 {
					t.Fatal("cleanup did not send exactly one owned deletion", writes)
				}
			} else if writes != 0 {
				t.Fatal("cleanup replayed or sent an unsafe deletion", writes)
			}
			if scenario == "started-failure" && polls != 0 {
				t.Fatal("cleanup polled an unsaved deletion")
			}
		})
	}
}
