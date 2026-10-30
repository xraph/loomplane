//go:build integration

package integration

import (
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/repositories"
)

func resolves[T any](t *testing.T, c forge.Container, name string) {
	t.Helper()
	v, err := forge.InjectType[T](c)
	if err != nil {
		t.Errorf("%s is not provided: %v", name, err)
		return
	}
	if any(v) == nil {
		t.Errorf("%s resolved to nil", name)
	}
}

// The AI extension resolves Loomplane's services from the container, so each
// one has to be there with AI off too: providing a value is inert.
func TestAgentDependenciesAreProvided(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Dashboard.Dir = t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := r.app.(forge.App).Container()
	resolves[*planning.Service](t, c, "planning")
	resolves[*deployments.Service](t, c, "deployments")
	resolves[*manifests.Service](t, c, "manifests")
	resolves[*discovery.Service](t, c, "discovery")
	resolves[*projects.Service](t, c, "projects")
	resolves[*environments.Service](t, c, "environments")
	resolves[*connections.Service](t, c, "connections")
	resolves[*repositories.Connections](t, c, "repository connections")
	resolves[*realtime.PgStore](t, c, "realtime store")
	resolves[realtime.WakeSource](t, c, "realtime wake source")
}
