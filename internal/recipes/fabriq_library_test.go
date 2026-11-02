package recipes_test

import (
	"reflect"
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

// TestFabriqLibraryOutputsAreTheServersEnv ties what a consuming app gets
// to what the hosted server runs with, so the two can't drift.
func TestFabriqLibraryOutputsAreTheServersEnv(t *testing.T) {
	hosted, _ := recipes.Default().Kind("fabriq")
	var server recipes.Component
	for _, c := range hosted.Components {
		if c.Name == "server" {
			server = c
		}
	}
	env := envOf(t, server)
	library, _ := recipes.Default().Kind("fabriq-library")
	want := []string{"FABRIQ_POSTGRES_DSN", "FABRIQ_REDIS_ADDR", "FABRIQ_REDIS_USERNAME", "FABRIQ_REDIS_PASSWORD",
		"FABRIQ_FALKORDB_ADDR", "FABRIQ_FALKORDB_USERNAME", "FABRIQ_FALKORDB_PASSWORD",
		"FABRIQ_ELASTICSEARCH_ADDRS", "FABRIQ_ELASTICSEARCH_USERNAME", "FABRIQ_ELASTICSEARCH_PASSWORD"}
	got := make([]string, 0, len(library.Outputs))
	for _, o := range library.Outputs {
		got = append(got, o.EnvKey)
		e, ok := env[o.EnvKey]
		if !ok {
			t.Fatalf("the server doesn't set %s", o.EnvKey)
		}
		source := e.Template
		if e.Value != "" {
			source = e.Value
		}
		if e.Secret != "" {
			source = "${secret:" + e.Secret + "}"
		}
		if o.Template != source || !reflect.DeepEqual(o.From, e.From) || o.Requires != e.Requires {
			t.Fatalf("%s: library %+v (from %+v), server %+v (from %+v)", o.EnvKey, o, o.From, e, e.From)
		}
		if o.From != nil && o.From == e.From {
			t.Fatalf("%s shares the server's Ref", o.EnvKey)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("outputs %v, want %v", got, want)
	}
}
