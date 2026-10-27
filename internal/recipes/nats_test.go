package recipes_test

import (
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
)

func TestNATSJetStreamProfile(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		recipe, err := recipes.LookupForTarget("nats", "2.11", arch, "server")
		if err != nil {
			t.Fatal(err)
		}
		if recipe.Engine() != "nats" || recipe.Port() != 4222 || recipe.BackupFormat != "nats-jetstream-v1" || recipe.MountPath != "/data" || recipes.EnvKey("nats") != "NATS_URL" {
			t.Fatalf("wrong native contract: %+v", recipe)
		}
	}
	if _, err := recipes.LookupForTarget("nats", "2.11", "arm64", "kubernetes"); err == nil {
		t.Fatal("unqualified Kubernetes recipe")
	}
}
