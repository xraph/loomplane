package recovery

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

func TestDuckReadonlyRecoveryBindsReviewedRevisionAndImage(t *testing.T) {
	registry := recipes.NewRegistry()
	kind := recipes.DuckDBKind(recipes.Image{Repository: "qualified.example/duckdb", Digests: map[string]string{"arm64": "sha256:" + strings.Repeat("a", 64), "amd64": "sha256:" + strings.Repeat("b", 64)}})
	if err := registry.Register(kind); err != nil {
		t.Fatal(err)
	}
	composition, err := registry.Resolve("duckdb", "2.0", "arm64", "server")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &planning.ResourceConfig{Kind: "duckdb", Version: "2.0", Provider: "server", Target: "target", TargetSettings: map[string]string{"architecture": "arm64"}, Component: &planning.ComponentConfig{Kind: "duckdb", Resource: "data", Revision: composition.Revision, Component: composition.Components[0], Protection: recipes.ProtectionNative}}
	descriptor, err := Describe(cfg, registry)
	if err != nil || descriptor.Revision != "3" || descriptor.Image != composition.Components[0].Image {
		t.Fatalf("reviewed recovery rejected: %+v %v", descriptor, err)
	}
	cfg.Component.Component.Image = "unreviewed.example/duckdb@sha256:" + strings.Repeat("c", 64)
	if _, describeErr := Describe(cfg, registry); describeErr == nil {
		t.Fatal("caller image replaced the registry image")
	}
	old := kind
	old.Releases = append([]recipes.Release(nil), kind.Releases...)
	old.Releases[0].Revision = "2"
	if historyErr := registry.RegisterBlockedHistorical(old, "DuckDB revision 2 requires recreation before recovery can start"); historyErr != nil {
		t.Fatal(historyErr)
	}
	_, historical, err := registry.ResolveRevision("duckdb", "2.0", "2", "arm64", "server")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Component.Revision = "2"
	cfg.Component.Component = historical.Components[0]
	if _, err := Describe(cfg, registry); err == nil {
		t.Fatal("blocked historical recovery was replayed")
	}
}
