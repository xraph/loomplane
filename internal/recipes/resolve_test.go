package recipes_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

func TestResolveOrdersDependenciesJobsProcesses(t *testing.T) {
	r := recipestest.Registry(t)
	c, err := r.Resolve("sample", "1", "arm64", "server")
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != "sample" || c.Version != "1" || c.Product != "1.0.0" || c.Revision != "1" || c.Architecture != "arm64" || c.Target != "server" {
		t.Fatalf("composition header: %+v", c)
	}
	want := []struct {
		name  string
		role  recipes.Role
		step  int
		image string
	}{
		{"migrate", recipes.RoleJob, 0, "registry.example/sample-migrate@sha256:6b1f63a59ef133cbd801d04ddcd873d29b9407cd6c756982d2074459fe32f0f5"},
		{"api", recipes.RoleProcess, 1, "registry.example/sample-api@sha256:01873eef9d70a2cf2ee33aab65b3496fcfc9674a5707b42b12fe19e7ae0c5ce7"},
		{"worker", recipes.RoleProcess, 2, "registry.example/sample-api@sha256:01873eef9d70a2cf2ee33aab65b3496fcfc9674a5707b42b12fe19e7ae0c5ce7"},
	}
	if len(c.Components) != len(want) {
		t.Fatalf("got %d components", len(c.Components))
	}
	for i, w := range want {
		got := c.Components[i]
		if got.Name != w.name || got.Role != w.role || got.Step != w.step || got.Image != w.image {
			t.Fatalf("component %d: got %s %s step %d %s", i, got.Name, got.Role, got.Step, got.Image)
		}
	}
	amd, err := r.Resolve("sample", "", "amd64", "kubernetes")
	if err != nil {
		t.Fatal(err)
	}
	if amd.Version != "1" || amd.Components[1].Image != "registry.example/sample-api@sha256:cb2ffce7d6a5fca5f8615339cc171c74dfe47017c634894dd43e33e10cb4cbba" {
		t.Fatalf("default version or amd64 digest wrong: %+v", amd)
	}
}

func TestResolveSortsJobsBeforeProcessesWithinAStep(t *testing.T) {
	k := recipestest.Sample()
	k.Name = "flat"
	for i := range k.Components {
		k.Components[i].After = nil
		k.Components[i].Env = nil
	}
	// Declared process first, so only the role puts the job ahead.
	k.Components[0], k.Components[1] = k.Components[1], k.Components[0]
	r := recipes.NewRegistry()
	if err := r.Register(k); err != nil {
		t.Fatal(err)
	}
	c, err := r.Resolve("flat", "1", "amd64", "server")
	if err != nil {
		t.Fatal(err)
	}
	if c.Components[0].Name != "migrate" || c.Components[1].Name != "api" || c.Components[2].Name != "worker" {
		t.Fatalf("got %s %s %s", c.Components[0].Name, c.Components[1].Name, c.Components[2].Name)
	}
	for _, rc := range c.Components {
		if rc.Step != 0 {
			t.Fatalf("%s has step %d", rc.Name, rc.Step)
		}
	}
}

func TestResolveRejectsUnknownArchitectureAndTarget(t *testing.T) {
	r := recipestest.Registry(t)
	for _, test := range []struct{ kind, version, architecture, target string }{
		{"sample", "1", "riscv64", "server"},
		{"sample", "1", "arm64", "vercel"},
		{"sample", "9", "arm64", "server"},
		{"postgres", "17", "arm64", "server"},
		{"nope", "1", "arm64", "server"},
	} {
		if _, err := r.Resolve(test.kind, test.version, test.architecture, test.target); err == nil {
			t.Fatalf("resolved %+v", test)
		}
	}
}

func TestResolveReturnsCopies(t *testing.T) {
	r := recipestest.Registry(t)
	c, err := r.Resolve("sample", "1", "arm64", "server")
	if err != nil {
		t.Fatal(err)
	}
	c.Components[1].Ports[0].Container = 1
	again, err := r.Resolve("sample", "1", "arm64", "server")
	if err != nil {
		t.Fatal(err)
	}
	if again.Components[1].Ports[0].Container != 8080 {
		t.Fatal("a resolved composition shares memory with the registry")
	}
}

func TestWithoutDropsRequiredComponentsAndReranks(t *testing.T) {
	k := recipestest.Sample()
	k.Dependencies = append(k.Dependencies, recipes.Dependency{Name: "falkordb", Title: "FalkorDB", Kinds: []string{"redis"}, Optional: true})
	k.Components = append(k.Components, recipes.Component{
		Name: "rebuild-graph", Role: recipes.RoleJob, Requires: "falkordb", After: []string{"migrate"},
		Env: []recipes.EnvVar{{Name: "GRAPH_URL", From: &recipes.Ref{Dependency: "falkordb", Field: recipes.FieldURL}}},
	})
	k.Components[1].After = []string{"migrate", "rebuild-graph"}
	k.Releases[0].Images["rebuild-graph"] = k.Releases[0].Images["migrate"]
	r := recipes.NewRegistry()
	if err := r.Register(k); err != nil {
		t.Fatal(err)
	}
	c, err := r.Resolve("sample", "1", "arm64", "server")
	if err != nil {
		t.Fatal(err)
	}
	steps := func(c *recipes.Composition) string {
		out := make([]string, 0, len(c.Components))
		for _, rc := range c.Components {
			out = append(out, fmt.Sprintf("%s:%d", rc.Name, rc.Step))
		}
		return strings.Join(out, " ")
	}
	if got := steps(c); got != "migrate:0 rebuild-graph:1 api:2 worker:3" {
		t.Fatalf("resolved: %s", got)
	}

	without := c.Without(map[string]bool{"falkordb": true})
	if got := steps(without); got != "migrate:0 api:1 worker:2" {
		t.Fatalf("without falkordb: %s", got)
	}
	for _, rc := range without.Components {
		if slices.Contains(rc.After, "rebuild-graph") {
			t.Fatalf("%s still starts after rebuild-graph: %v", rc.Name, rc.After)
		}
	}
	if without.Kind != c.Kind || without.Revision != c.Revision || without.Architecture != c.Architecture {
		t.Fatalf("header changed: %+v", without)
	}
	if got := steps(c); got != "migrate:0 rebuild-graph:1 api:2 worker:3" || strings.Join(c.Components[2].After, ",") != "migrate,rebuild-graph" {
		t.Fatalf("Without changed the receiver: %s %v", got, c.Components[2].After)
	}
	without.Components[0].Command[0] = "changed"
	if c.Components[0].Command[0] != "sample" {
		t.Fatal("Without shares memory with the receiver")
	}

	same := c.Without(map[string]bool{"cache": true})
	if got := steps(same); got != steps(c) || same == c {
		t.Fatalf("an off dependency no component requires changed the composition: %s", got)
	}
}
