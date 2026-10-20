package deployments

import (
	"context"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

func TestPostgresRecipeKeepsPasswordOutOfPersistentSpec(t *testing.T) {
	recipe, err := recipes.Postgres("17", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	resource := &planning.ResourceConfig{Provider: "server", Kind: "postgres", Version: "17", Target: "target", TargetSettings: map[string]string{"architecture": "arm64"}, Catalog: recipe}
	cfg, err := databaseService(resource)
	if err != nil {
		t.Fatal(err)
	}
	job := &Job{Deployment: &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, Connection: &connections.Connection{ID: "target"}, Action: planning.Action{Name: "database", Service: cfg}, Env: map[string]string{"POSTGRES_PASSWORD": "must-stay-in-vault", "POSTGRES_DB": "database"}, Secrets: map[string]bool{"POSTGRES_PASSWORD": true}}
	spec, labels := ownedSpec(job, recipe.Image, ownedState{RuntimeRef: "reference"})
	if spec.Env["POSTGRES_PASSWORD"] != "" || len(spec.Volumes) != 1 || spec.Volumes[0].Type != "persistent" || labels[server.LabelDatabase] != "database" {
		t.Fatalf("unsafe database spec: %+v %+v", spec, labels)
	}
	recipe.Image = "unreviewed-image"
	if _, err = databaseService(resource); err == nil {
		t.Fatal("accepted a modified image pin")
	}
}

func TestPostgresDoesNotReplaceMissingCredentialForRetainedData(t *testing.T) {
	job := &Job{Binding: &planning.Binding{ExternalID: "existing"}, Recall: func(context.Context, string) ([]byte, error) { return nil, ErrNoCredential }, Keep: func(context.Context, string, []byte) error { t.Fatal("replaced retained password"); return nil }}
	if _, err := retainedDatabaseSecret(t.Context(), job, passwordField); err == nil {
		t.Fatal("accepted missing credential")
	}
}
