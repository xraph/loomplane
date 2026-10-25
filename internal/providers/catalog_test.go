package providers

import (
	"reflect"
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestManagedProfilesShareTheDeploymentContract(t *testing.T) {
	for _, target := range []string{"rds", "cloud-sql"} {
		p, err := Registry().Get(target)
		if err != nil {
			t.Fatal(err)
		}
		for _, offer := range p.Provisions {
			if offer.Kind == "postgres" {
				if !reflect.DeepEqual(offer.ManagedProfiles, manageddb.Profiles(target)) {
					t.Fatalf("%s publishes a different profile than deployment", target)
				}
			} else if len(offer.ManagedProfiles) != 0 {
				t.Fatal("unreviewed managed engine published a profile")
			}
		}
	}
}

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
		{"web apps", names(r.Running(provider.WebApp)), []string{"vercel", "cloud-run", "ecs-fargate", "render", "digitalocean", "azure-container-apps", "server", "kubernetes"}},
		{"http services", names(r.Running(provider.HTTPService)), []string{"cloud-run", "ecs-fargate", "render", "digitalocean", "azure-container-apps", "server", "kubernetes"}},
		{"static sites", names(r.Running(provider.StaticSite)), []string{"vercel", "render", "digitalocean", "azure-container-apps", "server", "kubernetes"}},
		{"workers", names(r.Running(provider.Worker)), []string{"ecs-fargate", "render", "digitalocean", "azure-container-apps", "server", "kubernetes"}},
		{"postgres", names(r.Provisioning("postgres")), []string{"render", "digitalocean", "cloud-sql", "rds", "azure-database", "server", "kubernetes"}},
		{"mysql", names(r.Provisioning("mysql")), []string{"digitalocean", "cloud-sql", "rds", "azure-database", "server"}},
		{"mariadb", names(r.Provisioning("mariadb")), []string{"server"}},
		{"redis", names(r.Provisioning("redis")), []string{"render", "digitalocean", "memorystore", "elasticache", "azure-managed-redis", "server"}},
		{"valkey", names(r.Provisioning("valkey")), []string{"server"}},
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
		// Loomplane builds and runs Container Apps itself, without Ctrl Plane.
		{"azure-container-apps", provider.HTTPService, provider.Loomplane},
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

func provisionsOf(t *testing.T, all []provider.Provider, name string) []provider.Provisions {
	t.Helper()
	for _, p := range all {
		if p.Name == name {
			return p.Provisions
		}
	}
	t.Fatalf("no %s provider", name)
	return nil
}

func TestServerProvisionsKeepBuiltInOrderThenRegisteredKinds(t *testing.T) {
	builtIn := []provider.Provisions{
		{Kind: "postgres", Creates: "Persistent PostgreSQL container", Versions: []string{"17"}},
		{Kind: "mysql", Creates: "Persistent MySQL container", Versions: []string{"8.4"}},
		{Kind: "mariadb", Creates: "Persistent MariaDB container", Versions: []string{"11.8"}},
		{Kind: "redis", Creates: "Persistent Redis container", Versions: []string{"8.2"}},
		{Kind: "valkey", Creates: "Persistent Valkey container", Versions: []string{"8.1"}},
	}
	wantDefault := append(slices.Clone(builtIn),
		provider.Provisions{Kind: "fabriq", Creates: "Fabriq catalog service", Versions: []string{"1.9"}},
		provider.Provisions{Kind: "fabriq-library", Creates: "Fabriq library catalog service", Versions: []string{"1.9"}},
		provider.Provisions{Kind: "memcached", Creates: "Memcached catalog service", Versions: []string{"1.6"}},
		provider.Provisions{Kind: "nats", Creates: "NATS JetStream catalog service", Versions: []string{"2.11"}},
		provider.Provisions{Kind: "rabbitmq", Creates: "RabbitMQ catalog service", Versions: []string{"4.3"}},
		provider.Provisions{Kind: "timescaledb", Creates: "PostgreSQL 16 with TimescaleDB catalog service", Versions: []string{"16"}},
	)
	got := provisionsOf(t, Catalog(), "server")
	if !reflect.DeepEqual(got, wantDefault) {
		t.Fatalf("default registry changed the server provisions: %+v", got)
	}

	got = provisionsOf(t, catalogWith(recipestest.Registry(t)), "server")
	if len(got) != 7 || !reflect.DeepEqual(got[:5], builtIn) {
		t.Fatalf("built-in provisions moved or changed: %+v", got)
	}
	want := provider.Provisions{Kind: "sample", Creates: "Sample catalog service", Versions: []string{"1"}}
	if !reflect.DeepEqual(got[6], want) {
		t.Fatalf("registered kind: %+v, want %+v", got[6], want)
	}
}

func TestKubernetesProvisionsAcceptRegisteredKinds(t *testing.T) {
	first := provider.Provisions{Kind: "postgres", Creates: "Retained PostgreSQL StatefulSet", Versions: []string{"17"}}
	got := provisionsOf(t, Catalog(), "kubernetes")
	fabriq := provider.Provisions{Kind: "fabriq", Creates: "Fabriq catalog service", Versions: []string{"1.9"}}
	timescaledb := provider.Provisions{Kind: "timescaledb", Creates: "PostgreSQL 16 with TimescaleDB catalog service", Versions: []string{"16"}}
	if !reflect.DeepEqual(got, []provider.Provisions{first, fabriq, {Kind: "fabriq-library", Creates: "Fabriq library catalog service", Versions: []string{"1.9"}}, timescaledb}) {
		t.Fatalf("default registry changed the Kubernetes provisions: %+v", got)
	}

	got = provisionsOf(t, catalogWith(recipestest.Registry(t)), "kubernetes")
	if len(got) != 3 || !reflect.DeepEqual(got[0], first) || got[2].Kind != "sample" || got[2].Creates != "Sample catalog service" {
		t.Fatalf("Kubernetes provisions: %+v", got)
	}
	// Managed clouds don't gain registered kinds.
	for _, p := range catalogWith(recipestest.Registry(t)) {
		if p.Cloud == "server" || p.Cloud == "kubernetes" {
			continue
		}
		for _, offer := range p.Provisions {
			if offer.Kind == "sample" {
				t.Errorf("%s offers the sample kind", p.Name)
			}
		}
	}
	if _, err := provider.NewRegistry(catalogWith(recipestest.Registry(t))...); err != nil {
		t.Fatal(err)
	}
}

func TestServerProvisionsSkipDependencyOnlyKinds(t *testing.T) {
	kinds := recipestest.Registry(t)
	if err := kinds.Register(recipestest.Graph()); err != nil {
		t.Fatal(err)
	}
	all := catalogWith(kinds)
	for _, name := range []string{"server", "kubernetes"} {
		got := provisionsOf(t, all, name)
		if !slices.ContainsFunc(got, func(p provider.Provisions) bool { return p.Kind == "sample" }) {
			t.Fatalf("%s lost the sample kind: %+v", name, got)
		}
		if slices.ContainsFunc(got, func(p provider.Provisions) bool { return p.Kind == "graph" }) {
			t.Fatalf("%s offers a dependency-only kind: %+v", name, got)
		}
	}
}
