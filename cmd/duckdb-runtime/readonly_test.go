package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadonlyStartupLocksBeforeExecutingBoundTLSListener(t *testing.T) {
	sql := startupSQL(strings.Repeat("a", 64), true)
	previous := -1
	for _, part := range []string{"LOAD quack;", "PREPARE loomplane_readonly_listener AS SELECT * FROM quack_serve(", "ssl_key_file := '/etc/loomplane/node-key.pem'", "SET temp_directory='';", "SET enable_external_access=false;", "SET lock_configuration=true;", "EXECUTE loomplane_readonly_listener;", "DEALLOCATE loomplane_readonly_listener;"} {
		at := strings.Index(sql, part)
		if at < 0 || at <= previous {
			t.Fatalf("startup omitted or reordered %q", part)
		}
		previous = at
	}
	if strings.Contains(sql, "allowed_paths") || strings.Contains(sql, "allowed_directories") {
		t.Fatal("TLS files were exposed through an SQL allow-list")
	}
	writable := startupSQL(strings.Repeat("a", 64), false)
	if !strings.Contains(writable, "CALL quack_serve(") || strings.Contains(writable, "enable_external_access") || strings.Contains(writable, "PREPARE") {
		t.Fatal("normal writable startup changed")
	}
}

func TestReadonlyRuntimeRefusesMissingOrSymlinkDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "analytics.db")
	if err := validateReadonlyDatabase(path); err == nil {
		t.Fatal("missing recovery database accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("missing recovery database was initialized")
	}
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateReadonlyDatabase(path); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "analytics.db")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := validateReadonlyDatabase(link); err == nil {
		t.Fatal("symlink recovery database accepted")
	}
}
