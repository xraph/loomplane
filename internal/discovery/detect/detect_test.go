package detect

import (
	"context"
	"io/fs"
	"sort"
	"testing"
	"testing/fstest"

	"github.com/xraph/loomplane/pkg/discovery"
)

// run runs one detector over an in-memory checkout.
func run(t *testing.T, d discovery.Detector, files map[string]string) *discovery.Report {
	t.Helper()
	fsys := fstest.MapFS{}
	names := make([]string, 0, len(files))
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
		names = append(names, name)
	}
	sort.Strings(names)
	r := &discovery.Report{}
	if err := d.Detect(context.Background(), discovery.Repo{FS: fsys, Files: names}, r); err != nil {
		t.Fatal(err)
	}
	return r
}

func onlyUnit(t *testing.T, r *discovery.Report, id string) discovery.Unit {
	t.Helper()
	for _, u := range r.Units {
		if u.ID == id {
			return u
		}
	}
	t.Fatalf("no unit %s in %+v", id, r.Units)
	return discovery.Unit{}
}

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern, name string
		want          bool
	}{
		{"apps/*", "apps/web", true},
		{"apps/*", "apps/web/nested", false},
		{"./apps/*", "apps/web", true},
		{"apps/**", "apps/web/nested", true},
		{"apps/**", "apps", true},
		{"**/api", "services/go/api", true},
		{"services/*/api", "services/go/api", true},
		{"packages/ui-?", "packages/ui-x", true},
		{"packages/*", "apps/web", false},
		{"crates/core", "crates/core", true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+" "+tt.name, func(t *testing.T) {
			if got := matchGlob(tt.pattern, tt.name); got != tt.want {
				t.Fatalf("matchGlob(%q, %q) = %v", tt.pattern, tt.name, got)
			}
		})
	}
}

func TestExpand_negatedPatternsRemove(t *testing.T) {
	got := expand([]string{"packages/*", "!packages/legacy"}, []string{"packages/legacy", "packages/ui", "apps/web"})
	if len(got) != 1 || got[0] != "packages/ui" {
		t.Fatalf("expand = %v", got)
	}
}

func TestContainerPort(t *testing.T) {
	tests := []struct {
		in   string
		want int
		ok   bool
	}{
		{"8080", 8080, true},
		{"8080/tcp", 8080, true},
		{"3000:8080", 8080, true},
		{"127.0.0.1:3000:8080/udp", 8080, true},
		{"$PORT", 0, false},
		{"70000", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := containerPort(tt.in)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("containerPort(%q) = %d, %v", tt.in, got, ok)
			}
		})
	}
}

func TestImageName(t *testing.T) {
	tests := map[string]string{
		"postgres:17-alpine":                                   "postgres",
		"timescale/timescaledb-ha:pg17":                        "timescaledb-ha",
		"ghcr.io/acme/redis@sha256:abc":                        "redis",
		"docker.elastic.co/elasticsearch/elasticsearch:8.15.0": "elasticsearch",
	}
	for in, want := range tests {
		if got := imageName(in); got != want {
			t.Fatalf("imageName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNode_frameworkKindsFollowConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  discovery.FrameworkKind
	}{
		{"next server", map[string]string{
			"package.json": `{"name":"a","scripts":{"build":"next build"},"dependencies":{"next":"16"}}`,
		}, discovery.Web},
		{"next static export", map[string]string{
			"package.json":   `{"name":"a","dependencies":{"next":"16"}}`,
			"next.config.ts": "export default { output: 'export' }",
		}, discovery.Static},
		{"sveltekit static adapter", map[string]string{
			"package.json":     `{"name":"a","devDependencies":{"@sveltejs/kit":"2","@sveltejs/adapter-static":"3"}}`,
			"svelte.config.js": "export default {}",
		}, discovery.Static},
		{"astro with a server adapter", map[string]string{
			"package.json":     `{"name":"a","dependencies":{"astro":"5","@astrojs/node":"9"}}`,
			"astro.config.mjs": "export default {}",
		}, discovery.Web},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := onlyUnit(t, run(t, Node{}, tt.files), "node:.")
			if len(u.Frameworks) != 1 || u.Frameworks[0].Kind != tt.want {
				t.Fatalf("frameworks %+v, want kind %s", u.Frameworks, tt.want)
			}
			if len(u.Entrypoints) != 1 {
				t.Fatalf("entrypoints %+v", u.Entrypoints)
			}
		})
	}
}

func TestNode_sharedPackagesUsingAnAppFrameworkStayLibraries(t *testing.T) {
	tests := map[string]string{
		"next helpers":       `{"name":"seo","scripts":{"typecheck":"tsc"},"dependencies":{"next":"16"}}`,
		"express middleware": `{"name":"auth","dependencies":{"express":"5"}}`,
	}
	for name, pkg := range tests {
		t.Run(name, func(t *testing.T) {
			u := onlyUnit(t, run(t, Node{}, map[string]string{"package.json": pkg}), "node:.")
			if len(u.Entrypoints) != 0 || len(u.Frameworks) != 1 || u.Frameworks[0].Confidence != discovery.Possible {
				t.Fatalf("unit %+v", u)
			}
		})
	}
}

func TestNode_viteWithoutIndexHTMLIsALibrary(t *testing.T) {
	u := onlyUnit(t, run(t, Node{}, map[string]string{
		"package.json": `{"name":"lib","devDependencies":{"vite":"7"}}`,
	}), "node:.")
	if len(u.Frameworks) != 0 || len(u.Entrypoints) != 0 {
		t.Fatalf("unit %+v", u)
	}
}

func TestNode_yarnObjectWorkspacesAndDevOnlyDrivers(t *testing.T) {
	r := run(t, Node{}, map[string]string{
		"package.json":            `{"name":"root","private":true,"workspaces":{"packages":["services/*"]}}`,
		"yarn.lock":               "",
		"services/a/package.json": "{\n  \"name\": \"a\",\n  \"dependencies\": {\n    \"express\": \"5\",\n    \"pg\": \"8\"\n  },\n  \"devDependencies\": {\n    \"ioredis\": \"5\"\n  }\n}\n",
	})
	if len(r.Workspaces) != 1 || r.Workspaces[0].Tool != "yarn" || len(r.Workspaces[0].Members) != 1 || r.Workspaces[0].Members[0] != "services/a" {
		t.Fatalf("workspaces %+v", r.Workspaces)
	}
	a := onlyUnit(t, r, "node:services/a")
	// ioredis is only a dev dependency, so nothing in production talks to Redis.
	if len(a.Resources) != 1 || a.Resources[0].Kind != "postgres" || a.Resources[0].Evidence.Line != 5 {
		t.Fatalf("resources %+v", a.Resources)
	}
}

func TestNode_nxProjectWithoutPackageJSON(t *testing.T) {
	r := run(t, Node{}, map[string]string{
		"nx.json":               "{}",
		"apps/api/project.json": "{\n  \"name\": \"api\",\n  \"targets\": {\n    \"serve\": { \"executor\": \"@nx/node:execute\" }\n  }\n}\n",
	})
	u := onlyUnit(t, r, "node:apps/api")
	if len(u.Frameworks) != 1 || u.Frameworks[0].Kind != discovery.HTTP || u.Frameworks[0].Evidence.Line != 4 {
		t.Fatalf("frameworks %+v", u.Frameworks)
	}
	if len(r.BuildSystems) != 1 || r.BuildSystems[0].Name != "nx" {
		t.Fatalf("build systems %+v", r.BuildSystems)
	}
}

func TestGo_replaceDirectiveAndNestedModules(t *testing.T) {
	r := run(t, Go{}, map[string]string{
		"go.mod":               "module example.com/root\n\ngo 1.26\n\nreplace example.com/lib => ./lib\n",
		"main.go":              "package main\n\nimport \"github.com/gin-gonic/gin\"\n\nfunc main() { gin.New() }\n",
		"lib/go.mod":           "module example.com/lib\n\ngo 1.26\n",
		"lib/lib.go":           "package lib\n",
		"lib/cmd/tool/main.go": "package main\n\nimport \"github.com/spf13/cobra\"\n\nfunc main() { _ = cobra.Command{} }\n",
		"testdata/x/go.mod":    "module example.com/fixture\n",
	})
	if len(r.Units) != 2 {
		t.Fatalf("units %+v", r.Units)
	}
	root := onlyUnit(t, r, "go:.")
	if len(root.DependsOn) != 1 || root.DependsOn[0].Unit != "go:lib" || root.DependsOn[0].Evidence.Line != 5 {
		t.Fatalf("root depends on %+v", root.DependsOn)
	}
	// The tool's main belongs to the nested module, not the root one.
	if len(root.Entrypoints) != 1 || root.Entrypoints[0].Name != "root" || root.Entrypoints[0].Frameworks[0].Name != "gin" {
		t.Fatalf("root entrypoints %+v", root.Entrypoints)
	}
	lib := onlyUnit(t, r, "go:lib")
	if len(lib.Entrypoints) != 1 || lib.Entrypoints[0].Name != "tool" || lib.Entrypoints[0].Frameworks[0].Kind != discovery.CLI {
		t.Fatalf("lib entrypoints %+v", lib.Entrypoints)
	}
}

func TestRust_autobinsOffAndDeclaredBins(t *testing.T) {
	r := run(t, Rust{}, map[string]string{
		"Cargo.toml":    "[package]\nname = \"svc\"\nedition = \"2024\"\nautobins = false\n\n[[bin]]\nname = \"server\"\npath = \"src/server.rs\"\n\n[dependencies]\nactix-web = \"4\"\n",
		"src/server.rs": "use actix_web::App;\nfn main() {}\n",
		"src/main.rs":   "fn main() {}\n",
		"src/bin/x.rs":  "fn main() {}\n",
	})
	u := onlyUnit(t, r, "rust:.")
	if len(u.Entrypoints) != 1 || u.Entrypoints[0].Name != "server" || u.Entrypoints[0].Path != "src/server.rs" {
		t.Fatalf("entrypoints %+v", u.Entrypoints)
	}
	fw := u.Entrypoints[0].Frameworks
	if len(fw) != 1 || fw[0].Confidence != discovery.Likely || fw[0].Evidence.Path != "src/server.rs" || fw[0].Evidence.Line != 1 {
		t.Fatalf("frameworks %+v", fw)
	}
}

func TestDetectors_noteBrokenManifests(t *testing.T) {
	files := map[string]string{
		"go.mod":       "this is not a go.mod",
		"Cargo.toml":   "[package\nname=",
		"package.json": "{",
		"compose.yaml": "services: [",
	}
	r := &discovery.Report{}
	fsys := fstest.MapFS{}
	names := make([]string, 0, len(files))
	for n, b := range files {
		fsys[n] = &fstest.MapFile{Data: []byte(b)}
		names = append(names, n)
	}
	sort.Strings(names)
	for _, d := range All() {
		if err := d.Detect(context.Background(), discovery.Repo{FS: fsys, Files: names}, r); err != nil {
			t.Fatalf("%s: %v", d.Name(), err)
		}
	}
	if len(r.Units) != 0 || len(r.Notes) != 4 {
		t.Fatalf("units %+v notes %+v", r.Units, r.Notes)
	}
}

func TestRead_skipsOversizedManifests(t *testing.T) {
	fsys := fstest.MapFS{"package.json": {Data: make([]byte, maxManifestBytes+1), Mode: fs.ModePerm}}
	if _, n := read(discovery.Repo{FS: fsys}, "package.json"); n == nil {
		t.Fatal("an oversized manifest was read")
	}
}

func TestDocker_joinsContinuedInstructions(t *testing.T) {
	r := run(t, Docker{}, map[string]string{
		"Dockerfile": "FROM golang:1.26 AS build\n# a comment\nRUN --mount=type=cache,target=/go/pkg/mod \\\n    go build -o /out/api \\\n        ./cmd/api\n\nFROM gcr.io/distroless/static\nEXPOSE 8080/tcp 9090\n",
	})
	if len(r.Images) != 1 {
		t.Fatalf("images %+v", r.Images)
	}
	img := r.Images[0]
	if img.Base != "gcr.io/distroless/static" || len(img.Ports) != 2 || img.Ports[0] != 8080 || img.Ports[1] != 9090 {
		t.Fatalf("image %+v", img)
	}
	if len(img.Builds) != 1 || img.Builds[0].Line != 3 ||
		img.Builds[0].Detail != "RUN --mount=type=cache,target=/go/pkg/mod go build -o /out/api ./cmd/api" {
		t.Fatalf("builds %+v", img.Builds)
	}
}

func TestRust_aBinaryCallingItsOwnLibraryInheritsItsFrameworks(t *testing.T) {
	r := run(t, Rust{}, map[string]string{
		"src-tauri/Cargo.toml":  "[package]\nname = \"demo\"\nedition = \"2021\"\n\n[lib]\nname = \"app_lib\"\n\n[dependencies]\ntauri = \"2\"\n",
		"src-tauri/src/lib.rs":  "pub fn run() { tauri::Builder::default(); }\n",
		"src-tauri/src/main.rs": "fn main() {\n    app_lib::run();\n}\n",
	})
	u := onlyUnit(t, r, "rust:src-tauri")
	fw := u.Entrypoints[0].Frameworks
	if len(fw) != 1 || fw[0].Kind != discovery.Native || fw[0].Confidence != discovery.Likely || fw[0].Evidence.Line != 2 {
		t.Fatalf("frameworks %+v", fw)
	}
}

func TestGo_requirementsOutsideTheCheckoutAreKept(t *testing.T) {
	r := run(t, Go{}, map[string]string{
		"go.mod":     "module example.com/root\n\ngo 1.26\n\nrequire (\n\tgithub.com/acme/kit v1.2.0\n\texample.com/lib v0.0.0\n\tgolang.org/x/sync v0.8.0 // indirect\n)\n\nreplace example.com/lib => ./lib\n",
		"main.go":    "package main\n\nfunc main() {}\n",
		"lib/go.mod": "module example.com/lib\n\ngo 1.26\n",
		"lib/lib.go": "package lib\n",
	})
	root := onlyUnit(t, r, "go:.")
	if len(root.Requires) != 1 {
		t.Fatalf("requires %+v", root.Requires)
	}
	req := root.Requires[0]
	if req.Name != "github.com/acme/kit" || req.Version != "v1.2.0" || req.Evidence.Path != "go.mod" || req.Evidence.Line != 6 {
		t.Fatalf("requirement %+v", req)
	}
}

func TestNode_requirementsOutsideTheCheckoutAreKept(t *testing.T) {
	r := run(t, Node{}, map[string]string{
		"package.json":              `{"name": "root", "private": true, "workspaces": ["packages/*"]}`,
		"packages/ui/package.json":  `{"name": "@acme/ui"}`,
		"packages/app/package.json": "{\n  \"name\": \"app\",\n  \"dependencies\": {\n    \"@acme/ui\": \"workspace:*\",\n    \"@acme/tokens\": \"^2.0.0\"\n  }\n}\n",
	})
	app := onlyUnit(t, r, "node:packages/app")
	if len(app.Requires) != 1 || app.Requires[0].Name != "@acme/tokens" || app.Requires[0].Version != "^2.0.0" || app.Requires[0].Evidence.Line != 5 {
		t.Fatalf("requires %+v", app.Requires)
	}
}

func TestRust_requirementsOutsideTheCheckoutAreKept(t *testing.T) {
	r := run(t, Rust{}, map[string]string{
		"Cargo.toml":             "[workspace]\nmembers = [\"crates/*\"]\n\n[workspace.dependencies]\nkit = { git = \"https://github.com/acme/kit\" }\n",
		"crates/app/Cargo.toml":  "[package]\nname = \"app\"\nversion = \"0.1.0\"\n\n[dependencies]\nkit = { workspace = true }\nserde = \"1\"\ncore = { path = \"../core\" }\n",
		"crates/app/src/main.rs": "fn main() {}\n",
		"crates/core/Cargo.toml": "[package]\nname = \"core\"\nversion = \"0.1.0\"\n",
		"crates/core/src/lib.rs": "",
	})
	app := onlyUnit(t, r, "rust:crates/app")
	got := map[string]string{}
	for _, req := range app.Requires {
		got[req.Name] = req.Version + "|" + req.Git
	}
	if len(got) != 2 || got["kit"] != "|https://github.com/acme/kit" || got["serde"] != "1|" {
		t.Fatalf("requires %+v", app.Requires)
	}
}
