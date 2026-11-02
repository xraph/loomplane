package graph

import "testing"

func TestExclude(t *testing.T) {
	g := &Graph{
		Services: []Service{
			{ID: "svc:repository:go:cmd/loomplane", Name: "loomplane", Path: "cmd/loomplane", Entrypoint: "cmd/loomplane"},
			{ID: "svc:repository:docker:deployments/miget/build", Name: "build", Path: "deployments/miget/build"},
			{ID: "svc:repository:go:tests/acceptance/fixture/cmd/api", Name: "api", Path: "tests/acceptance/fixture/cmd/api"},
			{ID: "svc:repository:go:tests/acceptance/fixture/cmd/worker", Name: "acceptance-worker", Path: "tests/acceptance/fixture/cmd/worker"},
		},
		Resources: []Resource{{ID: "res:postgres", Kind: "postgres"}},
		Edges: []Edge{
			{From: "svc:repository:go:cmd/loomplane", To: "res:postgres", Kind: Needs},
			{From: "svc:repository:go:tests/acceptance/fixture/cmd/api", To: "res:postgres", Kind: Needs},
		},
	}
	kept, excluded := Exclude(g, []ExclusionRule{
		{Kind: ExcludeService, Match: "svc:repository:docker:deployments/miget/build"},
		{Kind: ExcludePath, Match: "tests/acceptance/**"},
	})
	if len(kept.Services) != 1 || kept.Services[0].Name != "loomplane" {
		t.Fatalf("kept services = %+v, want only loomplane", kept.Services)
	}
	if len(kept.Edges) != 1 || kept.Edges[0].From != "svc:repository:go:cmd/loomplane" {
		t.Fatalf("kept edges = %+v, want only loomplane's", kept.Edges)
	}
	if len(kept.Resources) != 1 {
		t.Fatalf("resources = %+v, want postgres kept", kept.Resources)
	}
	if len(excluded) != 3 {
		t.Fatalf("excluded = %d services, want 3", len(excluded))
	}
	if excluded[1].Rule.Kind != ExcludePath || excluded[2].Rule.Match != "tests/acceptance/**" {
		t.Fatalf("excluded rules = %+v", excluded)
	}
	if len(g.Services) != 4 || len(g.Edges) != 2 {
		t.Fatal("Exclude changed the graph it was given")
	}
}

func TestExcludeNoRules(t *testing.T) {
	g := &Graph{Services: []Service{{ID: "a"}}}
	kept, excluded := Exclude(g, nil)
	if kept != g || len(excluded) != 0 {
		t.Fatalf("Exclude with no rules = %p, %v; want the same graph and nothing excluded", kept, excluded)
	}
}

func TestMatchPath(t *testing.T) {
	for _, c := range []struct {
		pattern, name string
		want          bool
	}{
		{"tests/acceptance/**", "tests/acceptance/fixture/cmd/api", true},
		{"tests/acceptance/**", "tests/acceptance", true},
		{"tests/acceptance/**", "tests/acceptancex/api", false},
		{"deployments/miget", "deployments/miget/build", true},
		{"deployments/miget", "deployments/migets", false},
		{"cmd/*-worker", "cmd/loomplane-worker", true},
		{"cmd/*-worker", "cmd/loomplane", false},
		{"/tests/", "tests/api", true},
		{"", "cmd/api", false},
		{".", "cmd/api", false},
	} {
		if got := matchPath(c.pattern, c.name); got != c.want {
			t.Errorf("matchPath(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}
