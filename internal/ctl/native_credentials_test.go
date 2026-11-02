package ctl

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeCredentialsSavePrivatelyWithoutPrintingOrOverwriting(t *testing.T) {
	calls := 0
	app, out := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/native-recovery-copies/copy/credentials" {
			t.Error("wrong credential request", r.Method, r.URL)
		}
		fmt.Fprint(w, `{"copy_id":"copy","provider":"cloud-sql","connection_name":"recovery:region:copy","database":"orders","username":"reader","password":"kept-private-reader","expires_at":"2026-10-24T01:00:00Z","checked_at":"2026-10-23T23:00:00Z"}`)
	})
	file := filepath.Join(t.TempDir(), "copy.json")
	if err := app.Run([]string{"backups", "native-copies", "credentials", "copy", "--out", file}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(raw), `"password": "kept-private-reader"`) {
		t.Fatal("credentials not saved", err)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("credential file is not private", err)
	}
	if strings.Contains(out.String(), "kept-private-reader") {
		t.Fatal("credentials printed to normal output")
	}
	if err = app.Run([]string{"backups", "native-copies", "credentials", "copy", "--out", file}); err == nil {
		t.Fatal("overwrote a credential file")
	}
	if err = app.Run([]string{"backups", "native-copies", "credentials", "copy"}); err == nil {
		t.Fatal("disclosed without an explicit output file")
	}
	if calls != 1 {
		t.Fatal("invalid or existing destination contacted credential API", calls)
	}
}

func TestNativePSCCredentialsRetainRequiredTransport(t *testing.T) {
	app, out := recoveryCLI(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"copy_id":"copy","provider":"cloud-sql","private_service_connect":true,"connection_name":"source:region:copy","database":"orders","username":"reader","password":"kept-private-reader","expires_at":"2099-01-01T00:00:00Z","checked_at":"2026-10-25T00:00:00Z"}`)
	})
	file := filepath.Join(t.TempDir(), "psc.json")
	if err := app.Run([]string{"backups", "native-copies", "credentials", "copy", "--out", file}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(raw), `"private_service_connect": true`) || !strings.Contains(out.String(), "--psc") || strings.Contains(out.String(), "kept-private-reader") {
		t.Fatal("PSC transport instructions missing or secret exposed", err)
	}
}
