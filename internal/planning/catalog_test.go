package planning

import (
	"context"
	"testing"

	"github.com/xraph/loomplane/internal/manifests"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/providers"
)

func TestCatalogPostgresPinsRuntimeAndTarget(t *testing.T) {
	doc := parse(t, "version: 1\nresources:\n  database:\n    kind: postgres\n    provider: server\n    version: '17'\n    target: "+targetA+"\n")
	connections := infrastructureConnections(t)
	connections[0].Details["architecture"] = "arm64"
	connections[0].Details["docker_id"] = "reviewed-daemon"
	result := Compute(Input{Environment: "production", Graph: &graph.Graph{}, Manifest: doc, Providers: providers.Registry(), Connections: connections})
	if !result.Executable || len(result.Actions) != 1 {
		t.Fatalf("catalog plan: %+v", result)
	}
	cfg := result.Actions[0].Resource
	if cfg.Catalog == nil || cfg.Catalog.Image == "" || cfg.Target != targetA || cfg.TargetSettings["docker_id"] != "reviewed-daemon" {
		t.Fatalf("unpinned catalog: %+v", cfg)
	}
}

func TestCatalogPostgresRefusesUnreviewedMove(t *testing.T) {
	doc := parse(t, "version: 1\nresources:\n  database: {kind: postgres, provider: server, target: "+targetB+"}\n")
	result := Compute(Input{Environment: "production", Graph: &graph.Graph{}, Manifest: doc, Providers: providers.Registry(), Connections: infrastructureConnections(t), Bindings: []*Binding{{ID: "binding", Name: "database", Subject: ResourceSubject, Provider: "server", ConnectionID: targetA, ExternalID: "instance", Config: []byte(`{"kind":"postgres","provider":"server","target":"` + targetA + `"}`)}}})
	if result.Executable {
		t.Fatal("moving a persistent database silently created an empty replacement")
	}
}

type catalogStore struct {
	Store
	bindings []*Binding
}

func (s catalogStore) ListBindings(context.Context, string, string) ([]*Binding, error) {
	return s.bindings, nil
}
func (catalogStore) EnvironmentConnections(context.Context, string, string) (map[string]string, error) {
	return nil, nil
}

type catalogManifest struct {
	Manifests
	revision *manifests.Revision
}

func (m catalogManifest) Get(context.Context, string, string) (*manifests.Revision, error) {
	return m.revision, nil
}

func TestCatalogPlanWithoutRepositoriesCannotRemoveSourceServices(t *testing.T) {
	manifest := parse(t, "version: 1\nresources:\n  database: {kind: postgres, provider: server, target: "+targetA+"}\n")
	service := NewService(Deps{Store: catalogStore{}, Manifests: catalogManifest{revision: &manifests.Revision{Manifest: manifest}}, Providers: providers.Registry()})
	plan := &Plan{Catalog: true, OrgID: "org", ProjectID: "project", EnvironmentID: "env", Environment: "production"}
	if _, _, err := service.compute(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	service.store = catalogStore{bindings: []*Binding{{Subject: ServiceSubject, Name: "existing-api"}}}
	if _, _, err := service.compute(t.Context(), plan); err == nil {
		t.Fatal("catalog plan could remove a repository service")
	}
	plan.Catalog = false
	if _, _, err := service.compute(t.Context(), plan); err == nil {
		t.Fatal("legacy unpinned plan accepted")
	}
}
