package manifest

import (
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
