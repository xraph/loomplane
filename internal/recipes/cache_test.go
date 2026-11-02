package recipes

import "testing"

func TestCacheRecipesStayEngineAndTargetSpecific(t *testing.T) {
	for _, test := range []struct{ kind, version, product, format string }{
		{"redis", "8.2", "8.2.10", "redis-rdb-v1"},
		{"valkey", "8.1", "8.1.10", "valkey-rdb-v1"},
	} {
		for _, architecture := range []string{"amd64", "arm64"} {
			t.Run(test.kind+"/"+architecture, func(t *testing.T) {
				recipe, err := LookupForTarget(test.kind, test.version, architecture, "server")
				if err != nil {
					t.Fatal(err)
				}
				if recipe.Engine() != test.kind || recipe.ProductVersion != test.product || recipe.Port() != 6379 || recipe.MountPath != "/data" || recipe.BackupFormat != test.format {
					t.Fatalf("wrong cache contract: %+v", recipe)
				}
				// Kubernetes runs a cache only as a composition's dependency;
				// the planner refuses a standalone one.
				cluster, err := LookupForTarget(test.kind, test.version, architecture, "kubernetes")
				if err != nil || cluster.ID != recipe.ID+"-kubernetes" || cluster.VolumeSizeMB != 2048 {
					t.Fatalf("Kubernetes cache recipe: %+v %v", cluster, err)
				}
			})
		}
	}
}
