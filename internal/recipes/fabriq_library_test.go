package recipes_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
)

func TestFabriqLibraryKind(t *testing.T) {
	c, err := recipes.Default().Resolve("fabriq-library", "", "arm64", "server")
	if err != nil {
		t.Fatal(err)
	}
	c = c.Without(map[string]bool{"falkordb": true, "elasticsearch": true})
	if !slices.Equal(componentNames(c), []string{"preflight", "migrate", "app-role", "worker"}) {
		t.Fatal(componentNames(c))
	}
	k, _ := recipes.Default().Kind("fabriq-library")
	if len(k.ConsumerOptions) != 0 || len(k.Secrets) != 1 || k.Secrets[0].Name != "app-password" {
		t.Fatalf("library exposes hosted credentials: %+v", k.Secrets)
	}
	worker := fabriqComponent(t, c, "worker")
	env := envOf(t, worker.Component)
	for key := range env {
		if strings.HasPrefix(key, "FABRIQ_ADMIN_") {
			t.Fatalf("admin API enabled in library: %s", key)
		}
	}
	if env["FABRIQ_DYNAMIC_SYNC_INTERVAL"].Value != "15s" || env["FABRIQ_POSTGRES_DSN"].Template != "postgres://fabriq_app:${secret:app-password}@${dep:postgres:hostport}/${dep:postgres:database}?${dep:postgres:query}" {
		t.Fatal(env)
	}
	for _, o := range k.Outputs {
		if o.PerConsumer || !o.Secret {
			t.Fatalf("unsafe library output: %+v", o)
		}
	}
	for _, name := range []string{"preflight", "migrate", "app-role"} {
		if !slices.Equal(fabriqComponent(t, c, name).Command, []string{"/usr/local/bin/fabriq"}) {
			t.Fatal("job entrypoint not overridden")
		}
	}
	enabled, err := recipes.Default().Resolve("fabriq-library", "", "amd64", "kubernetes")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"rebuild-graph", "rebuild-search"} {
		if !slices.Equal(fabriqComponent(t, enabled, name).After, []string{"worker"}) {
			t.Fatal("rebuild starts before worker")
		}
	}
}
