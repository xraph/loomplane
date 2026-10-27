package fabriqloom_test

import (
	"testing"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/fabriqloom"
	"github.com/xraph/loomplane/internal/recipes"
)

func TestSearchHooksRenderProbeRole(t *testing.T) {
	kind, ok := recipes.Default().Kind("elasticsearch")
	if !ok {
		t.Fatal("Elasticsearch is not registered")
	}
	c := kind.Components[0]
	var hooks deployments.CatalogHooks = fabriqloom.SearchHooks{}
	files, err := hooks.Files(t.Context(), deployments.ComponentRun{
		Kind: "elasticsearch", Component: recipes.ResolvedComponent{Component: c},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := c.Files[0].Path
	if path != "/usr/share/elasticsearch/config/roles.yml" {
		t.Fatalf("declared path %q", path)
	}
	// An anonymous request can read the root document (cluster name and
	// version) and nothing else, so it learns no index names.
	const want = "loomplane_probe:\n  cluster: [\"cluster:monitor/main\"]\n"
	if len(files) != 1 || string(files[path]) != want {
		t.Fatalf("got %q", files)
	}
	if err := hooks.Ready(t.Context(), deployments.ComponentRun{}); err != nil {
		t.Fatal(err)
	}
}
