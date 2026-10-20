package recipes

import (
	"strings"
	"testing"
)

func TestCatalogPinsEachEngineAndArchitecture(t *testing.T) {
	for _, test := range []struct{ kind, version, format string }{{"postgres", "17", "pg-dump-custom-v1"}, {"mysql", "8.4", "mysqldump-sql-v1"}, {"mariadb", "11.8", "mariadb-dump-sql-v1"}} {
		for _, architecture := range []string{"amd64", "arm64"} {
			t.Run(test.kind+"/"+architecture, func(t *testing.T) {
				recipe, err := Lookup(test.kind, test.version, architecture)
				if err != nil {
					t.Fatal(err)
				}
				if recipe.Architecture != architecture || !strings.Contains(recipe.Image, "@sha256:") || recipe.BackupFormat != test.format || recipe.Engine() != test.kind {
					t.Fatalf("unqualified recipe: %+v", recipe)
				}
				if recipe.Port() == 0 {
					t.Fatal("missing native database port")
				}
			})
		}
	}
}

func TestCatalogRejectsUnsupportedVersionsAndTargets(t *testing.T) {
	for _, test := range []struct{ kind, version, architecture string }{{"mysql", "8.0", "arm64"}, {"mariadb", "10.11", "amd64"}, {"mysql", "8.4", "unknown"}, {"redis", "7", "arm64"}, {"postgres", "8.4", "arm64"}} {
		if _, err := Lookup(test.kind, test.version, test.architecture); err == nil {
			t.Fatalf("unsupported recipe accepted: %+v", test)
		}
	}
}

func TestMySQLRecoveryDisablesTimersWithoutBroaderApplicationGrants(t *testing.T) {
	for _, kind := range []string{"mysql", "mariadb"} {
		recipe, err := Lookup(kind, DefaultVersion(kind), "arm64")
		if err != nil {
			t.Fatal(err)
		}
		live, recovery := strings.Join(recipe.RuntimeArgs(false), " "), strings.Join(recipe.RuntimeArgs(true), " ")
		scheduler := "DISABLED"
		if kind == "mariadb" {
			scheduler = "OFF"
		}
		if !strings.Contains(live, "--skip-log-bin") || strings.Contains(live, "event-scheduler") || !strings.Contains(recovery, "--event-scheduler="+scheduler) {
			t.Fatalf("unsafe native profile arguments: %s, %s", live, recovery)
		}
	}
}
