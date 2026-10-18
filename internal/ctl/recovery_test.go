package ctl

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoveryCommandsKeepRequestIdentityAndRequireCopyDeletion(t *testing.T) {
	var requestID string
	deleted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/service-backups/backup_1/restore" && r.Method == http.MethodPost {
			var body struct {
				RequestID string `json:"request_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			requestID = body.RequestID
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"restore_1","status":"queued"}`))
			return
		}
		if r.URL.Path == "/api/v1/service-restores/restore_1" && r.Method == http.MethodDelete {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	var out bytes.Buffer
	env := map[string]string{EnvURL: srv.URL, EnvKey: "lp_test"}
	app := New(Options{Out: &out, Getenv: func(k string) string { return env[k] }, ConfigPath: t.TempDir() + "/none.json"})
	if err := app.Run([]string{"restores", "create", "backup_1", "--request-id", "stable-request-1"}); err != nil {
		t.Fatal(err)
	}
	if requestID != "stable-request-1" || !strings.Contains(out.String(), "restore_1") {
		t.Fatalf("restore request: %s %s", requestID, out.String())
	}
	if err := app.Run([]string{"restores", "delete", "restore_1"}); err == nil || deleted {
		t.Fatal("deleted without an explicit confirmation flag")
	}
	if err := app.Run([]string{"restores", "delete", "restore_1", "--yes"}); err != nil || !deleted {
		t.Fatalf("confirmed cleanup: %v", err)
	}
}
