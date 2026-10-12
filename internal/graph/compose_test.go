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
