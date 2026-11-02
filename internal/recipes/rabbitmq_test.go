package recipes_test

import (
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
)

func TestRabbitMQProfile(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		recipe, err := recipes.LookupForTarget("rabbitmq", "4.3", arch, "server")
		if err != nil {
			t.Fatal(err)
		}
		if recipe.Engine() != "rabbitmq" || recipe.Port() != 5672 || recipe.BackupFormat != "rabbitmq-node-v1" || recipe.MountPath != "/var/lib/rabbitmq" || recipes.EnvKey("rabbitmq") != "AMQP_URL" {
			t.Fatalf("wrong native contract: %+v", recipe)
		}
	}
	if _, err := recipes.LookupForTarget("rabbitmq", "4.3", "arm64", "kubernetes"); err == nil {
		t.Fatal("unqualified Kubernetes recipe")
	}
}
