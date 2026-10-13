package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type mapGetter map[string]any

func (m mapGetter) Get(key string) any { return m[key] }

const testDSN = "postgres://u:p@localhost:5432/db?sslmode=disable"

func TestFromSource_defaults(t *testing.T) {
	c, err := FromSource(mapGetter{"database.url": testDSN})
	if err != nil {
		t.Fatalf("FromSource: %v", err)
	}
	want := Defaults()
	want.Database.URL = testDSN
	if c.HTTPAddr != want.HTTPAddr || c.Identity.AppID != want.Identity.AppID || c.Orchestration != want.Orchestration {
		t.Fatalf("got %+v, want defaults %+v", c, want)
	}
}

func TestFromSource_overrides(t *testing.T) {
	c, err := FromSource(mapGetter{
		"database.url":                        testDSN,
		"database.pool_size":                  "8",
		"http_addr":                           ":9000",
		"identity.require_email_verification": "yes",
		"orchestration.concurrency":           2,
		"orchestration.poll_interval":         "250ms",
		"dashboard.dir":                       "/srv/dashboard",
	})
	if err != nil {
		t.Fatalf("FromSource: %v", err)
	}
	switch {
	case c.Database.PoolSize != 8:
		t.Errorf("pool size %d, want 8", c.Database.PoolSize)
	case c.HTTPAddr != ":9000":
		t.Errorf("http addr %q", c.HTTPAddr)
	case !c.Identity.RequireEmailVerification:
		t.Error("email verification should be on")
	case c.Orchestration.Concurrency != 2 || c.Orchestration.PollInterval != 250*time.Millisecond:
		t.Errorf("orchestration %+v", c.Orchestration)
	case c.Dashboard.Dir != "/srv/dashboard":
		t.Errorf("dashboard dir %q", c.Dashboard.Dir)
	}
}

func TestFromSource_emptyValuesKeepDefaults(t *testing.T) {
	// An unset variable expands to "", which must read as "not set".
	c, err := FromSource(mapGetter{"database.url": testDSN, "http_addr": "  ", "identity.app_id": ""})
	if err != nil {
		t.Fatalf("FromSource: %v", err)
	}
	if c.HTTPAddr != Defaults().HTTPAddr || c.Identity.AppID != Defaults().Identity.AppID {
		t.Fatalf("empty values replaced defaults: %+v", c)
	}
}

func TestFromSource_rejects(t *testing.T) {
	tests := []struct {
		name string
		src  mapGetter
		want string
	}{
		{"missing database", mapGetter{}, "database.url is required"},
		{"bad environment", mapGetter{"database.url": testDSN, "environment": "prod"}, "environment must be"},
		{"short secret", mapGetter{"database.url": testDSN, "secret_key": "c2hvcnQ="}, "secret_key must be 32 bytes"},
		{"bad pool size", mapGetter{"database.url": testDSN, "database.pool_size": "many"}, "database.pool_size must be a whole number"},
		{"bad duration", mapGetter{"database.url": testDSN, "orchestration.poll_interval": "soon"}, "orchestration.poll_interval must be a duration"},
		{"bad bool", mapGetter{"database.url": testDSN, "identity.require_email_verification": "maybe"}, "must be true or false"},
		{"bad base path", mapGetter{"database.url": testDSN, "identity.base_path": "authsome"}, "identity.base_path"},
		{"bad app id", mapGetter{"database.url": testDSN, "identity.app_id": "app_1"}, "identity.app_id"},
		{"relative public url", mapGetter{"database.url": testDSN, "public_url": "localhost"}, "public_url must be an absolute URL"},
		{"production with dev secret", mapGetter{"database.url": testDSN, "environment": "production", "public_url": "https://lp.example.com"}, "development secret_key"},
		{"production over http", mapGetter{
			"database.url": testDSN, "environment": "production", "public_url": "http://lp.example.com",
			"secret_key": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		}, "https public_url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FromSource(tt.src)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestLoad_readsFilesAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("config.yaml", "http_addr: \"${LOOMPLANE_HTTP_ADDR:-}\"\ndatabase:\n  url: \"${LOOMPLANE_DATABASE_URL:-}\"\n")
	write("config.local.yaml", "database:\n  pool_size: 3\n")
	t.Setenv("LOOMPLANE_DATABASE_URL", testDSN)
	t.Setenv("LOOMPLANE_HTTP_ADDR", ":7001")

	c, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Database.URL != testDSN || c.HTTPAddr != ":7001" || c.Database.PoolSize != 3 {
		t.Fatalf("got %+v", c)
	}
}

func TestLoad_requiresConfigFile(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("Load succeeded without config.yaml")
	}
}

func TestFromSource_pausedTargetsAsAListOrAString(t *testing.T) {
	for _, v := range []any{"render, cloud-run", []any{"render", "cloud-run"}} {
		c, err := FromSource(mapGetter{"database.url": testDSN, "providers.paused": v})
		if err != nil {
			t.Fatalf("FromSource: %v", err)
		}
		if len(c.Providers.Paused) != 2 || c.Providers.Paused[0] != "render" || c.Providers.Paused[1] != "cloud-run" {
			t.Fatalf("paused from %#v: %q", v, c.Providers.Paused)
		}
	}
	if _, err := FromSource(mapGetter{"database.url": testDSN, "providers.paused": 3}); err == nil {
		t.Fatal("a number named targets")
	}
}
