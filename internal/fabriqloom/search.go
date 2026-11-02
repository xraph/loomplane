// Package fabriqloom holds what the hosted Fabriq stack's catalog kinds do
// beyond their recipes.
package fabriqloom

import (
	"context"

	"github.com/xraph/loomplane/internal/deployments"
)

const (
	rolesPath = "/usr/share/elasticsearch/config/roles.yml"
	// probeRole lets an anonymous request read the root document (cluster
	// name and version) and nothing else. cluster:monitor/health is not safe
	// here: with it, GET /_cluster/health?level=indices lists every index
	// name and /_cluster/health/<index> answers per index, anonymously, which
	// leaks tenant-named Fabriq indices. The root document names no index.
	probeRole = "loomplane_probe:\n  cluster: [\"cluster:monitor/main\"]\n"
)

// SearchHooks are the Elasticsearch kind's hooks: it renders the role the
// anonymous readiness probe runs as.
type SearchHooks struct{ deployments.NoHooks }

// Files implements deployments.CatalogHooks.
func (SearchHooks) Files(context.Context, deployments.ComponentRun) (map[string][]byte, error) {
	return map[string][]byte{rolesPath: []byte(probeRole)}, nil
}
