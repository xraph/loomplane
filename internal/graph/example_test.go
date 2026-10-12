package graph_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/graph"
)

func TestBuild_examplePolyglot(t *testing.T) {
	report, err := discovery.Analyze(context.Background(), os.DirFS("../../examples/polyglot"))
	if err != nil {
		t.Fatal(err)
	}
	g := graph.Build(report)
	want := map[string]graph.ServiceKind{
		"svc:go:services/api":                        graph.HTTPService,
		"svc:go:services/worker/cmd/worker":          graph.Worker,
		"svc:node:apps/docs":                         graph.StaticSite,
		"svc:node:apps/gateway":                      graph.HTTPService,
		"svc:node:apps/web":                          graph.WebApp,
		"svc:rust:crates/ingest/src/bin/backfill.rs": graph.Worker,
		"svc:rust:crates/ingest/src/main.rs":         graph.HTTPService,
	}
	if len(g.Services) != len(want) {
		t.Fatalf("services %+v", g.Services)
	}
	for _, s := range g.Services {
		if want[s.ID] != s.Kind {
			t.Fatalf("%s is %s, want %s", s.ID, s.Kind, want[s.ID])
		}
	}
	if len(g.Packages) != 3 || len(g.Resources) != 2 {
		t.Fatalf("packages %+v resources %+v", g.Packages, g.Resources)
	}
	sum := g.Summarize()
	if sum != (graph.Summary{Services: 7, Deployable: 7, Packages: 3, Resources: 2}) {
		t.Fatalf("summary %+v", sum)
	}
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	var back graph.Graph
	if err := json.Unmarshal(b, &back); err != nil || back.Version != graph.SchemaVersion || len(back.Edges) != len(g.Edges) {
		t.Fatalf("round trip: %v", err)
	}
}
