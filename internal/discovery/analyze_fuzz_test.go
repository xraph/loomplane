package discovery

import (
	"context"
	"testing"
	"testing/fstest"
	"time"
)

// FuzzAnalyze reads repositories with any go.mod, package.json,
// Cargo.toml, Dockerfile and compose file: analysis reads what anyone
// pushes, so none of it may crash or hang the worker.
func FuzzAnalyze(f *testing.F) {
	f.Add("module example.com/x\n\ngo 1.26\n", `{"name":"web","dependencies":{"next":"15"}}`, "[package]\nname = \"x\"\n", "FROM golang:1.26\nEXPOSE 8080\n", "services:\n  db:\n    image: postgres:17\n")
	f.Add("module\n", `{"workspaces":["**"]}`, "[workspace]\nmembers = [\"*\"]\n", "FROM scratch AS a\nCOPY --from=a / /\n", "services: [")
	f.Fuzz(func(t *testing.T, gomod, pkg, cargo, dockerfile, compose string) {
		fsys := fstest.MapFS{
			"go.mod":                  {Data: []byte(gomod)},
			"main.go":                 {Data: []byte("package main\n\nfunc main() {}\n")},
			"package.json":            {Data: []byte(pkg)},
			"apps/web/package.json":   {Data: []byte(pkg)},
			"Cargo.toml":              {Data: []byte(cargo)},
			"src/main.rs":             {Data: []byte("fn main() {}\n")},
			"Dockerfile":              {Data: []byte(dockerfile)},
			"services/api/Dockerfile": {Data: []byte(dockerfile)},
			"docker-compose.yml":      {Data: []byte(compose)},
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = Analyze(ctx, fsys)
		if ctx.Err() != nil {
			t.Fatal("analysis took more than five seconds on a small repository")
		}
	})
}
