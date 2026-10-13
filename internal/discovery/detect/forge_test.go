package detect

import (
	"context"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xraph/loomplane/pkg/discovery"
)

func forgeFixture(t *testing.T, files map[string]string) (*discovery.Report, discovery.Repo) {
	t.Helper()
	fsys := fstest.MapFS{}
	names := make([]string, 0, len(files))
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
		names = append(names, name)
	}
	sort.Strings(names)
	repo := discovery.Repo{FS: fsys, Files: names}
	report := &discovery.Report{}
	for _, detector := range []discovery.Detector{Go{}, Forge{}} {
		if err := detector.Detect(context.Background(), repo, report); err != nil {
			t.Fatal(err)
		}
	}
	return report, repo
}

func TestForgeAppManifestRoles(t *testing.T) {
	for _, role := range []string{"worker", "web", "cli"} {
		t.Run(role, func(t *testing.T) {
			r, _ := forgeFixture(t, map[string]string{
				"go.mod":             "module example.com/project\ngo 1.26\n",
				"cmd/job/main.go":    "package main\nfunc main() {}\n",
				"cmd/job/.forge.yml": "app:\n  name: background\n  type: " + role + "\n",
			})
			ep := onlyUnit(t, r, "go:.").Entrypoints[0]
			if ep.Name != "background" {
				t.Fatalf("name = %s", ep.Name)
			}
			want := map[string]discovery.FrameworkKind{"worker": discovery.FrameworkKind("worker"), "web": discovery.HTTP, "cli": discovery.CLI}[role]
			if len(ep.Frameworks) != 1 || ep.Frameworks[0].Kind != want || ep.Frameworks[0].Confidence != discovery.Certain {
				t.Fatalf("frameworks = %+v", ep.Frameworks)
			}
			if len(ep.Evidence) < 2 || ep.Evidence[len(ep.Evidence)-1].Path != "cmd/job/.forge.yml" {
				t.Fatalf("evidence = %+v", ep.Evidence)
			}
		})
	}
}

func TestForgeProjectTargetsAndWorkspace(t *testing.T) {
	r, _ := forgeFixture(t, map[string]string{
		"go.mod":                  "module example.com/project\ngo 1.26\n",
		".forge.yaml":             "project:\n  name: product\n  layout: single-module\n  structure:\n    cmd: services\n    apps: applications\nbuild:\n  apps:\n    - name: demo-server\n      cmd: examples/server/main.go\n",
		"services/api/main.go":    "package main\nfunc main() {}\n",
		"examples/server/main.go": "package main\nfunc main() {}\n",
	})
	u := onlyUnit(t, r, "go:.")
	if len(r.BuildSystems) != 1 || r.BuildSystems[0].Name != "forge" || len(r.Workspaces) != 1 || r.Workspaces[0].Tool != "forge" {
		t.Fatalf("metadata = %+v %+v", r.BuildSystems, r.Workspaces)
	}
	found := false
	for _, ep := range u.Entrypoints {
		if ep.Path == "examples/server" {
			found = ep.Name == "demo-server" && discovery.ExplicitTarget(ep.Evidence)
		}
	}
	if !found {
		t.Fatalf("missing declared target: %+v", u.Entrypoints)
	}
}

func TestForgeMultiModuleGlobs(t *testing.T) {
	r, _ := forgeFixture(t, map[string]string{
		".forge.yml":              "project:\n  name: product\n  layout: multi-module\n  workspace:\n    apps: ./products/*\n    services: ./backends/*\n",
		"products/ui/go.mod":      "module example.com/ui\ngo 1.26\n",
		"products/ui/cmd/main.go": "package main\nfunc main() {}\n",
		"unrelated/go.mod":        "module example.com/other\ngo 1.26\n",
	})
	if len(r.Workspaces) != 1 || len(r.Workspaces[0].Members) != 1 || r.Workspaces[0].Members[0] != "products/ui" {
		t.Fatalf("workspaces = %+v", r.Workspaces)
	}
}

func TestForgeConfigPrecedenceAndSecrets(t *testing.T) {
	r, repo := forgeFixture(t, map[string]string{
		"go.mod":              "module example.com/project\ngo 1.26\n",
		"cmd/api/main.go":     "package main\nfunc main() {}\n",
		"config/api.yaml":     "extensions:\n  cache: {driver: inmemory}\n  grove:\n    databases:\n      - {name: primary, driver: postgres, dsn: 'postgres://user:secret@localhost/db'}\n",
		"cmd/api/config.yaml": "extensions:\n  cache: {driver: redis}\n  trove: {storage_driver: file}\n",
		"config.yaml":         "extensions:\n  cache: {driver: redis}\n  queue: {driver: rabbitmq}\n",
	})
	ep := &r.Units[0].Entrypoints[0]
	if err := applyForgeConfig(context.Background(), repo, r, ep, ep.Path, map[string]bool{"cache": true, "grove": true, "trove": true}); err != nil {
		t.Fatal(err)
	}
	if len(ep.Resources) != 1 || ep.Resources[0].Kind != "postgres" || ep.Resources[0].Evidence.Line != 5 {
		t.Fatalf("resources = %+v", ep.Resources)
	}
	if strings.Contains(ep.Resources[0].Evidence.Detail, "secret") {
		t.Fatalf("secret leaked: %+v", ep.Resources)
	}
}

func TestForgeMalformedConfigIsSafe(t *testing.T) {
	r, repo := forgeFixture(t, map[string]string{"go.mod": "module example.com/project\ngo 1.26\n", "cmd/api/main.go": "package main\nfunc main() {}", "config/api.yaml": "extensions: [secret: ["})
	ep := &r.Units[0].Entrypoints[0]
	if err := applyForgeConfig(context.Background(), repo, r, ep, ep.Path, map[string]bool{"cache": true}); err != nil {
		t.Fatal(err)
	}
	if len(r.Notes) == 0 {
		t.Fatal("missing malformed config note")
	}
}

func TestForgeDeclaredNameScopesConfigBeforeMetadata(t *testing.T) {
	r, repo := forgeFixture(t, map[string]string{
		"go.mod":              "module example.com/project\ngo 1.26\n",
		"cmd/api/main.go":     "package main\nfunc main() {}",
		"cmd/api/.forge.yaml": "app:\n  name: service\n  type: web\n",
		"config/service.yaml": "extensions:\n  cache: {driver: inmemory}\n",
		"config.yaml":         "extensions:\n  cache: {driver: redis}\n",
	})
	ep := &r.Units[0].Entrypoints[0]
	ep.Name = "api"
	if err := applyForgeConfig(context.Background(), repo, r, ep, ep.Path, map[string]bool{"cache": true}); err != nil {
		t.Fatal(err)
	}
	if len(ep.Resources) != 0 {
		t.Fatalf("root default bypassed app name override: %+v", ep.Resources)
	}
}

func TestForgeCustomStructureDeclaresOnlySelectedTargets(t *testing.T) {
	r, _ := forgeFixture(t, map[string]string{
		"go.mod":                      "module example.com/project\ngo 1.26\n",
		".forge.yaml":                 "project:\n  structure:\n    cmd: scripts\n    apps: examples\n",
		"scripts/api/main.go":         "package main\nfunc main() {}",
		"examples/worker/cmd/main.go": "package main\nfunc main() {}",
		"experiments/other/main.go":   "package main\nfunc main() {}",
	})
	for _, ep := range r.Units[0].Entrypoints {
		want := ep.Path != "experiments/other"
		if discovery.ExplicitTarget(ep.Evidence) != want {
			t.Fatalf("target %s explicit=%v, want %v", ep.Path, discovery.ExplicitTarget(ep.Evidence), want)
		}
	}
}

func TestForgeModuleBuildTargetAndConfigName(t *testing.T) {
	r, repo := forgeFixture(t, map[string]string{
		".forge.yaml":              "project:\n  layout: multi-module\n  workspace: {apps: 'services/*'}\nbuild:\n  apps:\n    - name: public-api\n      module: services/api\n",
		"services/api/go.mod":      "module example.com/api\ngo 1.26\n",
		"services/api/cmd/main.go": "package main\nfunc main() {}",
		"config/public-api.yaml":   "extensions:\n  cache: {driver: inmemory}\n",
		"config.yaml":              "extensions:\n  cache: {driver: redis}\n",
	})
	ep := &r.Units[0].Entrypoints[0]
	if ep.Name != "public-api" {
		t.Fatalf("build target name = %s", ep.Name)
	}
	ep.Name = "cmd"
	if err := applyForgeConfig(context.Background(), repo, r, ep, ep.Path, map[string]bool{"cache": true}); err != nil {
		t.Fatal(err)
	}
	if len(ep.Resources) != 0 {
		t.Fatalf("build target scoped config lost: %+v", ep.Resources)
	}
}

func TestForgeInvalidManifestShapes(t *testing.T) {
	for _, body := range []string{"project: secret", "app: secret"} {
		r, _ := forgeFixture(t, map[string]string{".forge.yaml": body, "go.mod": "module example.com/api\ngo 1.26\n", "main.go": "package main\nfunc main() {}"})
		if len(r.Notes) != 1 || strings.Contains(r.Notes[0].Message, "secret") {
			t.Fatalf("notes = %+v", r.Notes)
		}
	}
}

func TestForgeUnregisteredCommandDoesNotReadConfig(t *testing.T) {
	r, repo := forgeFixture(t, map[string]string{
		"go.mod":      "module example.com/api\ngo 1.26\n",
		"main.go":     "package main\nfunc main() {}",
		"config.yaml": "extensions: [secret: [",
	})
	ep := &r.Units[0].Entrypoints[0]
	if err := applyForgeConfig(context.Background(), repo, r, ep, ep.Path, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if len(r.Notes) != 0 {
		t.Fatalf("unregistered command read unrelated config: %+v", r.Notes)
	}
}

func TestForgeSearchUsesSharedResourceKind(t *testing.T) {
	r, repo := forgeFixture(t, map[string]string{
		"go.mod":      "module example.com/api\ngo 1.26\n",
		"main.go":     "package main\nfunc main() {}",
		"config.yaml": "extensions:\n  search: {driver: elasticsearch}\n",
	})
	ep := &r.Units[0].Entrypoints[0]
	if err := applyForgeConfig(context.Background(), repo, r, ep, ep.Path, map[string]bool{"search": true}); err != nil {
		t.Fatal(err)
	}
	if len(ep.Resources) != 1 || ep.Resources[0].Kind != "search" {
		t.Fatalf("resources = %+v", ep.Resources)
	}
}

func TestForgeModuleAppConfigOverridesSharedDefault(t *testing.T) {
	for _, manifest := range []bool{false, true} {
		name := "module fallback"
		if manifest {
			name = "declared app"
		}
		t.Run(name, func(t *testing.T) {
			files := map[string]string{
				".forge.yaml":                 "project:\n  layout: multi-module\n  workspace: {apps: 'services/*'}\n",
				"config.yaml":                 "extensions:\n  cache: {driver: redis}\n",
				"services/worker/go.mod":      "module example.com/worker\ngo 1.26\n",
				"services/worker/config.yaml": "extensions:\n  cache: {driver: inmemory}\n",
				"services/worker/cmd/main.go": "package main\nimport \"github.com/xraph/forge/extensions/cache\"\nfunc main(){cache.New()}\n",
			}
			if manifest {
				files["services/worker/.forge.yaml"] = "app:\n  name: worker\n  type: worker\n"
			}
			report, _ := forgeFixture(t, files)
			ep := report.Units[0].Entrypoints[0]
			if len(ep.Resources) != 0 {
				t.Fatalf("module app config override ignored: %+v", ep.Resources)
			}
		})
	}
}

func TestForgeNamedConfigPrecedesDeclaredAppConfig(t *testing.T) {
	report, _ := forgeFixture(t, map[string]string{
		".forge.yaml":                 "project:\n  layout: multi-module\n  workspace: {apps: 'services/*'}\n",
		"config/worker.yaml":          "extensions:\n  cache: {driver: redis}\n",
		"services/worker/go.mod":      "module example.com/worker\ngo 1.26\n",
		"services/worker/.forge.yaml": "app:\n  name: worker\n  type: worker\n",
		"services/worker/config.yaml": "extensions:\n  cache: {driver: inmemory}\n",
		"services/worker/cmd/main.go": "package main\nimport \"github.com/xraph/forge/extensions/cache\"\nfunc main(){cache.New()}\n",
	})
	resources := report.Units[0].Entrypoints[0].Resources
	if len(resources) != 1 || resources[0].Kind != "redis" || resources[0].Evidence.Path != "config/worker.yaml" {
		t.Fatalf("named config precedence = %+v", resources)
	}
}

func TestForgeConfigInfersSafeDSNSchemes(t *testing.T) {
	for _, backend := range []struct{ name, key, value, kind string }{
		{"grove", "dsn", "postgres://user:secret@db/app", "postgres"},
		{"grove_kv", "dsn", "redis://:secret@cache/0", "redis"},
		{"cache", "url", "redis://:secret@cache/0", "redis"},
		{"queue", "url", "nats://user:secret@broker", "nats"},
	} {
		t.Run(backend.name, func(t *testing.T) {
			report, repo := forgeFixture(t, map[string]string{"go.mod": "module example.com/app\ngo 1.26\n", "main.go": "package main\nfunc main() {}", "config.yaml": "extensions:\n  " + backend.name + ": {" + backend.key + ": '" + backend.value + "'}\n"})
			ep := &report.Units[0].Entrypoints[0]
			if err := applyForgeConfig(context.Background(), repo, report, ep, ep.Path, map[string]bool{backend.name: true}); err != nil {
				t.Fatal(err)
			}
			if len(ep.Resources) != 1 || ep.Resources[0].Kind != backend.kind {
				t.Fatalf("resources = %+v", ep.Resources)
			}
			if strings.Contains(ep.Resources[0].Evidence.Detail, "secret") || strings.Contains(ep.Resources[0].Evidence.Detail, "://") {
				t.Fatalf("sensitive evidence = %+v", ep.Resources)
			}
		})
	}
}

func TestForgeExplicitDriverPrecedesDSN(t *testing.T) {
	for _, driver := range []string{"inmemory", "unrecognized"} {
		t.Run(driver, func(t *testing.T) {
			report, repo := forgeFixture(t, map[string]string{"go.mod": "module example.com/app\ngo 1.26\n", "main.go": "package main\nfunc main() {}", "config.yaml": "extensions:\n  cache: {driver: " + driver + ", url: 'redis://:secret@cache'}\n"})
			ep := &report.Units[0].Entrypoints[0]
			if err := applyForgeConfig(context.Background(), repo, report, ep, ep.Path, map[string]bool{"cache": true}); err != nil {
				t.Fatal(err)
			}
			if len(ep.Resources) != 0 {
				t.Fatalf("explicit driver overridden: %+v", ep.Resources)
			}
			if driver == "unrecognized" && len(report.Notes) != 1 {
				t.Fatalf("unknown driver note missing: %+v", report.Notes)
			}
		})
	}
}

func TestForgeDisabledBackendsDoNotBecomeResources(t *testing.T) {
	for _, config := range []string{
		"cache: {driver: redis, enabled: FALSE}",
		"events:\n    brokers:\n      - {name: disabled, type: nats, enabled: false}\n      - {name: local, type: memory, enabled: true}",
		"grove:\n    databases:\n      - {name: ignored, driver: postgres, enabled: false}\n      - {name: local, driver: sqlite}",
	} {
		report, repo := forgeFixture(t, map[string]string{"go.mod": "module example.com/app\ngo 1.26\n", "main.go": "package main\nfunc main() {}", "config.yaml": "extensions:\n  " + config + "\n"})
		ep := &report.Units[0].Entrypoints[0]
		if err := applyForgeConfig(context.Background(), repo, report, ep, ep.Path, map[string]bool{"cache": true, "events": true, "grove": true}); err != nil {
			t.Fatal(err)
		}
		if len(ep.Resources) != 0 {
			t.Fatalf("disabled backend inferred from %q: %+v", config, ep.Resources)
		}
	}
}

func TestForgeDynamicConfigReportsUnresolvedWithoutValues(t *testing.T) {
	for _, value := range []string{"null", "''", "'${SECRET_URL}'"} {
		report, repo := forgeFixture(t, map[string]string{"go.mod": "module example.com/app\ngo 1.26\n", "main.go": "package main\nfunc main() {}", "config.yaml": "extensions:\n  cache: {url: " + value + "}\n"})
		ep := &report.Units[0].Entrypoints[0]
		if err := applyForgeConfig(context.Background(), repo, report, ep, ep.Path, map[string]bool{"cache": true}); err != nil {
			t.Fatal(err)
		}
		if len(ep.Resources) != 0 || len(report.Notes) != 1 || strings.Contains(report.Notes[0].Message, "SECRET") {
			t.Fatalf("dynamic config result resources=%+v notes=%+v", ep.Resources, report.Notes)
		}
	}
}

func TestForgeNamedBackendsRequireNames(t *testing.T) {
	for _, backend := range []struct{ extension, list, driverKey, driver string }{
		{"grove", "databases", "driver", "postgres"},
		{"grove_kv", "stores", "driver", "redis"},
		{"trove", "stores", "storage_driver", "s3"},
		{"events", "brokers", "type", "nats"},
	} {
		t.Run(backend.extension, func(t *testing.T) {
			report, repo := forgeFixture(t, map[string]string{"go.mod": "module example.com/app\ngo 1.26\n", "main.go": "package main\nfunc main() {}", "config.yaml": "extensions:\n  " + backend.extension + ":\n    " + backend.list + ":\n      - {" + backend.driverKey + ": " + backend.driver + "}\n"})
			ep := &report.Units[0].Entrypoints[0]
			if err := applyForgeConfig(context.Background(), repo, report, ep, ep.Path, map[string]bool{backend.extension: true}); err != nil {
				t.Fatal(err)
			}
			if len(ep.Resources) != 0 || len(report.Notes) != 1 {
				t.Fatalf("missing instance name resources=%+v notes=%+v", ep.Resources, report.Notes)
			}
		})
	}
}

func TestForgeYAMLAliasesAndMergePrecedence(t *testing.T) {
	for _, config := range []struct{ name, body, kind string }{
		{"alias", "defaults: &backend {driver: redis}\nextensions:\n  cache: *backend\n", "redis"},
		{"merged driver", "defaults: &backend {driver: redis}\nextensions:\n  cache: {<<: *backend}\n", "redis"},
		{"explicit embedded override", "defaults: &backend {driver: redis}\nextensions:\n  cache: {<<: *backend, driver: inmemory}\n", ""},
		{"merge sequence priority", "local: &local {driver: inmemory}\nremote: &remote {driver: redis}\nextensions:\n  cache: {<<: [*local, *remote]}\n", ""},
		{"named instance alias", "defaults: &backend {name: primary, driver: postgres}\nextensions:\n  grove: {databases: [*backend]}\n", "postgres"},
	} {
		t.Run(config.name, func(t *testing.T) {
			report, repo := forgeFixture(t, map[string]string{"go.mod": "module example.com/app\ngo 1.26\n", "main.go": "package main\nfunc main() {}", "config.yaml": config.body})
			ep := &report.Units[0].Entrypoints[0]
			if err := applyForgeConfig(context.Background(), repo, report, ep, ep.Path, map[string]bool{"cache": true, "grove": true}); err != nil {
				t.Fatal(err)
			}
			if config.kind == "" {
				if len(ep.Resources) != 0 {
					t.Fatalf("embedded override lost: %+v", ep.Resources)
				}
			} else if len(ep.Resources) != 1 || ep.Resources[0].Kind != config.kind {
				t.Fatalf("alias resources=%+v notes=%+v", ep.Resources, report.Notes)
			}
			if len(report.Notes) != 0 {
				t.Fatalf("alias notes=%+v", report.Notes)
			}
		})
	}
}

func TestForgeCyclicYAMLNotesDoNotExposeValues(t *testing.T) {
	report, repo := forgeFixture(t, map[string]string{
		"go.mod":      "module example.com/app\ngo 1.26\n",
		"main.go":     "package main\nfunc main() {}",
		"config.yaml": "extensions: &loop\n  cache: *loop\n  secret: 'redis://:password@cache'\n",
	})
	ep := &report.Units[0].Entrypoints[0]
	if err := applyForgeConfig(context.Background(), repo, report, ep, ep.Path, map[string]bool{"cache": true}); err != nil {
		t.Fatal(err)
	}
	if len(ep.Resources) != 0 || len(report.Notes) != 1 {
		t.Fatalf("cyclic YAML resources=%+v notes=%+v", ep.Resources, report.Notes)
	}
	if strings.Contains(report.Notes[0].Message, "password") || strings.Contains(report.Notes[0].Message, "://") {
		t.Fatalf("sensitive YAML note: %+v", report.Notes)
	}
}
