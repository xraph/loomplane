package planning

import (
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/pkg/discovery"
	"github.com/xraph/loomplane/pkg/manifest"
)

func TestDraft_ofThePolyglotExamplePlansWithNoProblems(t *testing.T) {
	g := polyglot(t)
	m := Draft(g, providers.Registry())
	if err := m.Validate(); err != nil {
		t.Fatalf("draft doesn't validate: %v", err)
	}
	// It round-trips, so the YAML the dashboard shows is what gets saved.
	data, err := manifest.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manifest.Parse(data); err != nil {
		t.Fatalf("draft doesn't parse back: %v\n%s", err, data)
	}

	want := map[string]string{
		"api": "cloud-run", "gateway": "cloud-run", "ingest": "cloud-run",
		"web": "vercel", "docs": "vercel", "worker": "ecs-fargate", "backfill": "ecs-fargate",
	}
	if len(m.Services) != len(want) {
		t.Fatalf("services %v", m.ServiceNames())
	}
	for name, prov := range want {
		if got := m.Services[name].Provider; got != prov {
			t.Errorf("%s on %q, want %q", name, got, prov)
		}
	}
	// Most services landed on Google Cloud, so the databases do too.
	if m.Resources["postgres"].Provider != "cloud-sql" || m.Resources["redis"].Provider != "memorystore" {
		t.Errorf("resources %+v", m.Resources)
	}
	// web reaches Postgres through Prisma; the worker reaches Redis.
	if !slices.Contains(m.Services["web"].Needs, "postgres") || !slices.Contains(m.Services["worker"].Needs, "redis") {
		t.Errorf("needs: web %v, worker %v", m.Services["web"].Needs, m.Services["worker"].Needs)
	}

	res := Compute(Input{Environment: "production", Graph: g, Manifest: m, Providers: providers.Registry(), Connections: connected})
	if len(res.Problems) > 0 {
		t.Fatalf("the draft's plan has problems: %+v", res.Problems)
	}
	for _, w := range res.Warnings {
		if w.Code == "service-left-out" || w.Code == "unprovided-resource" || w.Code == "unlinked-resource" {
			t.Errorf("the draft left something out: %+v", w)
		}
	}
}

func TestDraft_leavesWeakAndUnprovisionableResourcesForThePlanToAskAbout(t *testing.T) {
	g := &graph.Graph{
		Services: []graph.Service{{ID: "svc:go:cmd/api", Name: "API Server!", Kind: graph.HTTPService, Deployable: true}},
		Resources: []graph.Resource{
			{ID: "res:postgres", Kind: "postgres"},
			{ID: "res:mysql", Kind: "mysql", NeedsConfirmation: true},
			{ID: "res:kafka", Kind: "kafka"},
		},
		Edges: []graph.Edge{
			{From: "svc:go:cmd/api", To: "res:postgres", Kind: graph.Needs},
			{From: "svc:go:cmd/api", To: "res:mysql", Kind: graph.Needs, Confidence: discovery.Possible},
			{From: "svc:go:cmd/api", To: "res:kafka", Kind: graph.Needs},
		},
	}
	m := Draft(g, providers.Registry())
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Services["api-server"]; !ok {
		t.Fatalf("services %v", m.ServiceNames())
	}
	if _, ok := m.Resources["mysql"]; ok {
		t.Fatal("a weakly evidenced resource was drafted")
	}
	if _, ok := m.Resources["kafka"]; ok {
		t.Fatal("a resource no target provisions was drafted")
	}
	if got := m.Services["api-server"].Needs; !slices.Equal(got, []string{"postgres"}) {
		t.Fatalf("needs %v", got)
	}
	res := Compute(Input{Environment: "production", Graph: g, Manifest: m, Providers: providers.Registry(), Connections: connected})
	if len(res.Problems) > 0 {
		t.Fatalf("the draft's plan is blocked: %+v", res.Problems)
	}
	if !slices.ContainsFunc(res.Warnings, func(f Finding) bool { return f.Code == "unconfirmed-resource" && f.Name == "mysql" }) {
		t.Fatalf("no question about mysql: %+v", res.Warnings)
	}
	if !slices.ContainsFunc(res.Warnings, func(f Finding) bool { return f.Code == "unprovided-resource" && f.Name == "kafka" }) {
		t.Fatalf("no warning about kafka: %+v", res.Warnings)
	}
}

func TestDraft_namesNeverCollide(t *testing.T) {
	g := &graph.Graph{
		Services: []graph.Service{
			{ID: "svc:node:a", Name: "redis", Kind: graph.HTTPService, Deployable: true},
			{ID: "svc:node:b", Name: "Redis", Kind: graph.HTTPService, Deployable: true},
			{ID: "svc:node:c", Name: "9lives", Kind: graph.Worker, Deployable: true},
		},
		Resources: []graph.Resource{{ID: "res:redis", Kind: "redis"}},
	}
	m := Draft(g, providers.Registry())
	if err := m.Validate(); err != nil {
		t.Fatalf("%v: services %v, resources %v", err, m.ServiceNames(), m.ResourceNames())
	}
	if !slices.Equal(m.ServiceNames(), []string{"redis", "redis-2", "service-9lives"}) || !slices.Equal(m.ResourceNames(), []string{"redis-3"}) {
		t.Fatalf("services %v, resources %v", m.ServiceNames(), m.ResourceNames())
	}
}
