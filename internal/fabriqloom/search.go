// Package fabriqloom holds what the hosted Fabriq stack's catalog kinds do
// beyond their recipes.
package fabriqloom

import (
	"context"

	"github.com/xraph/loomplane/internal/deployments"
)

const (
	rolesPath = "/usr/share/elasticsearch/config/roles.yml"
	// probeRole lets an anonymous request read cluster health and nothing
	// else, so the unauthenticated readiness probe passes while every index
	// still needs the elastic password.
	probeRole = "loomplane_probe:\n  cluster: [\"cluster:monitor/health\"]\n"
)

// SearchHooks are the Elasticsearch kind's hooks: it renders the role the
// anonymous readiness probe runs as.
type SearchHooks struct{ deployments.NoHooks }

// Files implements deployments.CatalogHooks.
func (SearchHooks) Files(context.Context, deployments.ComponentRun) (map[string][]byte, error) {
	return map[string][]byte{rolesPath: []byte(probeRole)}, nil
}
