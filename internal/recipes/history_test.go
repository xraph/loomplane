package recipes

import "testing"

func TestHistoricalRecipesRemainExactAndCannotReplaceCurrent(t *testing.T) {
	old := legacyFalkorDB()
	current := old.clone()
	current.Releases[0].Revision = "100"
	current.Components[0].Readiness = &HTTPProbe{Port: "redis", Checker: "falkordb-v1"}
	registry := NewRegistry()
	if err := registry.Register(current); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterHistorical(old); err != nil {
		t.Fatal(err)
	}
	historical, comp, err := registry.ResolveRevision("falkordb", "4.2", old.Releases[0].Revision, "arm64", "server")
	if err != nil || comp.Revision != old.Releases[0].Revision || historical.Components[0].Readiness.Checker != "" {
		t.Fatalf("historical recipe: %+v %v", comp, err)
	}
	latest, err := registry.Resolve("falkordb", "4.2", "arm64", "server")
	if err != nil || latest.Revision != "100" {
		t.Fatalf("latest recipe: %+v %v", latest, err)
	}
	historical.Components[0].Readiness.Path = "changed"
	_, comp, err = registry.ResolveRevision("falkordb", "4.2", old.Releases[0].Revision, "arm64", "server")
	if err != nil || comp.Components[0].Readiness.Path != "" {
		t.Fatal("historical recipe shares mutable storage")
	}
	if _, _, err = registry.ResolveRevision("falkordb", "4.2", "unknown", "arm64", "server"); err == nil {
		t.Fatal("accepted unknown revision")
	}
	if err = registry.RegisterHistorical(old); err == nil {
		t.Fatal("replaced historical revision")
	}
	if err = registry.RegisterHistorical(current); err == nil {
		t.Fatal("replaced current revision")
	}
	invalid := old.clone()
	invalid.Releases[0].Revision = "999"
	invalid.Components[0].Readiness = nil
	if err = registry.RegisterHistorical(invalid); err == nil {
		t.Fatal("registered invalid history")
	}
}
