package planning

import (
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/config"
)

func TestLoomplaneServicesNameItsOwnAddresses(t *testing.T) {
	cfg := config.Config{HTTPAddr: ":8780", Database: config.DatabaseConfig{URL: "postgres://u:p@db.internal:6543/app?sslmode=disable"}}
	cfg.Providers.Plugins = []config.PluginConfig{{URL: "http://fly-provider:9000"}, {URL: "https://render-provider"}}
	want := []string{"db.internal:6543", ":8780", "fly-provider:9000", "render-provider:443"}
	if got := loomplaneServices(cfg); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for dsn, want := range map[string][]string{
		"host=db1 port=5433 user=u dbname=app":         {"db1:5433", ":8780"},
		"postgres://u:p@db1,db2:5434/app":              {"db1:5432", "db2:5434", ":8780"},
		"postgres://u:p@/app?host=/var/run/postgresql": {":8780"},
		"postgres://u:p@localhost/app":                 {"localhost:5432", ":8780"},
	} {
		cfg := config.Config{HTTPAddr: ":8780", Database: config.DatabaseConfig{URL: dsn}}
		if got := loomplaneServices(cfg); !slices.Equal(got, want) {
			t.Fatalf("%s: got %q, want %q", dsn, got, want)
		}
	}
}
