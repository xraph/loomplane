package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
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
		{"later version", "version: 3", "version: is 3; this Loomplane reads version 1"},
		{"version as text", "version: one", "version: must be the number 1"},
		{"unknown field", "version: 1\nservice: {}", "field service not found"},
		{"unknown nested field", "version: 1\nservices:\n  api: {source: x, replicas: 3}", "field replicas not found"},
		{"two documents", "version: 1\n---\nversion: 1", "more than one YAML document"},
		{"bad yaml", "version: 1\nservices: [", "did not find expected node content"},
		{"source missing", "version: 1\nservices:\n  api: {provider: vercel}", "services.api.source: is required"},
		{"source escapes", "version: 1\nservices:\n  api: {source: ../other}", "isn't a path in the repository"},
		{"absolute source", "version: 1\nservices:\n  api: {source: /etc}", "isn't a path in the repository"},
		{"link to one cloud", "version: 1\nlinks:\n  vpn: {between: [gcp, gcp]}", "links.vpn.between: names two different clouds"},
		{"link to an unknown pair", "version: 1\nlinks:\n  vpn: {between: [gcp, azure]}", "can't link azure and gcp"},
		{"two links for one pair", "version: 1\nlinks:\n  a: {between: [gcp, aws]}\n  b: {between: [aws, gcp]}", "a already links aws and gcp"},
		{"link named like a service", "version: 1\nservices:\n  vpn: {source: x}\nlinks:\n  vpn: {between: [gcp, aws]}", "links.vpn: a service, resource or gateway has the same name"},
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
		{"bad held same_address", "version: 1\nheld: {same_address: allow}", "held.same_address: \"allow\" isn't a choice; use block or warn"},
		{"unknown held field", "version: 1\nheld: {address: warn}", "field address not found"},
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
		{"federation", props(defs["federation"]), Federation{}},
		{"destinationRef", props(defs["destinationRef"]), DestinationRef{}},
		{"federationLink", props(defs["federationLink"]), FederationLink{}},
		{"federationExport", props(defs["federationExport"]), FederationExport{}},
		{"federationGrant", props(defs["federationGrant"]), FederationGrant{}},
		{"federationPlacement", props(defs["federationPlacement"]), FederationPlacement{}},
		{"manifest", props(schema), Manifest{}},
		{"defaults", props(schema["properties"].(map[string]any)["defaults"]), Defaults{}},
		{"ignore", props(schema["properties"].(map[string]any)["ignore"]), Ignore{}},
		{"held", props(schema["properties"].(map[string]any)["held"]), Held{}},
		{"service", append(props(defs["service"]), props(defs["settings"])...), Service{}},
		{"build", props(defs["build"]), Build{}},
		{"endpoint", props(defs["endpoint"]), Endpoint{}},
		{"farp", props(defs["farp"]), Farp{}},
		{"settings", props(defs["settings"]), Settings{}},
		{"scale", props(defs["settings"].(map[string]any)["properties"].(map[string]any)["scale"]), Scale{}},
		{"size", props(defs["settings"].(map[string]any)["properties"].(map[string]any)["size"]), Size{}},
		{"resource", props(defs["resource"]), Resource{}},
		{"use", props(defs["use"]), Use{}},
		{"gpu", props(defs["gpu"]), GPU{}},
		{"resource settings", props(envOverride), ResourceSettings{}},
		{"gateway", props(defs["gateway"]), Gateway{}},
		{"database gateway", props(schema["properties"].(map[string]any)["database_gateway"]), DatabaseGateway{}},
		{"link", props(defs["link"]), Link{}},
		{"link settings", props(defs["link"].(map[string]any)["properties"].(map[string]any)["environments"].(map[string]any)["additionalProperties"]), LinkSettings{}},
		{"route", props(defs["route"]), Route{}},
		{"environment", props(defs["environment"]), Environment{}},
		{"release", props(defs["release"]), Release{}},
		{"thresholds", props(defs["thresholds"]), Thresholds{}},
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

func TestParse_held(t *testing.T) {
	tests := []struct {
		yaml string
		warn bool
	}{
		{"version: 1\n", false},
		{"version: 1\nheld: {}\n", false},
		{"version: 1\nheld: {same_address: block}\n", false},
		{"version: 1\nheld: {same_address: warn}\n", true},
	}
	for _, tt := range tests {
		m, err := Parse([]byte(tt.yaml))
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.yaml, err)
		}
		if got := m.Held.WarnSameAddress(); got != tt.warn {
			t.Errorf("Parse(%q): WarnSameAddress = %v, want %v", tt.yaml, got, tt.warn)
		}
	}
	_, err := Parse([]byte("version: 1\nheld: {same_address: Warn}\n"))
	var ps Problems
	if !errors.As(err, &ps) || len(ps) != 1 || ps[0].Path != "held.same_address" {
		t.Fatalf("Parse with a bad same_address: %v", err)
	}
	// It round-trips, and an unset one writes nothing.
	m, err := Parse([]byte("version: 1\nheld: {same_address: warn}\n"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(m)
	if err != nil || !strings.Contains(string(out), "held:\n  same_address: warn\n") {
		t.Fatalf("Marshal: %v\n%s", err, out)
	}
	if out, _ := Marshal(&Manifest{Version: 1}); strings.Contains(string(out), "held") {
		t.Fatalf("Marshal wrote an unset held:\n%s", out)
	}
}

const usesEndpoint = "endpoint_01jbz7q5mx8vh3k2r9t4w6y0ae"

func TestUsesValidation(t *testing.T) {
	tests := []struct {
		name, yaml, want string
	}{
		{"both resource and external", "version: 1\nresources:\n  db: {kind: postgres}\n  gateway:\n    kind: sample\n    uses:\n      postgres: {resource: db, external: " + usesEndpoint + "}", "resources.gateway.uses.postgres: choose resource or external, not both"},
		{"resource that isn't there", "version: 1\nresources:\n  gateway:\n    kind: sample\n    uses:\n      postgres: {resource: nope}", "resources.gateway.uses.postgres.resource: \"nope\" isn't a resource in this manifest"},
		{"a service isn't a resource", "version: 1\nservices:\n  api: {source: x}\nresources:\n  gateway:\n    kind: sample\n    uses:\n      postgres: {resource: api}", "\"api\" isn't a resource in this manifest"},
		{"resource names itself", "version: 1\nresources:\n  gateway:\n    kind: sample\n    uses:\n      postgres: {resource: gateway}", "resources.gateway.uses.postgres.resource: a resource can't use itself"},
		{"external that isn't an endpoint", "version: 1\nresources:\n  gateway:\n    kind: sample\n    uses:\n      postgres: {external: x}", "resources.gateway.uses.postgres.external: \"x\" isn't a catalog endpoint ID"},
		{"external with the wrong prefix", "version: 1\nresources:\n  gateway:\n    kind: sample\n    uses:\n      postgres: {external: target_01jbz7q5mx8vh3k2r9t4w6y0ae}", "isn't a catalog endpoint ID"},
		{"disabled but connected to a resource", "version: 1\nresources:\n  db: {kind: postgres}\n  gateway:\n    kind: sample\n    uses:\n      postgres: {resource: db, enabled: false}", "resources.gateway.uses.postgres: enabled: false can't be set with resource or external"},
		{"disabled but connected to an endpoint", "version: 1\nresources:\n  gateway:\n    kind: sample\n    uses:\n      postgres: {external: " + usesEndpoint + ", enabled: false}", "enabled: false can't be set with resource or external"},
		{"bad dependency name", "version: 1\nresources:\n  gateway:\n    kind: sample\n    uses:\n      Bad: {enabled: true}", "resources.gateway.uses.Bad: the name must be"},
		{"unknown field", "version: 1\nresources:\n  gateway:\n    kind: sample\n    uses:\n      postgres: {kind: postgres}", "field kind not found"},
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

func TestUsesAccepts(t *testing.T) {
	for name, doc := range map[string]string{
		"nothing set takes a new one": "version: 1\nresources:\n  gateway:\n    kind: sample\n    uses:\n      postgres: {}",
		"switched off":                "version: 1\nresources:\n  gateway:\n    kind: sample\n    uses:\n      falkordb: {enabled: false}",
		"switched on":                 "version: 1\nresources:\n  gateway:\n    kind: sample\n    uses:\n      falkordb: {enabled: true}",
		"enabled with a resource":     "version: 1\nresources:\n  db: {kind: postgres}\n  gateway:\n    kind: sample\n    uses:\n      postgres: {resource: db, enabled: true}",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(doc)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUsesRoundTrips(t *testing.T) {
	doc := `version: 1
resources:
  db:
    kind: postgres
  gateway:
    kind: sample
    target: conn_1
    uses:
      postgres:
        resource: db
      redis:
        external: ` + usesEndpoint + `
      falkordb:
        enabled: true
`
	m, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	uses := m.Resources["gateway"].Uses
	on := true
	want := map[string]Use{
		"postgres": {Resource: "db"},
		"redis":    {External: usesEndpoint},
		"falkordb": {Enabled: &on},
	}
	if !reflect.DeepEqual(uses, want) {
		t.Fatalf("uses = %+v, want %+v", uses, want)
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
	for _, s := range []string{"uses:", "resource: db", "external: " + usesEndpoint, "enabled: true"} {
		if !strings.Contains(string(out), s) {
			t.Errorf("lost %q:\n%s", s, out)
		}
	}
}

const needOptionsDoc = `version: 1
services:
  api:
    source: services/api
    needs: [db, fabriq]
    need_options:
      fabriq:
        tenant: acme
        plan_tier: "2"
resources:
  db:
    kind: postgres
  fabriq:
    kind: sample
`

func TestNeedOptionsValidation(t *testing.T) {
	tests := []struct {
		name string
		opts string
		want string
	}{
		{"for a resource it doesn't need", "      cache: {tenant: acme}\n", "need_options.cache isn't in needs"},
		{"for something that isn't a need at all", "      fabriq: {tenant: acme}\n      other: {a: b}\n", "need_options.other isn't in needs"},
		{"option name in capitals", "      fabriq: {Tenant: acme}\n", `"Tenant" isn't an option name`},
		{"option name with a hyphen", "      fabriq: {plan-tier: gold}\n", `"plan-tier" isn't an option name`},
		{"option name too long", "      fabriq: {" + strings.Repeat("a", 33) + ": x}\n", "isn't an option name"},
		{"value of 201 characters", "      fabriq: {tenant: " + strings.Repeat("a", 201) + "}\n", "is 201 characters; the most is 200"},
		{"value with a newline", "      fabriq: {tenant: \"acme\\nevil\"}\n", "can't hold a newline or other control character"},
		{"value with a tab", "      fabriq: {tenant: \"a\\tb\"}\n", "can't hold a newline or other control character"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := "version: 1\nservices:\n  api:\n    source: services/api\n    needs: [db, fabriq]\n    need_options:\n" + tt.opts +
				"resources:\n  db: {kind: postgres}\n  cache: {kind: redis}\n  fabriq: {kind: sample}\n"
			_, err := Parse([]byte(doc))
			var ps Problems
			if !errors.As(err, &ps) {
				t.Fatalf("got %v, want problems", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want %q", err, tt.want)
			}
		})
	}
	if _, err := Parse([]byte(needOptionsDoc)); err != nil {
		t.Fatalf("a valid manifest: %v", err)
	}
	ok := strings.Replace(needOptionsDoc, "tenant: acme", "tenant: "+strings.Repeat("a", 200), 1)
	if _, err := Parse([]byte(ok)); err != nil {
		t.Errorf("200 characters is allowed: %v", err)
	}
}

func TestNeedOptionsRoundTrip(t *testing.T) {
	m, err := Parse([]byte(needOptionsDoc))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]string{"fabriq": {"tenant": "acme", "plan_tier": "2"}}
	if got := m.Services["api"].NeedOptions; !reflect.DeepEqual(got, want) {
		t.Fatalf("need_options %v, want %v", got, want)
	}
	out, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := again.Services["api"].NeedOptions; !reflect.DeepEqual(got, want) {
		t.Fatalf("after Marshal %v, want %v\n%s", got, want, out)
	}
	// A service without options writes none.
	plain, err := Parse([]byte("version: 1\nservices:\n  api: {source: services/api}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := Marshal(plain); strings.Contains(string(out), "need_options") {
		t.Errorf("wrote need_options for a service with none:\n%s", out)
	}
	data, err := json.Marshal(m.Services["api"])
	if err != nil || !strings.Contains(string(data), `"need_options"`) {
		t.Errorf("JSON %s, %v", data, err)
	}
}

const databaseGatewayDoc = `version: 1
database_gateway:
  provider: cloud-run
  region: us-east1
resources:
  db:
    kind: postgres
    gateway: true
`

func TestManifestDatabaseGateway(t *testing.T) {
	m, err := Parse([]byte(databaseGatewayDoc))
	if err != nil {
		t.Fatal(err)
	}
	dg := m.DatabaseGateway
	if dg == nil || dg.Provider != "cloud-run" || dg.Region != "us-east1" {
		t.Fatalf("database_gateway = %+v", dg)
	}
	if !m.Resources["db"].GatewayIn("production") {
		t.Error("db isn't behind the gateway")
	}
	out, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(out)
	if err != nil || !reflect.DeepEqual(again, m) {
		t.Fatalf("round trip: %v\n%s", err, out)
	}
	m, err = Parse([]byte("version: 1\nresources:\n  db: {kind: postgres}\n"))
	if err != nil || m.DatabaseGateway != nil || m.Resources["db"].GatewayIn("production") {
		t.Fatalf("a manifest with no gateway: %v %+v", err, m)
	}
	if out, _ := Marshal(m); strings.Contains(string(out), "gateway") {
		t.Fatalf("Marshal wrote an unset gateway:\n%s", out)
	}
}

func TestManifestDatabaseGatewayOptions(t *testing.T) {
	m, err := Parse([]byte(`version: 1
database_gateway:
  cloud_tags: [eu, private]
  environments:
    staging: {provider: render, region: oregon}
    preview: {enabled: false}
`))
	if err != nil {
		t.Fatal(err)
	}
	dg := m.DatabaseGateway
	if !slices.Equal(dg.CloudTags, []string{"eu", "private"}) || dg.Environments["staging"].Provider != "render" || On(dg.Environments["preview"].Enabled) {
		t.Fatalf("database_gateway = %+v", dg)
	}
}

func TestDatabaseGatewayValidation(t *testing.T) {
	for doc, want := range map[string]string{
		"database_gateway: {provider: Cloud Run}\n":                                "database_gateway.provider: \"Cloud Run\" isn't a provider name",
		"database_gateway: {region: US}\n":                                         "database_gateway.region: \"US\" isn't a region name",
		"database_gateway: {cloud_tags: [Bad]}\n":                                  "database_gateway.cloud_tags: \"Bad\" must be a lowercase tag",
		"database_gateway: {cloud_tags: [eu, eu]}\n":                               "database_gateway.cloud_tags: names \"eu\" twice",
		"database_gateway: {cloud_tags: [a, b, c, d, e, f, g, h, i]}\n":            "database_gateway.cloud_tags: has 9 tags; the most is 8",
		"database_gateway: {provider: cloud-run, cloud_tags: [eu]}\n":              "database_gateway.cloud_tags: can't be set with database_gateway.provider",
		"database_gateway:\n  environments:\n    Prod: {region: us-east1}\n":       "database_gateway.environments.Prod: isn't an environment name",
		"database_gateway:\n  environments:\n    prod: {provider: Bad Provider}\n": "database_gateway.environments.prod.provider",
		"database_gateway:\n  environments:\n    prod: {gateway: true}\n":          "database_gateway.environments.prod.gateway: only resources can opt into the database gateway",
		"gateways:\n  edge:\n    routes: [{path: /, to: web}]\n    environments:\n      prod: {gateway: true}\nservices:\n  web: {source: web}\n": "gateways.edge.environments.prod.gateway: only resources can opt into the database gateway",
	} {
		_, err := Parse([]byte("version: 1\n" + doc))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: expected %q, got %v", doc, want, err)
		}
	}
}

func TestGatewayOnlyPostgres(t *testing.T) {
	var ps Problems
	_, err := Parse([]byte("version: 1\nresources:\n  cache: {kind: redis, gateway: true}\n"))
	if !errors.As(err, &ps) || len(ps) != 1 || ps[0].Path != "resources.cache.gateway" || ps[0].Message != "only postgres resources can use the database gateway" {
		t.Fatalf("problems: %v", err)
	}
	if want := "resources.cache.gateway: only postgres resources can use the database gateway"; !strings.Contains(err.Error(), want) {
		t.Errorf("got %v, want %q", err, want)
	}
	_, err = Parse([]byte("version: 1\nresources:\n  cache:\n    kind: redis\n    environments:\n      prod: {gateway: true}\n"))
	if want := "resources.cache.environments.prod.gateway: only postgres resources can use the database gateway"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("environment override: got %v, want %q", err, want)
	}
	// Turning it off is no claim on the gateway.
	if _, err := Parse([]byte("version: 1\nresources:\n  cache:\n    kind: redis\n    gateway: false\n    environments:\n      prod: {gateway: false}\n")); err != nil {
		t.Errorf("gateway: false on redis: %v", err)
	}
}

func TestGatewayEnvOverride(t *testing.T) {
	m, err := Parse([]byte(`version: 1
resources:
  on:
    kind: postgres
    gateway: true
    environments:
      staging: {gateway: false}
      preview: {enabled: false}
  off:
    kind: postgres
    environments:
      production: {gateway: true}
  plain:
    kind: postgres
`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		resource, env string
		want          bool
	}{
		{"on", "production", true},
		{"on", "staging", false},
		{"on", "preview", true},
		{"off", "production", true},
		{"off", "staging", false},
		{"plain", "production", false},
	} {
		if got := m.Resources[tc.resource].GatewayIn(tc.env); got != tc.want {
			t.Errorf("%s in %s: GatewayIn = %v, want %v", tc.resource, tc.env, got, tc.want)
		}
	}
}

// The fixture is a manifest that uses every database gateway field, so it
// has to parse and has to validate against the served schema. A bad value
// fails the schema the way it fails Parse.
func TestSchema_databaseGateway(t *testing.T) {
	fixture, err := os.ReadFile("testdata/database_gateway.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(fixture); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	schema := compileSchema(t)
	if err := schema.Validate(yamlToJSON(t, fixture)); err != nil {
		t.Fatalf("the schema rejects the fixture: %v", err)
	}
	for _, doc := range []string{
		"version: 1\ndatabase_gateway: {provider: cloud-run, cloud_tags: [eu]}\n",
		"version: 1\ndatabase_gateway: {size: 2}\n",
		"version: 1\ndatabase_gateway: {cloud_tags: [Bad]}\n",
		"version: 1\ndatabase_gateway: {environments: {prod: {gateway: true}}}\n",
		"version: 1\nresources:\n  db: {kind: postgres, gateway: \"yes\"}\n",
		"version: 1\nresources:\n  db: {kind: postgres, environments: {prod: {gateway: 1}}}\n",
	} {
		if err := schema.Validate(yamlToJSON(t, []byte(doc))); err == nil {
			t.Errorf("the schema accepts %q", doc)
		}
	}
}

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(Schema))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.UseRegexpEngine(goOrAnyRegexp)
	if err = c.AddResource(SchemaID, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(SchemaID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func yamlToJSON(t *testing.T, data []byte) any {
	t.Helper()
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// goOrAnyRegexp compiles a pattern with Go's regexp. A few of the schema's
// patterns use lookahead, which Go doesn't have and which editors' engines
// do; those match anything here, since only the database gateway's fields
// are under test.
func goOrAnyRegexp(pattern string) (jsonschema.Regexp, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return anyRegexp(pattern), nil
	}
	return re, nil
}

type anyRegexp string

func (a anyRegexp) MatchString(string) bool { return true }
func (a anyRegexp) String() string          { return string(a) }

func TestFarpValidation(t *testing.T) {
	for doc, want := range map[string]string{
		`{source: a, farp: {openapi: openapi.json}}`:                          "services.api.farp.openapi: must start with /",
		`{source: a, farp: {health: "/h?x"}}`:                                 "services.api.farp.health",
		`{source: a, farp: {capabilities: [REST]}}`:                           "services.api.farp.capabilities",
		`{source: a, farp: {capabilities: [a, b, c, d, e, f, g, h, i]}}`:      "services.api.farp.capabilities: has 9",
		`{source: a, farp: {port: 70000}}`:                                    "services.api.farp.port",
		`{source: a, farp: {gateway: Bad}}`:                                   "services.api.farp.gateway",
		`{source: a, environments: {staging: {farp: {metrics: "no-slash"}}}}`: "services.api.environments.staging.farp.metrics: must start with /",
		`{source: a, farp: {capabilities: [rest, rest]}}`:                     "names \"rest\" twice",
		`{source: a, farp: {asyncapi: "/a b"}}`:                               "services.api.farp.asyncapi",
		`{source: a, farp: {graphql: "/g#frag"}}`:                             "services.api.farp.graphql",
	} {
		_, err := Parse([]byte("version: 1\nservices:\n  api: " + doc + "\n"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: expected %q, got %v", doc, want, err)
		}
	}

	// A bare block, defaults, a port left out and the largest port are all fine.
	for _, doc := range []string{
		`{source: a, farp: {}}`,
		`{source: a, port: 8080, farp: {gateway: edge, openapi: /openapi.json, asyncapi: /asyncapi.json, graphql: /graphql, health: /health, metrics: /metrics, grpc_reflection: true, capabilities: [rest, websocket], port: 65535}}`,
		`{source: a, farp: {enabled: false}}`,
	} {
		if _, err := Parse([]byte("version: 1\nservices:\n  api: " + doc + "\nresources:\n  edge: {kind: octopus}\n")); err != nil {
			t.Errorf("%s: %v", doc, err)
		}
	}
}

func TestFarpRoundTrips(t *testing.T) {
	m, err := Parse([]byte("version: 1\nservices:\n  api: {source: a, farp: {}}\n  web: {source: w}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Services["api"].Farp == nil || m.Services["web"].Farp != nil {
		t.Fatalf("farp blocks: %+v, %+v", m.Services["api"].Farp, m.Services["web"].Farp)
	}
	out, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(out)
	if err != nil || again.Services["api"].Farp == nil || again.Services["web"].Farp != nil {
		t.Fatalf("round trip: %v\n%s", err, out)
	}
}

func TestFarpEnvironmentReplacesBlock(t *testing.T) {
	m, err := Parse([]byte(`version: 1
services:
  api:
    source: a
    farp: {openapi: /a, health: /h}
    environments:
      staging:
        farp: {enabled: false}
`))
	if err != nil {
		t.Fatal(err)
	}
	staging := m.Services["api"].For("staging", m.Defaults)
	off := false
	if want := (&Farp{Enabled: &off}); !reflect.DeepEqual(staging.Farp, want) {
		t.Fatalf("staging farp: %+v", staging.Farp)
	}
	production := m.Services["api"].For("production", m.Defaults)
	if production.Farp == nil || production.Farp.OpenAPI != "/a" || production.Farp.Health != "/h" || production.Farp.Enabled != nil {
		t.Fatalf("production farp: %+v", production.Farp)
	}
	// Settings are copies: changing one doesn't change the manifest.
	production.Farp.OpenAPI = "/changed"
	if m.Services["api"].Farp.OpenAPI != "/a" {
		t.Fatal("a service's farp block is shared with its settings")
	}
}

const gpuResourceDoc = "version: 2\nresources:\n  llm: {kind: gpusample, provider: server, target: t, gpu: {kind: nvidia-l4, count: 1}, environments: {staging: {gpu: {kind: nvidia-t4, count: 1}}}}\n"

// schemaAccepts reports the schema's verdict on a manifest document.
func schemaAccepts(t *testing.T, doc string) error {
	t.Helper()
	return compileSchema(t).Validate(yamlToJSON(t, []byte(doc)))
}

func TestParse_gpuResourceMergesPerEnvironment(t *testing.T) {
	m, err := Parse([]byte(gpuResourceDoc))
	if err != nil {
		t.Fatal(err)
	}
	r := m.Resources["llm"]
	if got := r.For("staging", m.Defaults).GPU; got == nil || got.Kind != "nvidia-t4" {
		t.Errorf("staging GPU = %+v, want nvidia-t4", got)
	}
	if got := r.For("production", m.Defaults).GPU; got == nil || got.Kind != "nvidia-l4" || got.Count != 1 {
		t.Errorf("production GPU = %+v, want nvidia-l4 x1", got)
	}
	if err := schemaAccepts(t, gpuResourceDoc); err != nil {
		t.Errorf("schema: %v", err)
	}
}

func TestParse_gpuOnService(t *testing.T) {
	doc := "version: 2\nservices:\n  api:\n    source: a\n    size: {cpu: 4, memory: 16384, gpu: {kind: nvidia-l4, count: 2}, shm: 2048}\n"
	m, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	sz := m.Services["api"].Size
	if sz.GPU == nil || sz.GPU.Kind != "nvidia-l4" || sz.GPU.Count != 2 || sz.Shm != 2048 {
		t.Errorf("size = %+v", sz)
	}
	if err := schemaAccepts(t, doc); err != nil {
		t.Errorf("schema: %v", err)
	}
}

func TestParse_gpuRejects(t *testing.T) {
	svc := func(size string) string {
		return "version: 2\nservices:\n  api:\n    source: a\n    size: {cpu: 1, memory: 16384, " + size + "}\n"
	}
	res := func(gpu string) string {
		return "version: 2\nresources:\n  llm: {kind: gpusample, gpu: " + gpu + "}\n"
	}
	tests := []struct {
		name, doc, path string
		schemaRejects   bool
	}{
		{"count 0", svc("gpu: {kind: nvidia-l4, count: 0}"), "services.api.size.gpu.count", true},
		{"count 9", svc("gpu: {kind: nvidia-l4, count: 9}"), "services.api.size.gpu.count", true},
		{"empty kind", svc(`gpu: {kind: "", count: 1}`), "services.api.size.gpu.kind", true},
		{"shm equals memory", svc("shm: 16384"), "services.api.size.shm", false},
		{"negative shm", svc("shm: -1"), "services.api.size.shm", true},
		{"resource count 0", res("{kind: nvidia-l4, count: 0}"), "resources.llm.gpu.count", true},
		{"resource env count 9", "version: 2\nresources:\n  llm: {kind: gpusample, environments: {staging: {gpu: {kind: nvidia-l4, count: 9}}}}\n", "resources.llm.environments.staging.gpu.count", true},
		{"gateway env", "version: 2\nservices:\n  web: {source: a}\ngateways:\n  edge: {routes: [{path: /, to: web}], environments: {staging: {gpu: {kind: nvidia-l4, count: 1}}}}\n", "gateways.edge.environments.staging.gpu", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.doc))
			var ps Problems
			if !errors.As(err, &ps) {
				t.Fatalf("Parse err = %v, want problems", err)
			}
			found := false
			for _, p := range ps {
				if p.Path == tt.path {
					found = true
				}
			}
			if !found {
				t.Errorf("no problem at %s: %v", tt.path, err)
			}
			if tt.schemaRejects && schemaAccepts(t, tt.doc) == nil {
				t.Error("schema accepts it")
			}
		})
	}
}

func TestResourceSettingsParse(t *testing.T) {
	doc := "version: 1\nresources:\n  llm:\n    kind: sample\n    settings: {model: acme/x, max_model_len: \"8192\"}\n"
	m, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := m.Resources["llm"].Settings, map[string]string{"model": "acme/x", "max_model_len": "8192"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("settings %v, want %v", got, want)
	}
	schema := compileSchema(t)
	if err := schema.Validate(yamlToJSON(t, []byte(doc))); err != nil {
		t.Fatalf("the schema rejects settings: %v", err)
	}
	for name, bad := range map[string]string{
		"a bad key":        "version: 1\nresources:\n  llm: {kind: sample, settings: {Bad: x}}\n",
		"a long value":     "version: 1\nresources:\n  llm: {kind: sample, settings: {model: " + strings.Repeat("x", MaxSettingValue+1) + "}}\n",
		"a value not text": "version: 1\nresources:\n  llm: {kind: sample, settings: {model: {name: x}}}\n",
	} {
		if err := schema.Validate(yamlToJSON(t, []byte(bad))); err == nil {
			t.Errorf("%s: the schema accepts it", name)
		}
		if _, err := Parse([]byte(bad)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Parse: %v", name, err)
		}
	}
}
