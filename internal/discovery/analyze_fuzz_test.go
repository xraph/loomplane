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

// FuzzAnalyzePythonAndJVM does the same for Python's and the JVM's
// manifests: pyproject.toml, requirements.txt, Pipfile, pom.xml, Gradle
// build and settings scripts, and version catalogs.
func FuzzAnalyzePythonAndJVM(f *testing.F) {
	f.Add("[project]\nname = \"api\"\ndependencies = [\"fastapi[std]>=0.1 ; python_version>'3'\"]\n", "flask==3\n-e ../lib\n",
		"<project><artifactId>a</artifactId><modules><module>b</module></modules></project>",
		"plugins { application }\ndependencies { implementation(libs.x) }\n", "include(\":b\", 'c')\n", "[libraries]\nx = \"g:a:1\"\n")
	f.Add("[tool.uv.workspace]\nmembers = [\"**\"]\n", "git+https://x#egg=y\n--index-url u\n", "<project", "implementation project(':')\n", "include", "[libraries]\nx = {group=\"g\"}\n")
	f.Fuzz(func(t *testing.T, pyproject, requirements, pomXML, gradle, settings, catalog string) {
		fsys := fstest.MapFS{
			"pyproject.toml":                          {Data: []byte(pyproject)},
			"svc/pyproject.toml":                      {Data: []byte(pyproject)},
			"svc/requirements.txt":                    {Data: []byte(requirements)},
			"svc/Pipfile":                             {Data: []byte(pyproject)},
			"svc/Procfile":                            {Data: []byte("web: " + requirements + "\nworker: " + requirements + "\nbeat: x\n")},
			"pom.xml":                                 {Data: []byte(pomXML)},
			"b/pom.xml":                               {Data: []byte(pomXML)},
			"g/build.gradle.kts":                      {Data: []byte(gradle)},
			"g/settings.gradle":                       {Data: []byte(settings)},
			"g/gradle/libs.versions.toml":             {Data: []byte(catalog)},
			"g/src/main/kotlin/Main.kt":               {Data: []byte("fun main() {}\n")},
			"g/build.gradle":                          {Data: []byte("subprojects {\n" + gradle + "\n}\n")},
			"g/buildSrc/src/main/kotlin/c.gradle.kts": {Data: []byte(gradle)},
			"svc/app/tasks.py":                        {Data: []byte("app = Celery(" + requirements + ")\n")},
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = Analyze(ctx, fsys)
		if ctx.Err() != nil {
			t.Fatal("analysis took more than five seconds on a small repository")
		}
	})
}
