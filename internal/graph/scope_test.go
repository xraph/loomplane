package graph

import (
	"slices"
	"strings"
	"testing"

	pd "github.com/xraph/loomplane/pkg/discovery"
)

func scopeGraph() *Graph {
	units := make([]pd.Unit, 0, 7)
	for _, dir := range []string{"apps/web", "apps/web/testdata/server", "examples/polyglot/api", "examples/polyglot/web", "scripts/migrate", "internal/client/generators/schema"} {
		eco := pd.Go
		if strings.HasSuffix(dir, "web") {
			eco = pd.Node
		}
		units = append(units, pd.Unit{ID: pd.UnitID(eco, dir), Ecosystem: eco, Path: dir,
			Entrypoints: []pd.Entrypoint{{Name: dir, Path: dir, Evidence: []pd.Evidence{{Path: dir + "/main"}},
				Resources: []pd.ResourceUse{{Kind: "redis", Confidence: pd.Possible, Evidence: pd.Evidence{Path: dir + "/main"}}}}}})
	}
	units[0].DependsOn = []pd.Dependency{{Unit: "node:tools/shared", Evidence: pd.Evidence{Path: "apps/web/package.json"}}}
	units = append(units, pd.Unit{ID: "node:tools/shared", Ecosystem: pd.Node, Path: "tools/shared"})
	return Build(&pd.Report{Units: units,
		Images:    []pd.Image{{Path: "fixtures/Dockerfile", Context: "."}},
		Resources: []pd.ResourceUse{{Kind: "postgres", Confidence: pd.Certain, Evidence: pd.Evidence{Path: "examples/polyglot/compose.yml"}}},
		Workspaces: []pd.Workspace{
			{Ecosystem: pd.Node, Path: ".", Members: []string{"apps/web", "examples/polyglot/web", "tools/shared"}},
			{Ecosystem: pd.Node, Path: "examples/polyglot", Members: []string{"examples/polyglot/web"}},
		},
		BuildSystems: []pd.BuildSystem{{Name: "turbo", Path: "."}, {Name: "turbo", Path: "examples/polyglot"}},
	})
}

func TestCompose_defaultScopeLeavesExamplesAndHelpersOut(t *testing.T) {
	raw := scopeGraph()
	g := Compose([]Part{{Source: ref("app", "acme/app"), Graph: raw}})
	if got := ids(g.Services, func(s Service) string { return s.Entrypoint }); !slices.Equal(got, []string{"apps/web"}) {
		t.Fatalf("services %v", got)
	}
	if len(g.Packages) != 1 || g.Packages[0].Path != "tools/shared" {
		t.Fatalf("reachable library missing: %+v", g.Packages)
	}
	if len(g.Resources) != 1 || g.Resources[0].Kind != "redis" || len(g.Resources[0].Evidence) != 1 || g.Resources[0].Evidence[0].Path != "apps/web/main" {
		t.Fatalf("excluded resource evidence leaked: %+v", g.Resources)
	}
	if len(g.Workspaces) != 1 || !slices.Equal(g.Workspaces[0].Members, []string{"apps/web", "tools/shared"}) || len(g.BuildSystems) != 1 {
		t.Fatalf("excluded workspace/build systems leaked: %+v / %+v", g.Workspaces, g.BuildSystems)
	}
	if !slices.ContainsFunc(g.Notes, func(n pd.Note) bool {
		return n.Path == "examples/polyglot/api" && strings.Contains(n.Message, "excluded")
	}) {
		t.Fatalf("missing exclusion explanation: %+v", g.Notes)
	}
	if len(raw.Services) != 7 || len(raw.Resources[1].Evidence) != 6 || len(raw.Workspaces[0].Members) != 3 {
		t.Fatalf("selection mutated raw graph: %+v", raw)
	}
	if got := raw.Summarize(); got.Services != 1 || got.Resources != 1 {
		t.Fatalf("raw summary should count default application: %+v", got)
	}
}

func TestCompose_explicitRootAllowsExamplesButStillFiltersNestedFixtures(t *testing.T) {
	for _, tt := range []struct {
		root string
		want []string
	}{
		{"examples/polyglot", []string{"examples/polyglot/api", "examples/polyglot/web"}},
		{"apps/web", []string{"apps/web"}},
		{"apps/web/testdata", []string{"apps/web/testdata/server"}},
	} {
		t.Run(tt.root, func(t *testing.T) {
			src := ref("app", "acme/app")
			src.Root = tt.root
			g := Compose([]Part{{Source: src, Graph: scopeGraph()}})
			if got := ids(g.Services, func(s Service) string { return s.Entrypoint }); !slices.Equal(got, tt.want) {
				t.Fatalf("services %v, want %v", got, tt.want)
			}
			if g.Summarize().Services != len(tt.want) {
				t.Fatalf("explicit selection summary lost services: %+v", g.Summarize())
			}
		})
	}
}

func TestCompose_declaredForgeTargetOverridesDefaultExclusion(t *testing.T) {
	raw := scopeGraph()
	for i := range raw.Services {
		if raw.Services[i].Entrypoint == "scripts/migrate" {
			raw.Services[i].Evidence = append(raw.Services[i].Evidence, pd.Evidence{Path: ".forge.yaml", Detail: "build.apps.migrate"})
		}
	}
	g := Compose([]Part{{Source: ref("app", "acme/app"), Graph: raw}})
	if got := ids(g.Services, func(s Service) string { return s.Entrypoint }); !slices.Equal(got, []string{"scripts/migrate", "apps/web"}) {
		t.Fatalf("services %v", got)
	}
}

func TestCompose_droppedHelperResourcesDoNotLeakThroughRootManifestEvidence(t *testing.T) {
	ev := pd.Evidence{Path: "go.mod", Line: 5}
	raw := Build(&pd.Report{Units: []pd.Unit{{ID: "go:.", Ecosystem: pd.Go, Path: ".", Entrypoints: []pd.Entrypoint{
		{Name: "api", Path: "cmd/api", Resources: []pd.ResourceUse{{Kind: "redis", Confidence: pd.Possible, Evidence: ev}}},
		{Name: "helper", Path: "scripts/helper", Resources: []pd.ResourceUse{
			{Kind: "redis", Confidence: pd.Certain, Evidence: ev},
			{Kind: "postgres", Confidence: pd.Certain, Evidence: ev},
		}},
	}}}})
	g := Compose([]Part{{Source: ref("app", "acme/app"), Graph: raw}})
	if len(g.Resources) != 1 || g.Resources[0].Kind != "redis" || g.Resources[0].Confidence != pd.Possible || !g.Resources[0].NeedsConfirmation {
		t.Fatalf("excluded helper resources leaked: %+v", g.Resources)
	}
}
