package ctl

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBackupRetentionCommandOnlyReadsPreview(t *testing.T) {
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/service-backups/backup_1/retention-preview" || r.URL.Query().Get("older_than_days") != "30" || r.URL.Query().Get("keep_last") != "7" {
			t.Errorf("unexpected retention request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		reads++
		_, _ = w.Write([]byte(`{"complete":true,"eligible_count":1,"eligible_bytes":1024,"points":[{"backup_id":"older-point","bytes":1024,"eligible":true,"reason":"eligible"}]}`))
	}))
	defer srv.Close()
	var out bytes.Buffer
	env := map[string]string{EnvURL: srv.URL, EnvKey: "lp_test"}
	app := New(Options{Out: &out, Getenv: func(k string) string { return env[k] }, ConfigPath: t.TempDir() + "/none.json"})
	if err := app.Run([]string{"backups", "retention", "backup_1", "--older-than-days", "30", "--keep-last", "7", "--json"}); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || !strings.Contains(out.String(), `"eligible_count": 1`) || !strings.Contains(out.String(), "older-point") {
		t.Fatalf("missing preview: reads=%d output=%s", reads, out.String())
	}
	for _, args := range [][]string{
		{"backups", "retention"},
		{"backups", "retention", "backup_1", "--older-than-days", "0"},
		{"backups", "retention", "backup_1", "--keep-last", "0"},
		{"backups", "retention", "backup_1", "--older-than-days", "oops"},
	} {
		if err := app.Run(args); err == nil || reads != 1 {
			t.Fatalf("invalid preview settings reached API: %v, reads=%d err=%v", args, reads, err)
		}
	}
}
