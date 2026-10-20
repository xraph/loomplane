package deployments

import (
	"net/url"
	"testing"

	"github.com/xraph/ctrlplane/id"

	native "github.com/xraph/loomplane/internal/clouds/redis"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

func TestCacheDeploymentKeepsSecretsAndNativeIdentity(t *testing.T) {
	for _, kind := range []string{"redis", "valkey"} {
		t.Run(kind, func(t *testing.T) {
			recipe, err := recipes.Lookup(kind, recipes.DefaultVersion(kind), "arm64")
			if err != nil {
				t.Fatal(err)
			}
			resource := &planning.ResourceConfig{Kind: kind, Provider: "server", Version: recipes.DefaultVersion(kind), Target: "selected", TargetSettings: map[string]string{"architecture": "arm64"}, Catalog: recipe}
			service, err := databaseService(resource)
			if err != nil {
				t.Fatal(err)
			}
			env, secrets := databaseEnvironment(kind, "database", "application-secret", "admin-secret")
			if env["LOOMPLANE_CACHE_ADMIN_PASSWORD"] != "admin-secret" || !secrets["LOOMPLANE_CACHE_ADMIN_PASSWORD"] || env["LOOMPLANE_APP_PASSWORD"] != "application-secret" {
				t.Fatal("lost cache credentials")
			}
			job := &Job{Deployment: &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, Connection: &connections.Connection{ID: "selected"}, Action: planning.Action{Name: "database", Service: service}, Env: env, Secrets: secrets}
			spec, labels := ownedSpec(job, recipe.Image, ownedState{})
			if labels[native.LabelEngine] != kind || spec.Ports[0].Container != 6379 || spec.Volumes[0].MountPath != "/data" {
				t.Fatal("cache lost runtime identity")
			}
			for key := range secrets {
				if spec.Env[key] != "" {
					t.Fatal("secret persisted in instance spec")
				}
			}
			address, err := databaseAddress(resource, id.New(id.PrefixInstance), "database", "application-secret")
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := url.Parse(address)
			if err != nil || parsed.Scheme != "redis" || parsed.Path != "/0" || parsed.User.Username() != "app" {
				t.Fatalf("wrong RESP address: %s %v", address, err)
			}
		})
	}
}
