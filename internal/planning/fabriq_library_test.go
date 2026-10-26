package planning

import (
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
)

func TestLibraryConsumerGetsEngineVariables(t *testing.T) {
	r := composedPlan(t, recipes.Default(), "version: 1\nresources:\n  lib: {kind: fabriq-library, provider: server, target: "+targetA+", uses: {falkordb: {enabled: true}, elasticsearch: {enabled: false}}}\nservices:\n  web: {source: cmd/api, provider: server, target: "+targetA+", public: false, needs: [lib]}\n")
	if !r.Executable {
		t.Fatalf("library plan blocked: %+v", r.Problems)
	}
	web := findAction(r, "service:web")
	if web == nil {
		t.Fatal("no app action")
	}
	keys := make([]string, 0, len(web.Service.Env))
	for _, link := range web.Service.Env {
		keys = append(keys, link.Key)
	}
	slices.Sort(keys)
	want := []string{"FABRIQ_FALKORDB_ADDR", "FABRIQ_FALKORDB_PASSWORD", "FABRIQ_FALKORDB_USERNAME", "FABRIQ_POSTGRES_DSN", "FABRIQ_REDIS_ADDR", "FABRIQ_REDIS_PASSWORD", "FABRIQ_REDIS_USERNAME"}
	if !slices.Equal(keys, want) {
		t.Fatalf("got %v want %v", keys, want)
	}
	if findAction(r, "resource:lib--admin") != nil || findAction(r, "resource:lib--worker") == nil {
		t.Fatal("wrong library processes")
	}
}
