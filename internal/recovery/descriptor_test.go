package recovery

import (
	"testing"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

func TestComponentNativeRecoveryUsesRecordedRecipe(t *testing.T) {
	for _, name := range []string{"falkordb", "elasticsearch", "opensearch"} {
		t.Run(name, func(t *testing.T) {
			composition, err := recipes.Default().Resolve(name, "", "arm64", "server")
			if err != nil {
				t.Fatal(err)
			}
			cfg := &planning.ResourceConfig{Kind: name, Version: composition.Version, Provider: "server", Target: "target", TargetSettings: map[string]string{"architecture": "arm64"}, Component: &planning.ComponentConfig{Kind: name, Resource: "data", Revision: composition.Revision, Component: composition.Components[0], Protection: recipes.ProtectionNative}}
			if _, err = Describe(cfg, nil); err != nil {
				t.Fatal(err)
			}
			if Protection(cfg) != recipes.ProtectionNative {
				t.Fatal("Docker native recovery missing")
			}
			cfg.Provider = "kubernetes"
			if Protection(cfg) != "" {
				t.Fatal("Kubernetes recovery advertised")
			}
			cfg.Provider = "server"
			cfg.Component.Component.Image += "tampered"
			if _, err = Describe(cfg, nil); err == nil {
				t.Fatal("changed recipe accepted")
			}
		})
	}
	for name, revisions := range map[string][]string{"falkordb": {"1", "2"}, "elasticsearch": {"2", "3"}, "opensearch": {"1"}} {
		for _, revision := range revisions {
			version := map[string]string{"falkordb": "4.2", "elasticsearch": "9.4", "opensearch": "3.9"}[name]
			_, old, err := recipes.Default().ResolveRevision(name, version, revision, "arm64", "server")
			if err != nil {
				t.Fatal(err)
			}
			if old.Components[0].Protection != recipes.ProtectionRebuildable {
				t.Fatalf("changed historical protection %s/%s", name, revision)
			}
		}
	}
}
