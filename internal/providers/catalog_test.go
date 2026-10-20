package providers

import (
	"slices"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestBuildLogCapabilitiesAreOperationScoped(t *testing.T) {
	want := map[string]string{"vercel": "streamed", "cloud-run": "polled", "ecs-fargate": "polled", "render": "unavailable"}
	for _, p := range Catalog() {
		if expected, ok := want[p.Name]; ok {
			if p.BuildLogs != expected {
				t.Errorf("%s output: %s", p.Name, p.BuildLogs)
			}
			if p.Name == "render" && p.LogNote == "" {
				t.Error("Render scope limitation is missing")
			}
		}
	}
}

func TestCatalog_registers(t *testing.T) {
	if _, err := provider.NewRegistry(Catalog()...); err != nil {
		t.Fatal(err)
	}
}

func TestRegistry_capabilities(t *testing.T) {
	r := Registry()
	names := func(ps []*provider.Provider) []string {
		out := make([]string, 0, len(ps))
		for _, p := range ps {
			out = append(out, p.Name)
		}
		return out
	}
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{"web apps", names(r.Running(provider.WebApp)), []string{"vercel", "cloud-run", "ecs-fargate", "render", "server", "kubernetes"}},
		{"static sites", names(r.Running(provider.StaticSite)), []string{"vercel", "render", "server", "kubernetes"}},
		{"workers", names(r.Running(provider.Worker)), []string{"ecs-fargate", "render", "server", "kubernetes"}},
		{"postgres", names(r.Provisioning("postgres")), []string{"render", "cloud-sql", "rds", "server", "kubernetes"}},
		{"mysql", names(r.Provisioning("mysql")), []string{"cloud-sql", "rds", "server"}},
		{"mariadb", names(r.Provisioning("mariadb")), []string{"server"}},
		{"redis", names(r.Provisioning("redis")), []string{"render", "memorystore", "elasticache"}},
		{"kafka", names(r.Provisioning("kafka")), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !slices.Equal(tt.got, tt.want) {
				t.Errorf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
}

func TestProvider_engine(t *testing.T) {
	r := Registry()
	tests := []struct {
		provider string
		workload provider.Workload
		want     provider.Engine
	}{
		// Containers on an instance target are Ctrl Plane's to manage.
		{"cloud-run", provider.HTTPService, provider.CtrlPlane},
		// Vercel and Render build from the source themselves, so
		// Loomplane's adapters manage what they run.
		{"vercel", provider.WebApp, provider.Loomplane},
		{"render", provider.Worker, provider.Loomplane},
		{"render", provider.StaticSite, provider.Loomplane},
	}
	for _, tt := range tests {
		t.Run(tt.provider+"/"+string(tt.workload), func(t *testing.T) {
			p, err := r.Get(tt.provider)
			if err != nil {
				t.Fatal(err)
			}
			runs, ok := p.Running(tt.workload)
			if !ok {
				t.Fatalf("%s doesn't run %s", tt.provider, tt.workload)
			}
			if got := p.Engine(runs); got != tt.want {
				t.Errorf("engine = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestRegistry_rejects(t *testing.T) {
	ok := provider.Provider{
		Name: "x", Title: "X", Cloud: "x", Model: provider.Instance, Available: true,
		Runs: []provider.Runs{{Workload: provider.Worker, Execution: provider.Container, Creates: "thing"}},
	}
	tests := []struct {
		name   string
		mutate func(*provider.Provider)
	}{
		{"bad name", func(p *provider.Provider) { p.Name = "Cloud Run" }},
		{"nothing to do", func(p *provider.Provider) { p.Runs = nil }},
		{"managed runs services", func(p *provider.Provider) { p.Model = provider.Managed }},
		{"unknown model", func(p *provider.Provider) { p.Model = "vm" }},
		{"unavailable without a note", func(p *provider.Provider) { p.Available = false }},
		{"runs without creates", func(p *provider.Provider) { p.Runs[0].Creates = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := ok
			p.Runs = slices.Clone(ok.Runs)
			tt.mutate(&p)
			if _, err := provider.NewRegistry(p); err == nil {
				t.Error("registered")
			}
		})
	}
	if _, err := provider.NewRegistry(ok, ok); err == nil {
		t.Error("registered a name twice")
	}
}

func TestRegistry_aPausedTargetIsForReviewOnly(t *testing.T) {
	p, err := Registry("render").Get("render")
	if err != nil || p.Available || p.Note != PausedNote {
		t.Fatalf("paused render %+v, %v", p, err)
	}
	if p, _ := Registry().Get("render"); !p.Available {
		t.Fatal("render is paused without being named")
	}
}
