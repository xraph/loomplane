package graph

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	pd "github.com/xraph/loomplane/pkg/discovery"
)

func ref(alias, repo string) SourceRef {
	return SourceRef{Alias: alias, Repository: repo, CommitSHA: strings.Repeat(alias[:1], 40), AnalysisID: "anl_" + alias}
}

// apiGraph is a Go server that might use Postgres and requires acme/kit.
func apiGraph() *Graph {
	return Build(&pd.Report{Units: []pd.Unit{{
		ID: "go:.", Ecosystem: pd.Go, Name: "github.com/acme/api", Path: ".",
		Entrypoints: []pd.Entrypoint{{Name: "api", Path: "cmd/api", Resources: []pd.ResourceUse{
			{Kind: "postgres", Confidence: pd.Possible, Evidence: pd.Evidence{Path: "go.mod", Line: 7}},
		}}},
		Requires: []pd.Requirement{{Name: "github.com/acme/kit/log", Version: "v1.2.0", Evidence: pd.Evidence{Path: "go.mod", Line: 5}}},
	}}})
}

// webGraph is a Next.js app that surely uses Postgres, beside a shared UI
// package, with a server in services/ that a root of apps/web leaves out.
func webGraph() *Graph {
	return Build(&pd.Report{Units: []pd.Unit{
		{ID: "node:apps/web", Ecosystem: pd.Node, Name: "web", Path: "apps/web",
			Entrypoints: []pd.Entrypoint{{Name: "web", Path: "apps/web", Resources: []pd.ResourceUse{
				{Kind: "postgres", Confidence: pd.Likely, Evidence: pd.Evidence{Path: "apps/web/package.json", Line: 4}},
			}}},
			DependsOn: []pd.Dependency{{Unit: "node:packages/ui", Evidence: pd.Evidence{Path: "apps/web/package.json", Line: 3}}}},
		{ID: "node:packages/ui", Ecosystem: pd.Node, Name: "@acme/ui", Path: "packages/ui"},
		{ID: "node:services/edge", Ecosystem: pd.Node, Name: "edge", Path: "services/edge",
			Entrypoints: []pd.Entrypoint{{Name: "edge", Path: "services/edge"}}},
	}})
}

func ids[T any](list []T, id func(T) string) []string {
	out := make([]string, 0, len(list))
	for _, x := range list {
		out = append(out, id(x))
	}
	return out
}

func TestCompose_qualifiesAndMerges(t *testing.T) {
	g := Compose([]Part{{Source: ref("web", "acme/web"), Graph: webGraph()}, {Source: ref("api", "acme/api"), Graph: apiGraph()}})
	if got := ids(g.Sources, func(s SourceRef) string { return s.Alias }); !slices.Equal(got, []string{"api", "web"}) {
		t.Fatalf("sources %v", got)
	}
	wantSvcs := []string{"svc:api:go:cmd/api", "svc:web:node:apps/web", "svc:web:node:services/edge"}
	if got := ids(g.Services, func(s Service) string { return s.ID }); !slices.Equal(got, wantSvcs) {
		t.Fatalf("services %v", got)
	}
	if g.Services[0].Source != "api" || g.Services[0].Requires[0].Evidence.Source != "api" {
		t.Fatalf("api service %+v", g.Services[0])
	}
	if len(g.Resources) != 1 {
		t.Fatalf("resources %+v", g.Resources)
	}
	pg := g.Resources[0]
	if pg.ID != "res:postgres" || pg.Confidence != pd.Likely || pg.NeedsConfirmation || len(pg.Evidence) != 2 {
		t.Fatalf("postgres %+v", pg)
	}
	if !slices.ContainsFunc(g.Edges, func(e Edge) bool {
		return e.From == "svc:web:node:apps/web" && e.To == "pkg:web:node:packages/ui" && e.Evidence[0].Source == "web"
	}) {
		t.Fatalf("edges %+v", g.Edges)
	}
}

func TestCompose_isDeterministic(t *testing.T) {
	a := Compose([]Part{{Source: ref("web", "acme/web"), Graph: webGraph()}, {Source: ref("api", "acme/api"), Graph: apiGraph()}})
	b := Compose([]Part{{Source: ref("api", "acme/api"), Graph: apiGraph()}, {Source: ref("web", "acme/web"), Graph: webGraph()}})
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatalf("order changed the graph:\n%s\n%s", ja, jb)
	}
}

func TestCompose_rootKeepsWhatItsServicesUse(t *testing.T) {
	web := ref("web", "acme/web")
	web.Root = "apps/web"
	g := Compose([]Part{{Source: web, Graph: webGraph()}})
	if got := ids(g.Services, func(s Service) string { return s.ID }); !slices.Equal(got, []string{"svc:web:node:apps/web"}) {
		t.Fatalf("services %v", got)
	}
	if got := ids(g.Packages, func(p Package) string { return p.ID }); !slices.Equal(got, []string{"pkg:web:node:packages/ui"}) {
		t.Fatalf("packages %v: a root must keep what its services use", got)
	}
}

func TestCompose_notesWhatItCouldntJoin(t *testing.T) {
	old := apiGraph()
	old.Version = 1
	g := Compose([]Part{{Source: ref("api", "acme/api"), Graph: old}, {Source: ref("web", "acme/web")}})
	msgs := ids(g.Notes, func(n pd.Note) string { return n.Source + ": " + n.Message })
	if len(msgs) != 2 || !strings.Contains(msgs[0], "api: api was analyzed before") || !strings.Contains(msgs[1], "web: web has no successful analysis") {
		t.Fatalf("notes %q", msgs)
	}
}

func kitGraph(name string) *Graph {
	return Build(&pd.Report{Units: []pd.Unit{{ID: "go:.", Ecosystem: pd.Go, Name: name, Path: "."}}})
}

func uiGraph() *Graph {
	return Build(&pd.Report{Units: []pd.Unit{{ID: "node:.", Ecosystem: pd.Node, Name: "@acme/tokens", Path: "."}}})
}

func edgeBetween(g *Graph, from, to string) *Edge {
	for i := range g.Edges {
		if g.Edges[i].From == from && g.Edges[i].To == to {
			return &g.Edges[i]
		}
	}
	return nil
}

func TestCompose_linksAGoModuleOnItsRepositoryAsCertain(t *testing.T) {
	g := Compose([]Part{{Source: ref("api", "acme/api"), Graph: apiGraph()}, {Source: ref("kit", "Acme/Kit"), Graph: kitGraph("github.com/acme/kit/log")}})
	e := edgeBetween(g, "svc:api:go:cmd/api", "pkg:kit:go:.")
	if e == nil || e.Kind != Uses || e.Confidence != pd.Certain || e.Evidence[0].Source != "api" || e.Evidence[0].Line != 5 {
		t.Fatalf("edge %+v in %+v", e, g.Edges)
	}
}

func TestCompose_linksAnNPMNameAsLikely(t *testing.T) {
	site := Build(&pd.Report{Units: []pd.Unit{{
		ID: "node:.", Ecosystem: pd.Node, Name: "site", Path: ".",
		Entrypoints: []pd.Entrypoint{{Name: "site", Path: "."}},
		Requires:    []pd.Requirement{{Name: "@acme/tokens", Evidence: pd.Evidence{Path: "package.json", Line: 9}}},
	}}})
	g := Compose([]Part{{Source: ref("site", "acme/site"), Graph: site}, {Source: ref("ui", "acme/ui"), Graph: uiGraph()}})
	if e := edgeBetween(g, "svc:site:node:.", "pkg:ui:node:."); e == nil || e.Confidence != pd.Likely {
		t.Fatalf("edge %+v in %+v", e, g.Edges)
	}

	// Published twice, the name is ambiguous: no edge, only a note.
	g = Compose([]Part{
		{Source: ref("site", "acme/site"), Graph: site},
		{Source: ref("ui", "acme/ui"), Graph: uiGraph()},
		{Source: ref("vendor", "acme/vendor"), Graph: uiGraph()},
	})
	if slices.ContainsFunc(g.Edges, func(e Edge) bool { return e.From == "svc:site:node:." }) {
		t.Fatalf("an ambiguous name got an edge: %+v", g.Edges)
	}
	if !slices.ContainsFunc(g.Notes, func(n pd.Note) bool {
		return n.Source == "site" && strings.Contains(n.Message, "@acme/tokens is published by ui and vendor")
	}) {
		t.Fatalf("notes %+v", g.Notes)
	}
}

func TestCompose_linksARustGitDependencyAsCertain(t *testing.T) {
	app := Build(&pd.Report{Units: []pd.Unit{{
		ID: "rust:.", Ecosystem: pd.Rust, Name: "app", Path: ".",
		Entrypoints: []pd.Entrypoint{{Name: "app", Path: "src/main.rs"}},
		Requires:    []pd.Requirement{{Name: "kit", Git: "https://github.com/Acme/Crates.git", Evidence: pd.Evidence{Path: "Cargo.toml", Line: 6}}},
	}}})
	crates := Build(&pd.Report{Units: []pd.Unit{{ID: "rust:kit", Ecosystem: pd.Rust, Name: "kit", Path: "kit"}}})
	g := Compose([]Part{{Source: ref("app", "acme/app"), Graph: app}, {Source: ref("crates", "acme/crates"), Graph: crates}})
	if e := edgeBetween(g, "svc:app:rust:src/main.rs", "pkg:crates:rust:kit"); e == nil || e.Confidence != pd.Certain {
		t.Fatalf("edge %+v in %+v", e, g.Edges)
	}
}

func TestCompose_neverLinksARepositoryToItself(t *testing.T) {
	g := Compose([]Part{{Source: ref("api", "acme/api"), Graph: apiGraph()}})
	if len(g.Edges) != 1 || g.Edges[0].To != "res:postgres" {
		t.Fatalf("edges %+v", g.Edges)
	}
}
