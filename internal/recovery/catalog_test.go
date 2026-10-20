package recovery

import (
	"testing"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

func TestRecoveryProfilesRequireMatchingEngineAndSupportedTarget(t *testing.T) {
	for _, kind := range []string{"postgres", "mysql", "mariadb", "redis", "valkey"} {
		recipe, err := recipes.Lookup(kind, recipes.DefaultVersion(kind), "arm64")
		if err != nil {
			t.Fatal(err)
		}
		cfg := planning.ResourceConfig{Provider: "server", Kind: kind, Catalog: recipe}
		if !supportsRecovery(&cfg) {
			t.Fatalf("qualified Docker profile refused: %s", kind)
		}
		cfg.Provider = "kubernetes"
		if supportsRecovery(&cfg) != (kind == "postgres") {
			t.Fatalf("unqualified Kubernetes profile accepted: %s", kind)
		}
		cfg.Provider = "server"
		cfg.Kind = "unreviewed-engine"
		if supportsRecovery(&cfg) {
			t.Fatal("recipe accepted for a different engine")
		}
	}
}
