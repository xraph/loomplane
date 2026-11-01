package fabriqloom_test

import (
	"testing"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/fabriqloom"
	"github.com/xraph/loomplane/internal/recipes"
)

func TestSearchHooksRenderProbeRole(t *testing.T) {
	// SearchHooks serve the anonymous-probe revision; later revisions use
	// authenticated engine hooks.
	_, composition, err := recipes.Default().ResolveRevision("elasticsearch", "9.4", "2", "amd64", "server")
	if err != nil {
		t.Fatal(err)
	}
	c := composition.Components[0]
	var hooks deployments.CatalogHooks = fabriqloom.SearchHooks{}
	files, err := hooks.Files(t.Context(), deployments.ComponentRun{
		Kind: "elasticsearch", Component: c,
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
