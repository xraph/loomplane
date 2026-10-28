package manifest

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const commented = `# Acme's manifest.
version: 1
services:
  # The API, which needs a database.
  api:
    source: services/api # the Go module
    provider: cloud-run
`

func TestWithResource_keepsComments(t *testing.T) {
	out, err := WithResource([]byte(commented), "db", Resource{Kind: "postgres", Provider: "cloud-sql"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Acme's manifest.", "# The API, which needs a database.", "# the Go module"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("lost %q:\n%s", want, out)
		}
	}
	m, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if r := m.Resources["db"]; r.Kind != "postgres" || r.Provider != "cloud-sql" {
		t.Fatalf("resources %+v", m.Resources)
	}
	if _, err := WithResource(out, "api", Resource{Kind: "redis"}); err == nil {
		t.Error("added a resource with a service's name")
	}
}

func TestWithIgnored(t *testing.T) {
	out, err := WithIgnored([]byte(commented), "kafka")
	if err != nil {
		t.Fatal(err)
	}
	out, err = WithIgnored(out, "redis")
	if err != nil {
		t.Fatal(err)
	}
	again, err := WithIgnored(out, "redis")
	if err != nil || string(again) != string(out) {
		t.Fatalf("ignoring twice changed it: %v\n%s", err, again)
	}
	m, err := Parse(out)
	if err != nil || !m.Ignored("kafka") || !m.Ignored("redis") || !strings.Contains(string(out), "# The API") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestWithPlacement_scopesChangesAndKeepsComments(t *testing.T) {
	provider, region, enabled := "ecs-fargate", "us-east-1", false
	out, err := WithPlacement([]byte(commented), Placement{Section: "services", Name: "api", Environment: "preview", Provider: &provider, Region: &region, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if s := m.Services["api"]; s.Provider != "cloud-run" || s.For("preview", m.Defaults).Provider != "ecs-fargate" || On(s.For("preview", m.Defaults).Enabled) {
		t.Fatalf("placement leaked across environments: %+v", s)
	}
	if !strings.Contains(string(out), "# the Go module") || !strings.Contains(string(out), "# Acme's manifest.") {
		t.Fatalf("comments lost: %s", out)
	}
	empty := ""
	out, err = WithPlacement(out, Placement{Section: "services", Name: "api", Environment: "preview", Provider: &empty, Region: &empty})
	if err != nil {
		t.Fatal(err)
	}
	m, _ = Parse(out)
	if m.Services["api"].For("preview", m.Defaults).Provider != "cloud-run" {
		t.Fatal("clearing an override didn't restore inheritance")
	}
}

func TestWithPlacement_rejectsInvalidTargets(t *testing.T) {
	provider, enabled := "vercel", false
	for _, p := range []Placement{
		{Section: "services", Name: "absent", Provider: &provider},
		{Section: "links", Name: "api", Provider: &provider},
		{Section: "services", Name: "api", Environment: "../preview", Provider: &provider},
		{Section: "services", Name: "api"},
		{Section: "resources", Name: "db", Enabled: &enabled},
	} {
		if _, err := WithPlacement([]byte(commented), p); err == nil {
			t.Errorf("accepted %+v", p)
		}
	}
}

func TestWithPlacement_materializesEmptyAndAliasedEnvironments(t *testing.T) {
	provider, enabled := "ecs-fargate", false
	for _, suffix := range []string{
		"    environments:\n",
		"    environments:\n      preview:\n",
		"    environments:\n      production: &prod\n        provider: cloud-run\n        region: us-central1\n      preview: *prod\n",
	} {
		doc := commented + suffix
		out, err := WithPlacement([]byte(doc), Placement{Section: "services", Name: "api", Environment: "preview", Provider: &provider, Enabled: &enabled})
		if err != nil {
			t.Fatal(err)
		}
		m, err := Parse(out)
		if err != nil {
			t.Fatal(err)
		}
		if s := m.Services["api"].For("preview", m.Defaults); s.Provider != provider || On(s.Enabled) {
			t.Fatalf("placement silently ignored for %q: %s", suffix, out)
		}
		if s := m.Services["api"].For("production", m.Defaults); s.Provider != "cloud-run" || !On(s.Enabled) {
			t.Fatalf("edit leaked to aliased production: %+v", s)
		}
	}
}

func TestWithPlacement_preservesAliasesOfTheEditedAnchor(t *testing.T) {
	doc := commented + "    environments:\n      production: &prod\n        provider: cloud-run\n      preview: *prod\n"
	provider := "ecs-fargate"
	out, err := WithPlacement([]byte(doc), Placement{Section: "services", Name: "api", Environment: "production", Provider: &provider})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := Parse(out)
	if m.Services["api"].For("preview", m.Defaults).Provider != "cloud-run" {
		t.Fatalf("edited anchor changed preview: %s", out)
	}
}

func TestWithPlacement_clearsAMergedRegion(t *testing.T) {
	doc := commented + "    environments:\n      production: &prod\n        provider: cloud-run\n        region: us-central1\n      preview:\n        <<: *prod # shared settings\n"
	provider, empty := "ecs-fargate", ""
	out, err := WithPlacement([]byte(doc), Placement{Section: "services", Name: "api", Environment: "preview", Provider: &provider, Region: &empty})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := Parse(out)
	if s := m.Services["api"].For("preview", m.Defaults); s.Provider != provider || s.Region != "" {
		t.Fatalf("merged region wasn't cleared: %+v", s)
	}
}

func TestWithResourceWritesCatalogFields(t *testing.T) {
	on := true
	want := Resource{
		Kind: "sample", Provider: "server", Target: "conn_1", Version: "1",
		Uses: map[string]Use{
			"postgres": {Resource: "db"},
			"falkordb": {Enabled: &on},
		},
	}
	doc := strings.Replace(commented, "services:", "resources:\n  db: {kind: postgres}\nservices:", 1)
	out, err := WithResource([]byte(doc), "gateway", want)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"# Acme's manifest.", "# The API, which needs a database.", "# the Go module"} {
		if !strings.Contains(string(out), s) {
			t.Errorf("lost %q:\n%s", s, out)
		}
	}
	m, err := Parse(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := m.Resources["gateway"]; !reflect.DeepEqual(got, want) {
		t.Errorf("gateway = %+v, want %+v\n%s", got, want, out)
	}
}

const needsDoc = `# Acme's manifest.
version: 1
services:
  # The API.
  api:
    source: services/api # the Go module
    needs: [db] # just the database
resources:
  db:
    kind: postgres
  fabriq:
    kind: sample
    uses:
      postgres: {resource: db} # share the main database
`

func TestWithNeedAddsNeedAndOptions(t *testing.T) {
	out, err := WithNeed([]byte(needsDoc), "api", "fabriq", map[string]string{"tenant": "acme"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Acme's manifest.", "# The API.", "# the Go module", "# just the database", "# share the main database"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("lost %q:\n%s", want, out)
		}
	}
	m, err := Parse(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	api := m.Services["api"]
	if !reflect.DeepEqual(api.Needs, []string{"db", "fabriq"}) {
		t.Errorf("needs %v, want db then fabriq", api.Needs)
	}
	if want := map[string]map[string]string{"fabriq": {"tenant": "acme"}}; !reflect.DeepEqual(api.NeedOptions, want) {
		t.Errorf("need_options %v, want %v", api.NeedOptions, want)
	}

	// Again with other options: still one need, options replaced.
	out, err = WithNeed(out, "api", "fabriq", map[string]string{"region": "eu"})
	if err != nil {
		t.Fatal(err)
	}
	m, _ = Parse(out)
	if api := m.Services["api"]; !reflect.DeepEqual(api.Needs, []string{"db", "fabriq"}) ||
		!reflect.DeepEqual(api.NeedOptions["fabriq"], map[string]string{"region": "eu"}) {
		t.Errorf("needs %v, options %v", api.Needs, api.NeedOptions)
	}

	// Without options it keeps what is there.
	same, err := WithNeed(out, "api", "fabriq", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(same) != string(out) {
		t.Errorf("linking again without options changed the manifest:\n%s", same)
	}

	// A need with no options adds no need_options.
	plain, err := WithNeed([]byte(needsDoc), "api", "fabriq", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "need_options") {
		t.Errorf("wrote need_options for none:\n%s", plain)
	}
}

func TestWithNeedRefusesWhatCannotWork(t *testing.T) {
	doc := []byte(needsDoc)
	tests := []struct {
		name, service, need string
		options             map[string]string
		want                string
	}{
		{"no such service", "web", "fabriq", nil, "services.web doesn't exist"},
		{"no such need", "api", "ghost", nil, `"ghost" isn't a service or resource`},
		{"itself", "api", "api", nil, "can't need itself"},
		{"bad option name", "api", "fabriq", map[string]string{"Tenant": "acme"}, `"Tenant" isn't an option name`},
		{"long value", "api", "fabriq", map[string]string{"tenant": strings.Repeat("a", 201)}, "is 201 characters; the most is 200"},
		{"newline", "api", "fabriq", map[string]string{"tenant": "a\nb"}, "can't hold a newline or other control character"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := WithNeed(doc, tt.service, tt.need, tt.options)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want an invalid manifest saying %q", err, tt.want)
			}
		})
	}
}

func TestWithoutNeedRemovesBoth(t *testing.T) {
	linked, err := WithNeed([]byte(needsDoc), "api", "fabriq", map[string]string{"tenant": "acme"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := WithNeed(linked, "api", "db", map[string]string{"schema": "app"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := WithoutNeed(other, "api", "fabriq")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	api := m.Services["api"]
	if !reflect.DeepEqual(api.Needs, []string{"db"}) || !reflect.DeepEqual(api.NeedOptions, map[string]map[string]string{"db": {"schema": "app"}}) {
		t.Errorf("needs %v, options %v", api.Needs, api.NeedOptions)
	}

	// The last options go with the need, and need_options goes with them.
	out, err = WithoutNeed(linked, "api", "fabriq")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "need_options") {
		t.Errorf("left an empty need_options:\n%s", out)
	}
	for _, want := range []string{"# Acme's manifest.", "# The API.", "# the Go module"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("lost %q:\n%s", want, out)
		}
	}

	// A service's only need: needs goes too.
	out, err = WithoutNeed([]byte(needsDoc), "api", "db")
	if err != nil {
		t.Fatal(err)
	}
	m, err = Parse(out)
	if err != nil || len(m.Services["api"].Needs) != 0 || strings.Contains(string(out), "needs:") {
		t.Errorf("%v\n%s", err, out)
	}

	if _, err := WithoutNeed([]byte(needsDoc), "api", "fabriq"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "api doesn't need fabriq") {
		t.Errorf("unlinking something it doesn't need: %v", err)
	}
	if _, err := WithoutNeed([]byte(needsDoc), "web", "db"); !errors.Is(err, ErrInvalid) {
		t.Errorf("unlinking from a missing service: %v", err)
	}
}

func TestWithUseSetsOneDependency(t *testing.T) {
	doc := []byte(needsDoc + "  cache:\n    kind: redis\n")
	on := true
	out, err := WithUse(doc, "fabriq", "redis", Use{Resource: "cache"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "# share the main database") {
		t.Errorf("lost a comment:\n%s", out)
	}
	m, err := Parse(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := map[string]Use{"postgres": {Resource: "db"}, "redis": {Resource: "cache"}}
	if got := m.Resources["fabriq"].Uses; !reflect.DeepEqual(got, want) {
		t.Errorf("uses %v, want %v", got, want)
	}

	// Setting one again replaces only that one.
	out, err = WithUse(out, "fabriq", "redis", Use{Enabled: &on})
	if err != nil {
		t.Fatal(err)
	}
	m, _ = Parse(out)
	want = map[string]Use{"postgres": {Resource: "db"}, "redis": {Enabled: &on}}
	if got := m.Resources["fabriq"].Uses; !reflect.DeepEqual(got, want) {
		t.Errorf("uses %v, want %v", got, want)
	}

	// A resource with no uses gets them.
	out, err = WithUse(doc, "cache", "postgres", Use{External: "endpoint_01jbz7q5mx8vh3k2r9t4w6y0ae"})
	if err != nil {
		t.Fatal(err)
	}
	m, _ = Parse(out)
	if got := m.Resources["cache"].Uses["postgres"].External; got != "endpoint_01jbz7q5mx8vh3k2r9t4w6y0ae" {
		t.Errorf("external %q\n%s", got, out)
	}

	for name, call := range map[string]func() error{
		"no such resource":    func() error { _, err := WithUse(doc, "ghost", "postgres", Use{}); return err },
		"a service":           func() error { _, err := WithUse(doc, "api", "postgres", Use{}); return err },
		"bad dependency name": func() error { _, err := WithUse(doc, "fabriq", "Postgres", Use{}); return err },
		"both choices": func() error {
			_, err := WithUse(doc, "fabriq", "redis", Use{Resource: "db", External: "endpoint_01jbz7q5mx8vh3k2r9t4w6y0ae"})
			return err
		},
		"itself": func() error { _, err := WithUse(doc, "fabriq", "redis", Use{Resource: "fabriq"}); return err },
	} {
		if err := call(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
}

func TestEdits_keepDatabaseGatewayFields(t *testing.T) {
	doc := []byte(`# Acme's manifest.
version: 1
database_gateway:
  provider: cloud-run # where the gateway runs
  region: us-east1
resources:
  db:
    kind: postgres
    gateway: true
    environments:
      staging: {gateway: false}
services:
  api:
    source: services/api
    provider: cloud-run
`)
	check := func(step string, out []byte) {
		t.Helper()
		m, err := Parse(out)
		if err != nil {
			t.Fatalf("%s: %v\n%s", step, err, out)
		}
		if m.DatabaseGateway == nil || m.DatabaseGateway.Provider != "cloud-run" || m.DatabaseGateway.Region != "us-east1" {
			t.Errorf("%s lost database_gateway:\n%s", step, out)
		}
		db := m.Resources["db"]
		if !db.GatewayIn("production") || db.GatewayIn("staging") {
			t.Errorf("%s lost the db's gateway settings:\n%s", step, out)
		}
		if !strings.Contains(string(out), "# where the gateway runs") {
			t.Errorf("%s lost a comment:\n%s", step, out)
		}
	}
	region := "eu-west-1"
	out, err := WithPlacement(doc, Placement{Section: "services", Name: "api", Region: &region})
	if err != nil {
		t.Fatal(err)
	}
	check("a service placement edit", out)
	provider := "cloud-sql"
	out, err = WithPlacement(out, Placement{Section: "resources", Name: "db", Environment: "production", Provider: &provider})
	if err != nil {
		t.Fatal(err)
	}
	check("a resource placement edit", out)
	out, err = WithResource(out, "cache", Resource{Kind: "redis"})
	if err != nil {
		t.Fatal(err)
	}
	check("adding a resource", out)
	out, err = WithIgnored(out, "mysql")
	if err != nil {
		t.Fatal(err)
	}
	check("ignoring a kind", out)
}

func TestWithPlacement_scaleAndSize(t *testing.T) {
	doc := []byte(`version: 1
services:
  api:
    source: ./api
    # Keep the GPU when CPU and memory change.
    size:
      cpu: 1
      memory: 1024
      gpu:
        kind: nvidia-l4
        count: 1
`)
	out, err := WithPlacement(doc, Placement{Section: "services", Name: "api", Environment: "production", Scale: &Scale{Min: 2, Max: 5}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if s := m.Services["api"].For("production", m.Defaults).Scale; s == nil || s.Min != 2 || s.Max != 5 {
		t.Fatalf("production scale: %+v", s)
	}
	if s := m.Services["api"].For("staging", m.Defaults).Scale; s != nil {
		t.Fatalf("an edit for production reached staging: %+v", s)
	}

	out, err = WithPlacement(out, Placement{Section: "services", Name: "api", Size: &SizeEdit{CPU: 2, Memory: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "# Keep the GPU when CPU and memory change.") {
		t.Fatalf("comment lost:\n%s", out)
	}
	m, err = Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	size := m.Services["api"].Size
	if size == nil || size.CPU != 2 || size.Memory != 4096 || size.GPU == nil || size.GPU.Count != 1 {
		t.Fatalf("size edit lost what it didn't change: %+v", size)
	}

	// Zero values restore inheritance.
	out, err = WithPlacement(out, Placement{Section: "services", Name: "api", Environment: "production", Scale: &Scale{}})
	if err != nil {
		t.Fatal(err)
	}
	out, err = WithPlacement(out, Placement{Section: "services", Name: "api", Size: &SizeEdit{}})
	if err != nil {
		t.Fatal(err)
	}
	m, err = Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if set := m.Services["api"].For("production", m.Defaults); set.Scale != nil || set.Size != nil {
		t.Fatalf("zero edits kept settings: %+v %+v", set.Scale, set.Size)
	}

	for name, p := range map[string]Placement{
		"min above max": {Section: "services", Name: "api", Scale: &Scale{Min: 4, Max: 2}},
		"no memory":     {Section: "services", Name: "api", Size: &SizeEdit{CPU: 1}},
		"not a service": {Section: "resources", Name: "api", Scale: &Scale{Min: 1, Max: 1}},
	} {
		if _, err := WithPlacement(doc, p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
