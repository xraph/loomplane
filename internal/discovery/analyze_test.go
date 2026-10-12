package discovery

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"

	"github.com/xraph/loomplane/pkg/discovery"
)

func analyzeExample(t *testing.T) *discovery.Report {
	t.Helper()
	r, err := Analyze(context.Background(), os.DirFS("../../examples/polyglot"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func unit(t *testing.T, r *discovery.Report, id string) discovery.Unit {
	t.Helper()
	for _, u := range r.Units {
		if u.ID == id {
			return u
		}
	}
	t.Fatalf("no unit %s", id)
	return discovery.Unit{}
}

func TestAnalyze_examplePolyglotWorkspaces(t *testing.T) {
	r := analyzeExample(t)
	want := map[string][]string{
		"go-work": {"libs/go/store", "services/api", "services/worker"},
		"cargo":   {"crates/core", "crates/ingest"},
		"pnpm":    {"apps/docs", "apps/gateway", "apps/web", "packages/ui"},
	}
	if len(r.Workspaces) != len(want) {
		t.Fatalf("workspaces %+v", r.Workspaces)
	}
	for _, ws := range r.Workspaces {
		members, ok := want[ws.Tool]
		if !ok || ws.Path != "." {
			t.Fatalf("unexpected workspace %+v", ws)
		}
		if len(ws.Members) != len(members) {
			t.Fatalf("%s members %v, want %v", ws.Tool, ws.Members, members)
		}
		for i := range members {
			if ws.Members[i] != members[i] {
				t.Fatalf("%s members %v, want %v", ws.Tool, ws.Members, members)
			}
		}
	}
	if len(r.BuildSystems) != 1 || r.BuildSystems[0].Name != "turborepo" {
		t.Fatalf("build systems %+v", r.BuildSystems)
	}
}

func TestAnalyze_goFollowsImportsAcrossTheWorkspace(t *testing.T) {
	r := analyzeExample(t)
	api := unit(t, r, "go:services/api")
	if api.Name != "example.com/acme/api" || api.Runtime != "go 1.26" {
		t.Fatalf("api %+v", api)
	}
	if len(api.DependsOn) != 1 || api.DependsOn[0].Unit != "go:libs/go/store" {
		t.Fatalf("api depends on %+v", api.DependsOn)
	}
	if len(api.Entrypoints) != 1 {
		t.Fatalf("api entrypoints %+v", api.Entrypoints)
	}
	ep := api.Entrypoints[0]
	if len(ep.Frameworks) != 1 || ep.Frameworks[0].Name != "net/http" ||
		ep.Frameworks[0].Evidence != (discovery.Evidence{Path: "services/api/main.go", Line: 13, Detail: `log.Fatal(http.ListenAndServe(":8080", nil))`}) {
		t.Fatalf("api frameworks %+v", ep.Frameworks)
	}
	// pgx is imported by the store library, which the main package imports.
	if len(ep.Resources) != 1 || ep.Resources[0].Kind != "postgres" || ep.Resources[0].Evidence.Path != "libs/go/store/store.go" {
		t.Fatalf("api resources %+v", ep.Resources)
	}

	worker := unit(t, r, "go:services/worker")
	// internal/gen is a main package excluded by //go:build ignore.
	if len(worker.Entrypoints) != 1 || worker.Entrypoints[0].Path != "services/worker/cmd/worker" || worker.Entrypoints[0].Name != "worker" {
		t.Fatalf("worker entrypoints %+v", worker.Entrypoints)
	}
}

func TestAnalyze_rustBinariesAndWorkspaceDependencies(t *testing.T) {
	r := analyzeExample(t)
	ingest := unit(t, r, "rust:crates/ingest")
	if ingest.Name != "ingest" || ingest.Runtime != "rust 2021 edition" {
		t.Fatalf("ingest %+v", ingest)
	}
	if len(ingest.DependsOn) != 1 || ingest.DependsOn[0].Unit != "rust:crates/core" {
		t.Fatalf("ingest depends on %+v", ingest.DependsOn)
	}
	// sqlx's postgres feature comes from [workspace.dependencies].
	if len(ingest.Resources) != 1 || ingest.Resources[0].Kind != "postgres" {
		t.Fatalf("ingest resources %+v", ingest.Resources)
	}
	bins := map[string]discovery.Confidence{}
	for _, ep := range ingest.Entrypoints {
		for _, fw := range ep.Frameworks {
			if fw.Name == "axum" {
				bins[ep.Name] = fw.Confidence
			}
		}
	}
	if bins["ingest"] != discovery.Likely || bins["backfill"] != discovery.Possible {
		t.Fatalf("axum per binary %v", bins)
	}
}

func TestAnalyze_nodeFrameworksAndPrisma(t *testing.T) {
	r := analyzeExample(t)
	web := unit(t, r, "node:apps/web")
	if web.Commands.Start != "next start" || web.Runtime != "node >=24" {
		t.Fatalf("web %+v", web)
	}
	if len(web.Frameworks) != 1 || web.Frameworks[0].Name != "next" || web.Frameworks[0].Kind != discovery.Web {
		t.Fatalf("web frameworks %+v", web.Frameworks)
	}
	if len(web.Resources) != 1 || web.Resources[0].Kind != "postgres" || web.Resources[0].Confidence != discovery.Likely ||
		web.Resources[0].Evidence.Path != "apps/web/prisma/schema.prisma" || web.Resources[0].Evidence.Line != 6 {
		t.Fatalf("web resources %+v", web.Resources)
	}
	if len(web.DependsOn) != 1 || web.DependsOn[0].Unit != "node:packages/ui" {
		t.Fatalf("web depends on %+v", web.DependsOn)
	}
	docs := unit(t, r, "node:apps/docs")
	if len(docs.Frameworks) != 1 || docs.Frameworks[0].Kind != discovery.Static {
		t.Fatalf("docs frameworks %+v", docs.Frameworks)
	}
	ui := unit(t, r, "node:packages/ui")
	if len(ui.Entrypoints) != 0 {
		t.Fatalf("a library has entrypoints: %+v", ui.Entrypoints)
	}
}

func TestAnalyze_containersAndCompose(t *testing.T) {
	r := analyzeExample(t)
	if len(r.Images) != 1 {
		t.Fatalf("images %+v", r.Images)
	}
	img := r.Images[0]
	if img.Path != "services/api/Dockerfile" || img.Base != "gcr.io/distroless/static" || len(img.Ports) != 1 || img.Ports[0] != 8080 {
		t.Fatalf("image %+v", img)
	}
	kinds := map[string]int{}
	for _, res := range r.Resources {
		kinds[res.Kind] = res.Evidence.Line
	}
	if kinds["postgres"] != 3 || kinds["redis"] != 7 || len(kinds) != 2 {
		t.Fatalf("compose resources %+v", r.Resources)
	}
}

func TestAnalyze_isDeterministic(t *testing.T) {
	a, err := json.Marshal(analyzeExample(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(analyzeExample(t))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("two analyses of the same checkout differ")
	}
}

func TestIndex_skipsDependenciesHiddenDirsAndSymlinks(t *testing.T) {
	fsys := fstest.MapFS{
		"go.mod":                          {Data: []byte("module x\n")},
		"node_modules/react/package.json": {Data: []byte("{}")},
		".git/config":                     {Data: []byte("")},
		"apps/web/.next/package.json":     {Data: []byte("{}")},
		"crates/x/target/Cargo.toml":      {Data: []byte("")},
		"apps/web/package.json":           {Data: []byte("{}")},
		"apps/web/link":                   {Data: []byte("/etc/passwd"), Mode: fs.ModeSymlink},
	}
	files, notes, err := Index(context.Background(), fsys)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"apps/web/package.json", "go.mod"}
	if len(files) != len(want) || files[0] != want[0] || files[1] != want[1] {
		t.Fatalf("files %v, want %v", files, want)
	}
	if len(notes) != 0 {
		t.Fatalf("notes %+v", notes)
	}
}

func TestAnalyze_emptyCheckout(t *testing.T) {
	r, err := Analyze(context.Background(), fstest.MapFS{"README.md": {Data: []byte("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Units) != 0 || r.Units == nil || r.Images == nil || r.Notes == nil {
		t.Fatalf("report %+v", r)
	}
}
