package ctl

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/client"
)

func recoveryCLI(t *testing.T, handler http.HandlerFunc) (*App, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	app := New(Options{Out: &out, Getenv: func(key string) string {
		switch key {
		case EnvURL:
			return srv.URL
		case EnvKey:
			return "lp_test"
		}
		return ""
	}, ConfigPath: t.TempDir() + "/none.json"})
	return app, &out
}

func TestRecoveryInvalidTimeoutDoesNotContactTheAPI(t *testing.T) {
	commands := [][]string{
		{"backups", "create", "database"},
		{"backups", "retry", "backup_1"},
		{"backups", "show", "backup_1"},
		{"restores", "create", "backup_1"},
		{"restores", "show", "restore_1"},
		{"services", "create", "Database", "--target", "conn_1"},
	}
	for _, command := range commands {
		for _, timeout := range []string{"0", "-1s"} {
			t.Run(strings.Join(command[:2], "-")+"/"+timeout, func(t *testing.T) {
				requests := 0
				app, _ := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
					requests++
					if r.URL.Path == "/api/v1/projects" {
						_, _ = w.Write([]byte(`{"projects":[{"id":"p","slug":"database"}]}`))
						return
					}
					_, _ = w.Write([]byte(`{"id":"operation","status":"queued"}`))
				})
				args := append(append([]string{}, command...), "--timeout", timeout)
				if command[0] != "services" {
					args = append(args, "--wait")
				}
				if err := app.Run(args); err == nil || !strings.Contains(err.Error(), "timeout") || requests != 0 {
					t.Fatalf("invalid timeout: err=%v, requests=%d", err, requests)
				}
			})
		}
	}
}

func TestRecoveryWaitTimeoutRetainsAcceptedOperationJSON(t *testing.T) {
	previous := client.Every
	client.Every = time.Millisecond
	t.Cleanup(func() { client.Every = previous })
	for _, kind := range []string{"backups", "restores"} {
		t.Run(kind, func(t *testing.T) {
			app, out := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/projects" {
					_, _ = w.Write([]byte(`{"projects":[{"id":"p","slug":"database"}]}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"accepted_operation","status":"running"}`))
			})
			arg := "database"
			if kind == "restores" {
				arg = "backup_1"
			}
			err := app.Run([]string{kind, "create", arg, "--wait", "--timeout", "20ms", "--json"})
			var record struct {
				ID string `json:"id"`
			}
			if err == nil || !strings.Contains(err.Error(), "accepted_operation") || json.Unmarshal(out.Bytes(), &record) != nil || record.ID != "accepted_operation" {
				t.Fatalf("lost accepted operation after wait timeout: err=%v output=%s", err, out)
			}
		})
	}
}

func TestBackupCreateWaitReturnsTerminalJSON(t *testing.T) {
	previous := client.Every
	client.Every = time.Millisecond
	t.Cleanup(func() { client.Every = previous })
	reads := 0
	app, out := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/projects":
			_, _ = w.Write([]byte(`{"projects":[{"id":"p","slug":"database"}]}`))
		case r.URL.Path == "/api/v1/projects/p/service-backups" && r.Method == http.MethodPost:
			var body client.BackupRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RequestID != "stable-backup" || body.Environment != "preview" || body.Resource != "orders" {
				t.Errorf("backup request: %+v %v", body, err)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"backup_1","status":"queued"}`))
		case r.URL.Path == "/api/v1/service-backups/backup_1":
			reads++
			status := "running"
			if reads > 1 {
				status = "succeeded"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "backup_1", "status": status, "bytes": 8192})
		default:
			http.NotFound(w, r)
		}
	})
	if err := app.Run([]string{"backups", "create", "database", "--resource", "orders", "--env", "preview", "--request-id", "stable-backup", "--wait", "--json"}); err != nil {
		t.Fatal(err)
	}
	var result client.ServiceBackup
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Status != "succeeded" || result.Bytes != 8192 || reads < 2 {
		t.Fatalf("terminal JSON: %s (%v), reads %d", out, err, reads)
	}
}

func TestRestoreWaitReportsFailureWithoutDeletingTheCopy(t *testing.T) {
	deleted := false
	app, out := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/v1/service-backups/backup_1/restore":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"restore_1","status":"queued"}`))
		case r.URL.Path == "/api/v1/service-restores/restore_1":
			_, _ = w.Write([]byte(`{"id":"restore_1","status":"failed","error":"SQL probe failed"}`))
		default:
			http.NotFound(w, r)
		}
	})
	err := app.Run([]string{"restores", "create", "backup_1", "--request-id", "restore-test", "--wait", "--json"})
	if err == nil || !strings.Contains(err.Error(), "SQL probe failed") || deleted {
		t.Fatalf("restore failure: %v, deleted %v, output %s", err, deleted, out)
	}
	if !json.Valid(out.Bytes()) {
		t.Fatalf("failure must retain inspectable JSON: %s", out)
	}
}

func TestBackupPoliciesListsPersistedCadenceWithoutWriting(t *testing.T) {
	mutated := false
	app, out := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutated = true
		}
		switch r.URL.Path {
		case "/api/v1/projects":
			_, _ = w.Write([]byte(`{"projects":[{"id":"p","slug":"database"}]}`))
		case "/api/v1/projects/p/backup-policies":
			_, _ = w.Write([]byte(`{"policies":[{"id":"policy_1","environment":"production","resource":"orders","every_hours":6,"enabled":true}]}`))
		default:
			http.NotFound(w, r)
		}
	})
	if err := app.Run([]string{"backups", "policies", "database", "--json"}); err != nil {
		t.Fatal(err)
	}
	var policies []client.BackupPolicy
	if err := json.Unmarshal(out.Bytes(), &policies); err != nil || len(policies) != 1 || policies[0].EveryHours != 6 || mutated {
		t.Fatalf("saved policies: %s, %v, mutated %v", out, err, mutated)
	}
}

func TestBackupPolicyRejectsInvalidCadenceBeforeWriting(t *testing.T) {
	for _, hours := range []string{"0", "169", "invalid"} {
		t.Run(hours, func(t *testing.T) {
			mutated := false
			app, _ := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					mutated = true
				}
				_, _ = w.Write([]byte(`{"projects":[{"id":"p","slug":"database"}]}`))
			})
			if err := app.Run([]string{"backups", "policy", "database", "--every-hours", hours}); err == nil || mutated {
				t.Fatalf("invalid cadence %s: %v, mutated %v", hours, err, mutated)
			}
		})
	}
}

func TestRecoveryFailedRequestReportsReusableKey(t *testing.T) {
	for _, args := range [][]string{
		{"backups", "create", "database", "--request-id", "reuse-after-disconnect", "--json"},
		{"restores", "create", "backup_1", "--request-id", "reuse-after-disconnect", "--json"},
	} {
		t.Run(args[0], func(t *testing.T) {
			app, _ := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"projects":[{"id":"p","slug":"database"}]}`))
					return
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":"response unavailable"}`))
			})
			if err := app.Run(args); err == nil || !strings.Contains(err.Error(), "reuse-after-disconnect") {
				t.Fatalf("lost request identity: %v", err)
			}
		})
	}
}
