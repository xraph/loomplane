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
		"identity.session_cookie":             "loomplane_staging",
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
	case c.Identity.SessionCookie != "loomplane_staging":
		t.Errorf("session cookie %q", c.Identity.SessionCookie)
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

func boolPtr(b bool) *bool { return &b }

func TestFromSource_waitlist(t *testing.T) {
	tests := []struct {
		name string
		src  mapGetter
		want *bool
	}{
		{"unset", mapGetter{"database.url": testDSN}, nil},
		{"empty", mapGetter{"database.url": testDSN, "identity.waitlist": ""}, nil},
		{"true", mapGetter{"database.url": testDSN, "identity.waitlist": "true"}, boolPtr(true)},
		{"off", mapGetter{"database.url": testDSN, "identity.waitlist": "off"}, boolPtr(false)},
		{"native bool", mapGetter{"database.url": testDSN, "identity.waitlist": false}, boolPtr(false)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := FromSource(tt.src)
			if err != nil {
				t.Fatal(err)
			}
			got := c.Identity.Waitlist
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Fatalf("waitlist = %v, want %v", got, tt.want)
			}
		})
	}
	_, err := FromSource(mapGetter{"database.url": testDSN, "identity.waitlist": "maybe"})
	if err == nil || !strings.Contains(err.Error(), "identity.waitlist") {
		t.Fatalf("error %v, want it to mention identity.waitlist", err)
	}
}

func TestFromSource_consolePath(t *testing.T) {
	if got := Defaults().Console.Path; got != "/__console" {
		t.Fatalf("default console path = %q", got)
	}
	for _, p := range []string{"/api/x", "/", "/__a/b", "__console", "/console"} {
		_, err := FromSource(mapGetter{"database.url": testDSN, "console.path": p})
		if err == nil || !strings.Contains(err.Error(), "console.path") {
			t.Errorf("console.path %q: error %v, want it to mention console.path", p, err)
		}
	}
	c, err := FromSource(mapGetter{"database.url": testDSN, "console.path": "/__ops-7f3"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Console.Path != "/__ops-7f3" {
		t.Fatalf("console path = %q", c.Console.Path)
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
		{"bad session cookie", mapGetter{"database.url": testDSN, "identity.session_cookie": "loomplane session"}, "identity.session_cookie"},
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

// The repository's config.yaml takes the pool size from the environment, so
// the Kubernetes chart can set it without a config file.
func TestLoad_poolSizeFromEnvironment(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, env string
		want      int
	}{
		{"default", "", 16},
		{"set", "40", 40},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("LOOMPLANE_DATABASE_URL", testDSN)
			t.Setenv("LOOMPLANE_DATABASE_POOL_SIZE", tt.env)
			c, err := Load(dir)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.Database.PoolSize != tt.want {
				t.Fatalf("pool size %d, want %d", c.Database.PoolSize, tt.want)
			}
		})
	}
}

func TestLoad_analysisConcurrencyFromEnvironment(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, env string
		want      int
	}{
		{"default", "", 2},
		{"set", "1", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("LOOMPLANE_DATABASE_URL", testDSN)
			t.Setenv("LOOMPLANE_ANALYSIS_CONCURRENCY", tt.env)
			c, err := Load(dir)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.Analysis.Concurrency != tt.want {
				t.Fatalf("analysis concurrency %d, want %d", c.Analysis.Concurrency, tt.want)
			}
		})
	}
}

func TestLoad_analysisConcurrencyZeroFailsValidation(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "config.yaml"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOOMPLANE_DATABASE_URL", testDSN)
	t.Setenv("LOOMPLANE_ANALYSIS_CONCURRENCY", "0")
	_, err = Load(dir)
	if err == nil || !strings.Contains(err.Error(), "analysis.concurrency must be at least 1") {
		t.Fatalf("Load error %v, want analysis.concurrency must be at least 1", err)
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

func TestFromSource_fallsBackToThePlatformsDatabaseURL(t *testing.T) {
	// Heroku, Render, DigitalOcean and Miget hand an app DATABASE_URL.
	t.Setenv("DATABASE_URL", "postgres://doadmin:pw@db.example.com:25060/defaultdb?sslmode=require")
	c, err := FromSource(mapGetter{"database.url": ""})
	if err != nil || c.Database.URL != "postgres://doadmin:pw@db.example.com:25060/defaultdb?sslmode=require" {
		t.Fatalf("got %q, %v", c.Database.URL, err)
	}
	// LOOMPLANE_DATABASE_URL, through database.url, still wins.
	if c, err = FromSource(mapGetter{"database.url": testDSN}); err != nil || c.Database.URL != testDSN {
		t.Fatalf("got %q, %v", c.Database.URL, err)
	}
}

func TestFromSource_dbGatewaySigningKeyFile(t *testing.T) {
	c, err := FromSource(mapGetter{"database.url": testDSN, "dbgateway.signing_key_file": "/run/secrets/dbgateway.json"})
	if err != nil || c.DBGateway.SigningKeyFile != "/run/secrets/dbgateway.json" {
		t.Fatalf("signing key file %q: %v", c.DBGateway.SigningKeyFile, err)
	}
	if c, err = FromSource(mapGetter{"database.url": testDSN}); err != nil || c.DBGateway.SigningKeyFile != "" {
		t.Fatalf("unset signing key file %q: %v", c.DBGateway.SigningKeyFile, err)
	}
	for _, path := range []string{"keys/dbgateway.json", "/run/secrets/../dbgateway.json"} {
		_, err := FromSource(mapGetter{"database.url": testDSN, "dbgateway.signing_key_file": path})
		if err == nil || !strings.Contains(err.Error(), "dbgateway.signing_key_file") {
			t.Fatalf("%q: error %v, want it to name dbgateway.signing_key_file", path, err)
		}
	}
}

func TestFromSource_endpointDeniedCIDRs(t *testing.T) {
	c, err := FromSource(mapGetter{"database.url": testDSN})
	if err != nil {
		t.Fatalf("FromSource: %v", err)
	}
	if len(c.Planning.EndpointDeniedCIDRs) != 0 {
		t.Fatalf("denied by default: %v", c.Planning.EndpointDeniedCIDRs)
	}
	for _, v := range []any{"10.9.0.0/16, 192.0.2.7", []any{"10.9.0.0/16", "192.0.2.7"}} {
		c, err := FromSource(mapGetter{"database.url": testDSN, "planning.endpoint_denied_cidrs": v})
		if err != nil {
			t.Fatalf("FromSource: %v", err)
		}
		got := c.Planning.EndpointDeniedCIDRs
		if len(got) != 2 || got[0].String() != "10.9.0.0/16" || got[1].String() != "192.0.2.7/32" {
			t.Fatalf("denied from %#v: %v", v, got)
		}
	}
	for _, v := range []any{"10.9.0.0/33", "db.internal", 3} {
		if _, err := FromSource(mapGetter{"database.url": testDSN, "planning.endpoint_denied_cidrs": v}); err == nil || !strings.Contains(err.Error(), "planning.endpoint_denied_cidrs") {
			t.Fatalf("%#v: %v", v, err)
		}
	}
}
