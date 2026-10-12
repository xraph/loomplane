package graph

import (
	"testing"

	pd "github.com/xraph/loomplane/pkg/discovery"
)

func fw(name string, kind pd.FrameworkKind, conf pd.Confidence) pd.Framework {
	return pd.Framework{Name: name, Kind: kind, Confidence: conf}
}

func TestService_kindFollowsTheStrongestLikelyFramework(t *testing.T) {
	tests := []struct {
		name       string
		frameworks []pd.Framework
		want       ServiceKind
		wantFw     string
		deployable bool
	}{
		{"no framework", nil, Worker, "", true},
		{"http", []pd.Framework{fw("gin", pd.HTTP, pd.Likely)}, HTTPService, "gin", true},
		{"grpc", []pd.Framework{fw("grpc", pd.GRPC, pd.Likely)}, HTTPService, "grpc", true},
		{"server with cli flags", []pd.Framework{fw("cobra", pd.CLI, pd.Likely), fw("forge", pd.HTTP, pd.Likely)}, HTTPService, "forge", true},
		{"cli", []pd.Framework{fw("cobra", pd.CLI, pd.Likely)}, Tool, "cobra", false},
		{"web app with custom server", []pd.Framework{fw("express", pd.HTTP, pd.Likely), fw("next", pd.Web, pd.Likely)}, WebApp, "next", true},
		{"static", []pd.Framework{fw("vite", pd.Static, pd.Likely)}, StaticSite, "vite", true},
		{"native", []pd.Framework{fw("expo", pd.Native, pd.Likely)}, NativeApp, "expo", false},
		{"only an inherited framework", []pd.Framework{fw("axum", pd.HTTP, pd.Possible)}, Worker, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := pd.Unit{ID: "go:.", Ecosystem: pd.Go, Path: "."}
			svc := service(u, pd.Entrypoint{Name: "x", Path: ".", Frameworks: tt.frameworks})
			if svc.Kind != tt.want || svc.Framework != tt.wantFw || svc.Deployable != tt.deployable {
				t.Fatalf("service %+v, want %s/%s", svc, tt.want, tt.wantFw)
			}
		})
	}
}

func TestBuild_imagesAttachOrStandAlone(t *testing.T) {
	r := &pd.Report{
		Units: []pd.Unit{{
			ID: "go:svc", Ecosystem: pd.Go, Path: "svc",
			Entrypoints: []pd.Entrypoint{{Name: "svc", Path: "svc"}},
		}},
		Images: []pd.Image{
			{Path: "svc/Dockerfile", Context: "svc", Ports: []int{8080}},
			{Path: "python/Dockerfile", Context: "python", Ports: []int{5000}},
		},
	}
	g := Build(r)
	if len(g.Services) != 2 {
		t.Fatalf("services %+v", g.Services)
	}
	py, svc := g.Services[0], g.Services[1]
	if svc.Image != "svc/Dockerfile" || len(svc.Ports) != 1 || svc.Ports[0] != 8080 {
		t.Fatalf("go service %+v", svc)
	}
	if py.ID != "svc:container:python/Dockerfile" || py.Kind != HTTPService || py.Ecosystem != pd.Container || py.Name != "python" {
		t.Fatalf("container service %+v", py)
	}
}

func TestBuild_toolingRootIsNotAPackage(t *testing.T) {
	r := &pd.Report{
		Workspaces: []pd.Workspace{{Ecosystem: pd.Node, Tool: "pnpm", Path: "."}},
		Units: []pd.Unit{
			{ID: "node:.", Ecosystem: pd.Node, Name: "acme", Path: "."},
			{ID: "node:packages/ui", Ecosystem: pd.Node, Name: "@acme/ui", Path: "packages/ui"},
		},
	}
	g := Build(r)
	if len(g.Packages) != 1 || g.Packages[0].ID != "pkg:node:packages/ui" {
		t.Fatalf("packages %+v", g.Packages)
	}
}

func TestBuild_resourcesTakeTheStrongestEvidence(t *testing.T) {
	weak := pd.ResourceUse{Kind: "redis", Confidence: pd.Possible, Evidence: pd.Evidence{Path: "a/package.json", Line: 4}}
	r := &pd.Report{
		Units: []pd.Unit{{
			ID: "node:a", Ecosystem: pd.Node, Path: "a",
			Entrypoints: []pd.Entrypoint{{Name: "a", Path: "a", Resources: []pd.ResourceUse{weak}}},
		}},
	}
	g := Build(r)
	if len(g.Resources) != 1 || !g.Resources[0].NeedsConfirmation || g.Resources[0].Confidence != pd.Possible {
		t.Fatalf("resources %+v", g.Resources)
	}
	r.Resources = []pd.ResourceUse{{Kind: "redis", Confidence: pd.Likely, Evidence: pd.Evidence{Path: "compose.yaml", Line: 3}}}
	g = Build(r)
	res := g.Resources[0]
	if res.NeedsConfirmation || res.Confidence != pd.Likely || len(res.Evidence) != 2 || res.Evidence[0].Path != "a/package.json" {
		t.Fatalf("resource %+v", res)
	}
	if len(g.Edges) != 1 || g.Edges[0].From != "svc:node:a" || g.Edges[0].To != "res:redis" || g.Edges[0].Kind != Needs {
		t.Fatalf("edges %+v", g.Edges)
	}
}

func TestBuild_rootDockerfileMatchesTheBinaryItBuilds(t *testing.T) {
	r := &pd.Report{
		Units: []pd.Unit{{
			ID: "go:.", Ecosystem: pd.Go, Path: ".",
			Entrypoints: []pd.Entrypoint{{Name: "api", Path: "cmd/api"}, {Name: "apibench", Path: "cmd/apibench"}},
		}},
		Images: []pd.Image{{
			Path: "Dockerfile", Context: ".",
			Builds: []pd.Evidence{{Path: "Dockerfile", Line: 4, Detail: "RUN go build -o /out/api ./cmd/api"}},
		}},
	}
	g := Build(r)
	if len(g.Services) != 2 {
		t.Fatalf("services %+v", g.Services)
	}
	api, bench := g.Services[0], g.Services[1]
	if api.Image != "Dockerfile" || bench.Image != "" {
		t.Fatalf("api %+v bench %+v", api, bench)
	}
	if last := api.Evidence[len(api.Evidence)-1]; last.Line != 4 {
		t.Fatalf("api evidence %+v", api.Evidence)
	}
}

func TestBuild_nodesCarryWhatTheirUnitRequires(t *testing.T) {
	req := pd.Requirement{Name: "github.com/acme/kit", Version: "v1.2.0", Evidence: pd.Evidence{Path: "go.mod", Line: 5}}
	g := Build(&pd.Report{Units: []pd.Unit{
		{ID: "go:.", Ecosystem: pd.Go, Name: "github.com/acme/api", Path: ".",
			Entrypoints: []pd.Entrypoint{{Name: "api", Path: "cmd/api"}}, Requires: []pd.Requirement{req}},
		{ID: "go:lib", Ecosystem: pd.Go, Name: "github.com/acme/api/lib", Path: "lib", Requires: []pd.Requirement{req}},
	}})
	if g.Version != 2 {
		t.Fatalf("version %d", g.Version)
	}
	svc := g.Services[0]
	if svc.UnitName != "github.com/acme/api" || len(svc.Requires) != 1 || svc.Requires[0] != req {
		t.Fatalf("service %+v", svc)
	}
	if pkg := g.Packages[0]; len(pkg.Requires) != 1 {
		t.Fatalf("package %+v", pkg)
	}
}

func TestService_Local(t *testing.T) {
	for _, tt := range []struct {
		s    Service
		want string
	}{
		{Service{ID: "svc:go:cmd/api"}, "go:cmd/api"},
		{Service{ID: "svc:api:go:cmd/api", Source: "api"}, "go:cmd/api"},
		{Service{ID: "svc:web:container:Dockerfile", Source: "web"}, "container:Dockerfile"},
	} {
		if got := tt.s.Local(); got != tt.want {
			t.Errorf("%s.Local() = %q, want %q", tt.s.ID, got, tt.want)
		}
	}
}
