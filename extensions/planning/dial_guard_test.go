package planning

import (
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/config"
)

func TestLoomplaneServicesNameItsOwnAddresses(t *testing.T) {
	cfg := config.Config{HTTPAddr: ":8780", Database: config.DatabaseConfig{URL: "postgres://u:p@db.internal:6543/app?sslmode=disable"}}
	cfg.Worker.HTTPAddr = ":8781"
	cfg.Federation.Runtime.ListenAddress = "10.0.0.9:9443"
	cfg.Providers.Plugins = []config.PluginConfig{{URL: "http://fly-provider:9000"}, {URL: "https://render-provider"}}
	want := []string{"db.internal:6543", ":8780", ":8781", "10.0.0.9:9443", "fly-provider:9000", "render-provider:443"}
	if got, err := loomplaneServices(cfg); err != nil || !slices.Equal(got, want) {
		t.Fatalf("got %q (%v), want %q", got, err, want)
	}
	for dsn, want := range map[string][]string{
		"host=db1 port=5433 user=u dbname=app":         {"db1:5433", ":8780"},
		"postgres://u:p@db1,db2:5434/app":              {"db1:5432", "db2:5434", ":8780"},
		"postgres://u:p@/app?host=/var/run/postgresql": {":8780"},
		"postgres://u:p@localhost/app":                 {"localhost:5432", ":8780"},
	} {
		cfg := config.Config{HTTPAddr: ":8780", Database: config.DatabaseConfig{URL: dsn}}
		if got, err := loomplaneServices(cfg); err != nil || !slices.Equal(got, want) {
			t.Fatalf("%s: got %q (%v), want %q", dsn, got, err, want)
		}
	}
}

func TestLoomplaneServicesSayWhenTheDatabaseCantBeDenied(t *testing.T) {
	cfg := config.Config{HTTPAddr: ":8780", Database: config.DatabaseConfig{URL: "postgres://u:p@db.internal:notaport/app"}}
	got, err := loomplaneServices(cfg)
	if err == nil || !strings.Contains(err.Error(), "database") || strings.Contains(err.Error(), "u:p") {
		t.Fatalf("error: %v", err)
	}
	// The rest are still denied.
	if !slices.Equal(got, []string{":8780"}) {
		t.Fatalf("got %q", got)
	}
}
