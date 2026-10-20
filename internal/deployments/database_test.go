package deployments

import (
	"testing"

	native "github.com/xraph/loomplane/internal/clouds/mysql"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

func TestSQLDatabaseSpecUsesReviewedEngineWithoutPersistentCredentials(t *testing.T) {
	for _, kind := range []string{"postgres", "mysql", "mariadb"} {
		t.Run(kind, func(t *testing.T) {
			recipe, err := recipes.Lookup(kind, recipes.DefaultVersion(kind), "arm64")
			if err != nil {
				t.Fatal(err)
			}
			resource := &planning.ResourceConfig{Kind: kind, Provider: "server", Version: recipes.DefaultVersion(kind), Target: "target", TargetSettings: map[string]string{"architecture": "arm64"}, Catalog: recipe}
			service, err := databaseService(resource)
			if err != nil {
				t.Fatal(err)
			}
			env, secrets := databaseEnvironment(kind, "database", "application-secret", "admin-secret")
			job := &Job{Deployment: &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, Connection: &connections.Connection{ID: "target"}, Action: planning.Action{Name: "database", Service: service}, Env: env, Secrets: secrets}
			spec, labels := ownedSpec(job, recipe.Image, ownedState{})
			for key := range secrets {
				if spec.Env[key] != "" {
					t.Fatalf("secret %s entered persisted spec", key)
				}
			}
			if len(spec.Ports) != 1 || spec.Ports[0].Container != recipe.Port() || len(spec.Volumes) != 1 || spec.Volumes[0].MountPath != recipe.MountPath {
				t.Fatalf("wrong native storage/port: %+v", spec)
			}
			if kind == "postgres" {
				if labels[server.LabelDatabase] != "database" {
					t.Fatal("lost PostgreSQL label")
				}
			} else if labels[native.LabelDatabase] != "database" || labels[native.LabelEngine] != kind || labels[server.LabelDatabase] != "" {
				t.Fatalf("wrong database identity: %+v", labels)
			}
			resource.Kind = "different-engine"
			if _, err := databaseService(resource); err == nil {
				t.Fatal("recipe accepted for another engine")
			}
		})
	}
}
