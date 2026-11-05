package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHallproofOffByDefault(t *testing.T) {
	c, err := FromSource(mapGetter{"database.url": testDSN})
	if err != nil {
		t.Fatal(err)
	}
	if c.Hallproof != (HallproofConfig{}) || c.Hallproof.Enabled() {
		t.Fatalf("hallproof %+v, want off with nothing set", c.Hallproof)
	}
}

func TestHallproofFromConfigYAML(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "ingest-key")
	for _, tt := range []struct {
		name                   string
		endpoint, key, keyFile string
		want                   HallproofConfig
	}{
		{"unset", "", "", "", HallproofConfig{}},
		{"key", "https://hallproof.example.com", "hpk_live", "", HallproofConfig{Endpoint: "https://hallproof.example.com", IngestKey: "hpk_live"}},
		{"key file", "https://hallproof.example.com", "", keyFile, HallproofConfig{Endpoint: "https://hallproof.example.com", IngestKeyFile: keyFile}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("LOOMPLANE_DATABASE_URL", testDSN)
			t.Setenv("LOOMPLANE_HALLPROOF_ENDPOINT", tt.endpoint)
			t.Setenv("LOOMPLANE_HALLPROOF_INGEST_KEY", tt.key)
			t.Setenv("LOOMPLANE_HALLPROOF_INGEST_KEY_FILE", tt.keyFile)
			c, err := Load(dir)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.Hallproof != tt.want {
				t.Fatalf("hallproof %+v, want %+v", c.Hallproof, tt.want)
			}
		})
	}
}

func TestHallproofEndpoints(t *testing.T) {
	for _, tt := range []struct {
		endpoint string
		want     string // "" means accepted
	}{
		{"https://hallproof.example.com", ""},
		{"https://hallproof.example.com:8443/base", ""},
		{"http://localhost:8080", ""},
		{"http://127.0.0.1:55100", ""},
		{"http://[::1]:8080", ""},
		{"http://hallproof.example.com", "https"},
		{"http://10.0.0.5", "https"},
		{"ftp://hallproof.example.com", "https"},
		{"hallproof.example.com", "absolute"},
		{"https://user:pass@hallproof.example.com", "user info"},
		{"https://hallproof.example.com/?k=v", "query"},
		{"https://hallproof.example.com/#x", "fragment"},
	} {
		t.Run(tt.endpoint, func(t *testing.T) {
			_, err := FromSource(mapGetter{"database.url": testDSN, "hallproof.endpoint": tt.endpoint, "hallproof.ingest_key": "hpk_test"})
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("FromSource = %v, want it accepted", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("FromSource = %v, want an error mentioning %q", err, tt.want)
			}
		})
	}
}

func TestHallproofKeys(t *testing.T) {
	const endpoint = "https://hallproof.example.com"
	for _, tt := range []struct {
		name string
		src  mapGetter
		want string
	}{
		{"both", mapGetter{"hallproof.endpoint": endpoint, "hallproof.ingest_key": "hpk_test", "hallproof.ingest_key_file": "/run/secrets/k"}, "not both"},
		{"neither", mapGetter{"hallproof.endpoint": endpoint}, "needs hallproof.ingest_key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.src["database.url"] = testDSN
			_, err := FromSource(tt.src)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("FromSource = %v, want an error mentioning %q", err, tt.want)
			}
		})
	}
	// Off, the keys aren't used, so they aren't judged.
	if _, err := FromSource(mapGetter{"database.url": testDSN, "hallproof.ingest_key": "hpk_test", "hallproof.ingest_key_file": "/run/secrets/k"}); err != nil {
		t.Fatalf("off with both keys: %v", err)
	}
}

func TestHallproofHidesTheKey(t *testing.T) {
	h := HallproofConfig{Endpoint: "https://hallproof.example.com", IngestKey: "hpk_secret_canary"}
	for _, s := range []string{fmt.Sprint(h), fmt.Sprintf("%+v", h), fmt.Sprintf("%#v", h), fmt.Sprintf("%+v", Config{Hallproof: h})} {
		if strings.Contains(s, "hpk_secret_canary") {
			t.Fatalf("the ingest key is printed: %s", s)
		}
	}
}
