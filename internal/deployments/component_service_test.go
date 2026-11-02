package deployments

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

func serviceKinds(t *testing.T) *recipes.Registry {
	t.Helper()
	graph := recipestest.Graph()
	graph.Placement = recipes.PlacementServiceProject
	graph.Outputs = append(graph.Outputs,
		recipes.Output{Name: "user", EnvKey: "GRAPH_USER", Template: "operator"},
		recipes.Output{Name: "password", EnvKey: "GRAPH_PASSWORD", Secret: true, Template: "${secret:password}"},
		recipes.Output{Name: "endpoint", EnvKey: "GRAPH_ENDPOINT", From: &recipes.Ref{Component: "server", Field: "address:resp"}},
	)
	graph.Service = &recipes.ServiceDefinition{Connection: map[string]string{recipes.FieldURL: "url", recipes.FieldUsername: "user", recipes.FieldPassword: "password", recipes.FieldHostPort: "endpoint"}}
	consumer := recipestest.Sample()
	consumer.Dependencies = append(consumer.Dependencies, recipes.Dependency{Name: "graph", Title: "Graph", Kinds: []string{"graph"}, Optional: true})
	for _, field := range []string{recipes.FieldUsername, recipes.FieldPassword, recipes.FieldHostPort, "secret:password"} {
		consumer.Components[1].Env = append(consumer.Components[1].Env, recipes.EnvVar{Name: "GRAPH_" + strings.ToUpper(strings.ReplaceAll(field, ":", "_")), From: &recipes.Ref{Dependency: "graph", Field: field}, Requires: "graph"})
	}
	r := recipes.NewRegistry()
	for _, k := range []recipes.Kind{graph, consumer} {
		if err := r.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestReusableServicePublishedConnectionAndReuse(t *testing.T) {
	f := newComponentFixture(t, serviceKinds(t))
	f.secrets.m[resourceKey("org", "env", "app--postgres", "url")] = []byte(postgresURL)
	service := f.graphJob(t)
	if _, err := f.target.Apply(t.Context(), service); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{planning.DependencyNew, planning.DependencyResource} {
		deps := map[string]planning.DependencyChoice{"graph": {Mode: mode, Resource: "app--falkordb", Kind: "graph"}, "cache": {Mode: planning.DependencyOff}, "postgres": newPostgres["postgres"]}
		job := f.componentJob(t, "api", deps)
		run, secret, err := f.target.componentEnv(t.Context(), job, mustKind(t, f.kinds, "sample"))
		// The sample's Postgres values still come through the existing URL fallback.
		if err != nil {
			t.Fatal(err)
		}
		for key, field := range map[string]string{"GRAPH_USERNAME": "output:user", "GRAPH_PASSWORD": "output:password", "GRAPH_HOSTPORT": "output:endpoint", "GRAPH_SECRET_PASSWORD": "secret:password"} {
			want := string(f.secrets.m[ResourceKey("org", "env", "app--falkordb", field)])
			if want == "" || run.Env[key] != want || !secret[key] {
				t.Fatalf("%s mode %s did not receive the private published field", key, mode)
			}
		}
	}
}

func mustKind(t *testing.T, r *recipes.Registry, name string) recipes.Kind {
	t.Helper()
	k, ok := r.Kind(name)
	if !ok {
		t.Fatal(name)
	}
	return k
}

func TestReusableServiceMissingAndDisabledConnection(t *testing.T) {
	f := newComponentFixture(t, serviceKinds(t))
	job := f.graphJob(t)
	reads := f.target.reads(job, mustKind(t, f.kinds, "sample"), "app", map[string]planning.DependencyChoice{"graph": {Mode: planning.DependencyResource, Kind: "graph", Resource: "missing"}, "off": {Mode: planning.DependencyOff}})
	// A declared field is required even if the URL could supply a different value.
	f.secrets.m[ResourceKey("org", "env", "missing", "url")] = []byte("redis://leak_user:leak_password@secret_host:6379")
	if _, _, err := reads.dependency(t.Context(), "graph", recipes.FieldPassword); err == nil || strings.Contains(err.Error(), "leak_") || strings.Contains(err.Error(), "secret_host") {
		t.Fatalf("missing declared field: %v", err)
	}
	if value, on, err := reads.dependency(t.Context(), "off", recipes.FieldPassword); err != nil || on || value != "" {
		t.Fatalf("disabled dependency: %q %t %v", value, on, err)
	}

	f.secrets.m[resourceKey("org", "env", "app--postgres", "url")] = []byte(postgresURL)
	deps := map[string]planning.DependencyChoice{"graph": {Mode: planning.DependencyOff}, "cache": {Mode: planning.DependencyOff}, "postgres": newPostgres["postgres"]}
	run, secret, err := f.target.componentEnv(t.Context(), f.componentJob(t, "api", deps), mustKind(t, f.kinds, "sample"))
	if err != nil {
		t.Fatal(err)
	}
	for key := range run.Env {
		if strings.HasPrefix(key, "GRAPH_") {
			t.Fatalf("disabled dependency supplied %s", key)
		}
	}
	for key := range secret {
		if strings.HasPrefix(key, "GRAPH_") {
			t.Fatalf("disabled dependency retained %s", key)
		}
	}
}

func TestReusableServiceURLFallbackUsesDeclaredConnection(t *testing.T) {
	graph := recipestest.Graph()
	graph.Placement = recipes.PlacementServiceProject
	graph.Outputs = append(graph.Outputs, recipes.Output{Name: "connection", EnvKey: "GRAPH_CONNECTION", Template: "redis://${component:server:address:resp}"})
	graph.Service = &recipes.ServiceDefinition{Connection: map[string]string{recipes.FieldURL: "connection"}}
	kinds := recipes.NewRegistry()
	if err := kinds.Register(graph); err != nil {
		t.Fatal(err)
	}
	f := newComponentFixture(t, kinds)
	job := f.graphJob(t)
	f.secrets.m[ResourceKey("org", "env", "linked", "url")] = []byte("redis://wrong_user:wrong_password@wrong_host:6379")
	f.secrets.m[ResourceKey("org", "env", "linked", "output:connection")] = []byte("redis://mapped_user:mapped_password@mapped_host:6380/graph")
	for _, mode := range []string{planning.DependencyNew, planning.DependencyResource} {
		reads := f.target.reads(job, graph, "app", map[string]planning.DependencyChoice{"graph": {Mode: mode, Kind: "graph", Resource: "linked"}})
		for field, want := range map[string]string{recipes.FieldURL: "redis://mapped_user:mapped_password@mapped_host:6380/graph", recipes.FieldUsername: "mapped_user", recipes.FieldPassword: "mapped_password", recipes.FieldHost: "mapped_host", recipes.FieldHostPort: "mapped_host:6380", recipes.FieldDatabase: "graph"} {
			got, on, err := reads.dependency(t.Context(), "graph", field)
			if err != nil || !on || got != want {
				t.Fatalf("%s mode %s did not read the declared connection", field, mode)
			}
		}
	}
	delete(f.secrets.m, ResourceKey("org", "env", "linked", "output:connection"))
	reads := f.target.reads(job, graph, "app", map[string]planning.DependencyChoice{"graph": {Mode: planning.DependencyResource, Kind: "graph", Resource: "linked"}})
	if _, _, err := reads.dependency(t.Context(), "graph", recipes.FieldHost); err == nil || strings.Contains(err.Error(), "wrong_") || strings.Contains(err.Error(), "mapped_") {
		t.Fatalf("missing mapped connection: %v", err)
	}
}
