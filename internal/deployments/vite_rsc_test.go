package deployments

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestViteRSCExamplePreservesServerBuildContext(t *testing.T) {
	repo := os.DirFS("../../examples/vite-rsc")
	report, err := discovery.Analyze(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.Build(report)
	if len(g.Services) != 1 || g.Services[0].Kind != graph.WebApp || g.Services[0].Framework != "vite-rsc" {
		t.Fatalf("example graph: %+v", g.Services)
	}
	raw, err := fs.ReadFile(repo, "loomplane.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	result := planning.Compute(planning.Input{Environment: "production", Graph: g, Manifest: m, Providers: providers.Registry(), Connections: []planning.ConnectionOption{{ID: "render", Cloud: "render", Ready: true}}})
	if !result.Executable {
		t.Fatalf("example cannot deploy: %+v", result.Problems)
	}
	var cfg *planning.ServiceConfig
	for _, a := range result.Actions {
		if a.Name == "web" {
			cfg = a.Service
		}
	}
	if cfg == nil || cfg.Image != "Dockerfile" || cfg.Workload != provider.WebApp {
		t.Fatalf("example lost container: %+v", cfg)
	}
	sources := map[string]string{}
	err = fs.WalkDir(repo, ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if name == "node_modules" || name == "dist" {
				return fs.SkipDir
			}
			return nil
		}
		data, readErr := fs.ReadFile(repo, name)
		if readErr != nil {
			return readErr
		}
		sources[name] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareImageContext(bytes.NewReader(buildArchive(t, sources)), cfg)
	if err != nil {
		t.Fatal(err)
	}
	files := contextFiles(t, prepared.Tar)
	for _, name := range []string{"package-lock.json", "server.mjs", "src/rsc.jsx", "src/ssr.jsx", "src/browser.jsx", "src/actions.js"} {
		if files[name] == "" {
			t.Fatalf("missing server build input %s", name)
		}
	}
	if files[prepared.Dockerfile] != sources["Dockerfile"] {
		t.Fatal("reviewed Dockerfile replaced")
	}
	spec, err := renderSpec(&Job{Deployment: &Deployment{ProjectID: "project_1", Environment: "production"}, Action: planning.Action{Name: "web", Service: cfg}}, cfg, "https://github.com/example/rsc")
	if err != nil || spec.Runtime != "docker" || spec.PublishPath != "" {
		t.Fatalf("RSC selected static publish: %+v, %v", spec, err)
	}
}

func TestViteRSCNativeTargetsRunServer(t *testing.T) {
	cfg := &planning.ServiceConfig{Source: "apps/web", Ecosystem: "node", Framework: "vite-rsc", Workload: provider.WebApp, Execution: provider.Container, Public: true, Port: 3000, Build: "vite build", Start: "node server.mjs"}
	job := &Job{Deployment: &Deployment{ProjectID: "project_1", Environment: "production"}, Action: planning.Action{Name: "web", Service: cfg}}
	spec, err := renderSpec(job, cfg, "https://github.com/example/rsc")
	if err != nil || spec.Runtime != "node" || spec.Start != "npm start" || !strings.Contains(spec.Build, "npm run build") || spec.PublishPath != "" {
		t.Fatalf("native server lost: %+v, %v", spec, err)
	}
	app := doAppSpec(job, cfg, "example/rsc", "main")
	if len(app.Services) != 1 || len(app.StaticSites) != 0 || app.Services[0].RunCommand != "node server.mjs" {
		t.Fatalf("RSC static deployment: %+v", app)
	}
}
