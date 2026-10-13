package manifest

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

const example = `version: 1
defaults:
  provider: cloud-run
  region: us-central1
services:
  api:
    source: services/api
    port: 8080
    scale: {min: 1, max: 4}
    size: {cpu: 1, memory: 512}
    needs: [db, cache]
    environments:
      preview:
        scale: {min: 0, max: 1}
      staging:
        provider: render
  web:
    source: apps/web
    provider: vercel
    needs: [api]
  backfill:
    source: rust:crates/ingest/src/bin/backfill.rs
    kind: worker
    provider: ecs-fargate
    region: eu-west-1
    enabled: false
resources:
  db:
    kind: postgres
    provider: cloud-sql
    version: "17"
  cache:
    kind: redis
    provider: memorystore
    environments:
      preview:
        enabled: false
ignore:
  resources: [mysql]
`

func TestParse_example(t *testing.T) {
	m, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.ServiceNames(); !slices.Equal(got, []string{"api", "backfill", "web"}) {
		t.Errorf("services = %v", got)
	}
	if m.Services["api"].Size.CPU != 1 || m.Resources["db"].Version != "17" || !m.Ignored("mysql") {
		t.Errorf("parsed = %+v", m)
	}
	dotted, err := Parse([]byte("version: 1\nresources:\n  db: {kind: mysql, version: '8.4'}"))
	if err != nil || dotted.Resources["db"].Version != "8.4" {
		t.Errorf("a dotted version: %v", err)
	}
}

func TestParse_rejects(t *testing.T) {
	tests := []struct {
		name, yaml, want string
	}{
		{"empty", "  \n", "it's empty"},
		{"no version", "services: {}", "version: is missing"},
		{"later version", "version: 2", "version: is 2; this Loomplane reads version 1"},
		{"version as text", "version: one", "version: must be the number 1"},
		{"unknown field", "version: 1\nservice: {}", "field service not found"},
		{"unknown nested field", "version: 1\nservices:\n  api: {source: x, replicas: 3}", "field replicas not found"},
		{"two documents", "version: 1\n---\nversion: 1", "more than one YAML document"},
		{"bad yaml", "version: 1\nservices: [", "did not find expected node content"},
		{"source missing", "version: 1\nservices:\n  api: {provider: vercel}", "services.api.source: is required"},
		{"source escapes", "version: 1\nservices:\n  api: {source: ../other}", "isn't a path in the repository"},
		{"absolute source", "version: 1\nservices:\n  api: {source: /etc}", "isn't a path in the repository"},
		{"context escapes", "version: 1\nservices:\n  api: {source: x, build: {context: a/../..}}", "services.api.build.context: \"a/../..\" isn't a directory"},
		{"absolute context", "version: 1\nservices:\n  api: {source: x, build: {context: /srv}}", "isn't a directory in the repository"},
		{"unknown build field", "version: 1\nservices:\n  api: {source: x, build: {target: prod}}", "target"},
		{"bad name", "version: 1\nservices:\n  API: {source: x}", "services.API: the name must be"},
		{"bad kind", "version: 1\nservices:\n  api: {source: x, kind: lambda}", "\"lambda\" isn't a service kind"},
		{"unknown need", "version: 1\nservices:\n  api: {source: x, needs: [db]}", "\"db\" isn't a service or resource"},
		{"gateway without routes", "version: 1\nservices:\n  web: {source: x}\ngateways:\n  edge: {routes: []}", "needs at least one route"},
		{"gateway to nothing", "version: 1\nservices:\n  web: {source: x}\ngateways:\n  edge: {routes: [{path: /, to: api}]}", "\"api\" isn't a service"},
		{"gateway path", "version: 1\nservices:\n  web: {source: x}\ngateways:\n  edge: {routes: [{path: api, to: web}]}", "isn't a path prefix"},
		{"gateway path twice", "version: 1\nservices:\n  web: {source: x}\ngateways:\n  edge: {routes: [{path: /a, to: web}, {path: /a/, to: web}]}", "is routed twice"},
		{"gateway named like a service", "version: 1\nservices:\n  web: {source: x}\ngateways:\n  web: {routes: [{path: /, to: web}]}", "they'd share an address"},
		{"needs itself", "version: 1\nservices:\n  api: {source: x, needs: [api]}", "can't need itself"},
		{"need twice", "version: 1\nservices:\n  a: {source: x, needs: [b, b]}\n  b: {source: y}", "\"b\" is listed twice"},
		{"cycle", "version: 1\nservices:\n  a: {source: x, needs: [b]}\n  b: {source: y, needs: [a]}", "a → b → a"},
		{"scale inverted", "version: 1\nservices:\n  api: {source: x, scale: {min: 3, max: 2}}", "min (3) is more than max (2)"},
		{"too big", "version: 1\nservices:\n  api: {source: x, size: {cpu: 64, memory: 512}}", "size.cpu: must be more than 0"},
		{"too little memory", "version: 1\nservices:\n  api: {source: x, size: {cpu: 1, memory: 64}}", "size.memory: must be between"},
		{"bad port", "version: 1\nservices:\n  api: {source: x, port: 70000}", "port: must be between"},
		{"resource kind missing", "version: 1\nresources:\n  db: {provider: rds}", "resources.db.kind: is required"},
		{"name clash", "version: 1\nservices:\n  db: {source: x}\nresources:\n  db: {kind: postgres}", "a service has the same name"},
		{"bad provider", "version: 1\ndefaults: {provider: Cloud Run}", "isn't a provider name"},
		{"bad environment", "version: 1\nservices:\n  api:\n    source: x\n    environments:\n      Prod: {}", "environments.Prod: isn't an environment name"},
		{"bad ignore", "version: 1\nignore: {resources: [My SQL]}", "ignore.resources[0]"},
		{"bad version", "version: 1\nresources:\n  db: {kind: mysql, version: 8 4}", "isn't a version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml))
			if err == nil {
				t.Fatal("parsed")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("%v doesn't wrap ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestParse_tooLarge(t *testing.T) {
	big := "version: 1\n# " + strings.Repeat("x", MaxSize)
	if _, err := Parse([]byte(big)); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("err = %v", err)
	}
}

func TestService_For(t *testing.T) {
	m, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, service, env string
		provider, region   string
		scaleMax           int
		on                 bool
	}{
		{"defaults fill in", "api", "production", "cloud-run", "us-central1", 4, true},
		{"environment scale", "api", "preview", "cloud-run", "us-central1", 1, true},
		{"another provider drops the region", "api", "staging", "render", "", 4, true},
		{"own provider skips the default region", "web", "production", "vercel", "", 0, true},
		{"own region kept", "backfill", "production", "ecs-fargate", "eu-west-1", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := m.Services[tt.service].For(tt.env, m.Defaults)
			if s.Provider != tt.provider || s.Region != tt.region {
				t.Errorf("placed on %s/%s, want %s/%s", s.Provider, s.Region, tt.provider, tt.region)
			}
			if got := 0; s.Scale != nil {
				got = s.Scale.Max
				if got != tt.scaleMax {
					t.Errorf("scale max = %d, want %d", got, tt.scaleMax)
				}
			} else if tt.scaleMax != 0 {
				t.Errorf("no scale, want max %d", tt.scaleMax)
			}
			if On(s.Enabled) != tt.on {
				t.Errorf("on = %v", On(s.Enabled))
			}
		})
	}

	cache := m.Resources["cache"].For("preview", m.Defaults)
	if On(cache.Enabled) || cache.Provider != "memorystore" || cache.Region != "" {
		t.Errorf("cache in preview = %+v", cache)
	}
	db := m.Resources["db"].For("production", m.Defaults)
	if db.Provider != "cloud-sql" || db.Region != "" {
		t.Errorf("db = %+v", db)
	}
}

func TestMarshal_roundTrip(t *testing.T) {
	m, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(out)
	if err != nil {
		t.Fatalf("reparse: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(m, again) {
		t.Errorf("round trip changed the manifest:\n%s", out)
	}
	twice, err := Marshal(again)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(twice) {
		t.Error("Marshal isn't stable")
	}
	// The embedded settings sit beside source, not under a key of their own.
	if strings.Contains(string(out), "settings") {
		t.Errorf("settings weren't inlined:\n%s", out)
	}
}

func TestMarshal_JSONFlattensSettings(t *testing.T) {
	m, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(m.Services["api"])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["port"]; !ok {
		t.Errorf("port isn't a field of its own: %s", data)
	}
}

// The schema is written by hand, so check it names exactly the fields the
// Go types read: a field added to one and not the other fails here.
func TestSchema_matchesTypes(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(Schema, &schema); err != nil {
		t.Fatalf("schema.json isn't JSON: %v", err)
	}
	if schema["$id"] != SchemaID {
		t.Errorf("$id = %v, want %s", schema["$id"], SchemaID)
	}
	defs := schema["$defs"].(map[string]any)
	props := func(node any) []string {
		p, _ := node.(map[string]any)["properties"].(map[string]any)
		keys := make([]string, 0, len(p))
		for k := range p {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}
	envOverride := defs["resource"].(map[string]any)["properties"].(map[string]any)["environments"].(map[string]any)["additionalProperties"]
	tests := []struct {
		name   string
		schema []string
		typ    any
	}{
		{"manifest", props(schema), Manifest{}},
		{"defaults", props(schema["properties"].(map[string]any)["defaults"]), Defaults{}},
		{"ignore", props(schema["properties"].(map[string]any)["ignore"]), Ignore{}},
		{"service", append(props(defs["service"]), props(defs["settings"])...), Service{}},
		{"build", props(defs["service"].(map[string]any)["properties"].(map[string]any)["build"]), Build{}},
		{"settings", props(defs["settings"]), Settings{}},
		{"scale", props(defs["settings"].(map[string]any)["properties"].(map[string]any)["scale"]), Scale{}},
		{"size", props(defs["settings"].(map[string]any)["properties"].(map[string]any)["size"]), Size{}},
		{"resource", props(defs["resource"]), Resource{}},
		{"resource settings", props(envOverride), ResourceSettings{}},
		{"gateway", props(defs["gateway"]), Gateway{}},
		{"route", props(defs["route"]), Route{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := yamlFields(reflect.TypeOf(tt.typ))
			got := slices.Clone(tt.schema)
			sort.Strings(got)
			if !slices.Equal(got, want) {
				t.Errorf("schema has %v, the type reads %v", got, want)
			}
		})
	}
}

func yamlFields(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if strings.Contains(opts, "inline") {
			out = append(out, yamlFields(f.Type)...)
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func TestParse_reportsEachYAMLErrorOnItsOwn(t *testing.T) {
	_, err := Parse([]byte("version: 1\nservices:\n  api:\n    source: a\nservices:\n  web:\n    source: b\nresources: {}\nresources: {}\n"))
	var ps Problems
	if !errors.As(err, &ps) || len(ps) != 2 {
		t.Fatalf("got %v", err)
	}
	for _, p := range ps {
		if !strings.HasPrefix(p.Message, "line ") {
			t.Errorf("problem %q", p.Message)
		}
	}
}

func TestParse_repo(t *testing.T) {
	m, err := Parse([]byte("version: 1\nservices:\n  site:\n    repo: web\n    source: apps/site\n"))
	if err != nil || m.Services["site"].Repo != "web" {
		t.Fatalf("Parse: %v, %+v", err, m)
	}
	_, err = Parse([]byte("version: 1\nservices:\n  site:\n    repo: Web\n    source: apps/site\n"))
	var ps Problems
	if !errors.As(err, &ps) || len(ps) != 1 || ps[0].Path != "services.site.repo" {
		t.Fatalf("Parse with a bad repo: %v", err)
	}
}
