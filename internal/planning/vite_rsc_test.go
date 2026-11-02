package planning

import (
	"context"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/providers"
	pd "github.com/xraph/loomplane/pkg/discovery"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestCompute_viteRSCUsesServerDeployment(t *testing.T) {
	for _, target := range []string{"render", "digitalocean", "vercel"} {
		for _, start := range []bool{false, true} {
			scripts := `"build":"vite build"`
			if start {
				scripts += `,"start":"node server.mjs"`
			}
			report, err := discovery.Analyze(context.Background(), fstest.MapFS{
				"package.json":   {Data: []byte(`{"name":"web","scripts":{` + scripts + `},"devDependencies":{"vite":"8","@vitejs/plugin-rsc":"0.5"}}`)},
				"vite.config.ts": {Data: []byte(`import rsc from '@vitejs/plugin-rsc'; export default { plugins: [rsc()] }`)},
			})
			if err != nil {
				t.Fatal(err)
			}
			r := Compute(Input{Environment: "production", Graph: graph.Build(report), Providers: providers.Registry(), Connections: append(slices.Clone(connected), ConnectionOption{ID: "conn_do", Cloud: "digitalocean", Ready: true}),
				Manifest: parse(t, "version: 1\nservices:\n  web:\n    source: .\n    provider: "+target+"\n    port: 3000\n")})
			if target == "vercel" {
				if !slices.Contains(codes(r.Problems), "vite-rsc-runtime:web") {
					t.Fatalf("Vercel accepted without adapter: %+v", r.Problems)
				}
				continue
			}
			if !start {
				if !slices.Contains(codes(r.Problems), "vite-rsc-start:web") {
					t.Fatalf("missing production command accepted: %+v", r.Problems)
				}
				continue
			}
			if !r.Executable {
				t.Fatalf("%s RSC app blocked: %+v", target, r.Problems)
			}
			cfg := action(t, r, "service:web").Service
			if cfg.Framework != "vite-rsc" || cfg.Workload != provider.WebApp || cfg.Execution == provider.Static || cfg.Start != "node server.mjs" {
				t.Fatalf("server deployment lost: %+v", cfg)
			}
		}
	}
}

func TestViteRSCStaticOverrideAndDockerfile(t *testing.T) {
	for _, tc := range []struct {
		kind, image string
		wantStart   bool
	}{
		{"web-app", "", true},
		{"web-app", "Dockerfile", false},
		{"static-site", "", false},
	} {
		p := &planner{}
		cfg := &ServiceConfig{Provider: "render", Framework: "vite-rsc", Workload: provider.Workload(tc.kind), Image: tc.image}
		p.checkViteRSC("web", "services.web", cfg, nil)
		found := slices.ContainsFunc(p.problems, func(f Finding) bool { return f.Code == "vite-rsc-start" })
		if found != tc.wantStart {
			t.Fatalf("kind=%s image=%s problems=%+v", tc.kind, tc.image, p.problems)
		}
	}
}

func TestViteRSCIntegrationKeepsRuntimeRequirements(t *testing.T) {
	g := &graph.Graph{Services: []graph.Service{{
		ID: "svc:node:.", Name: "web", Path: ".", Entrypoint: ".", Ecosystem: pd.Node,
		Kind: graph.WebApp, Deployable: true, Confidence: pd.Likely, Framework: "react-router",
		Commands: pd.Commands{Build: "vite build", Start: "node server.mjs"},
		Requires: []pd.Requirement{{Name: "@vitejs/plugin-rsc"}},
	}}}
	r := Compute(Input{Environment: "production", Graph: g, Providers: providers.Registry(), Connections: connected,
		Manifest: parse(t, "version: 1\nservices:\n  web: {source: ., provider: vercel}\n")})
	if !slices.Contains(codes(r.Problems), "vite-rsc-runtime:web") {
		t.Fatal("framework label hid the RSC runtime requirement")
	}
}
