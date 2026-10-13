package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasGitHubBlock(t *testing.T) {
	for _, tt := range []struct {
		name, text string
		want       bool
	}{
		{"plain", "github:\n  app_id: 42\n", true},
		{"quoted", "'github': {app_id: 42}\n", true},
		{"flow", "{github: {app_id: 42}}\n", true},
		{"nested", "other:\n  github: value\n", false},
		{"comment", "# github: configured later\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.text), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := hasGitHubBlock(path)
			if err != nil || got != tt.want {
				t.Fatalf("got %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestSaveRefusesToOverwriteAKey(t *testing.T) {
	dir := t.TempDir()
	o := options{key: filepath.Join(dir, "key.pem"), config: filepath.Join(dir, "config.yaml")}
	if err := os.WriteFile(o.key, []byte("existing key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := save(o, &createdApp{PEM: "new key"}); err == nil {
		t.Fatal("overwrote an existing key")
	}
	got, err := os.ReadFile(o.key)
	if err != nil || string(got) != "existing key" {
		t.Fatalf("key changed: %v", err)
	}
}

func TestSaveMakesExistingConfigPrivate(t *testing.T) {
	dir := t.TempDir()
	o := options{key: filepath.Join(dir, "key.pem"), config: filepath.Join(dir, "config.yaml"), webhook: true}
	if err := os.WriteFile(o.config, []byte("public_url: http://localhost:8780\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := save(o, &createdApp{ID: 42, Slug: "test", ClientID: "client", ClientSecret: "secret", PEM: "key", WebhookSecret: "hook"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{o.key, o.config} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %o", filepath.Base(path), info.Mode().Perm())
		}
	}
	has, err := hasGitHubBlock(o.config)
	if err != nil || !has {
		t.Fatalf("saved config: %v, %v", has, err)
	}
}
