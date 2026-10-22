package manifest

import (
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
