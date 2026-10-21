package manifest

import (
	"slices"
	"strings"
	"testing"
)

func TestCloudTagsParseAndPrecedence(t *testing.T) {
	doc := []byte(`version: 1
defaults: {cloud_tags: [eu]}
services:
  web:
    source: web
    cloud_tags: [frontend, eu]
    environments:
      staging: {cloud_target: preview}
  api:
    source: api
    cloud_target: api
    environments:
      staging: {cloud_tags: [gpu]}
  plain: {source: plain}
  direct: {source: direct, provider: vercel}
`)
	m, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		service, env, target string
		tags                 []string
	}{
		{"web", "production", "", []string{"frontend", "eu"}},
		{"web", "staging", "preview", nil},
		{"api", "production", "api", nil},
		{"api", "staging", "", []string{"gpu"}},
		{"plain", "production", "", []string{"eu"}},
		{"direct", "production", "", nil},
	} {
		set := m.Services[tc.service].For(tc.env, m.Defaults)
		if set.CloudTarget != tc.target || !slices.Equal(set.CloudTags, tc.tags) {
			t.Errorf("%s in %s: target %q tags %v", tc.service, tc.env, set.CloudTarget, set.CloudTags)
		}
	}
	set := m.Services["plain"].For("production", m.Defaults)
	set.CloudTags[0] = "changed"
	if m.Defaults.CloudTags[0] != "eu" {
		t.Fatal("default tags shared with a service's settings")
	}
}

func TestCloudTagsValidation(t *testing.T) {
	for doc, want := range map[string]string{
		"services:\n  web: {source: web, cloud_target: a, cloud_tags: [b]}\n":               "cloud_target",
		"services:\n  web: {source: web, cloud_tags: [Bad]}\n":                              "lowercase",
		"services:\n  web: {source: web, cloud_tags: [eu, eu]}\n":                           "twice",
		"services:\n  web: {source: web, cloud_tags: [a, b, c, d, e, f, g, h, i]}\n":        "most",
		"defaults: {provider: vercel, cloud_tags: [eu]}\nservices:\n  web: {source: web}\n": "defaults.provider",
	} {
		if _, err := Parse([]byte("version: 1\n" + doc)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: expected %q, got %v", doc, want, err)
		}
	}
}

func TestCloudTagsPlacementEdit(t *testing.T) {
	doc := []byte("version: 1\nservices:\n  web:\n    source: web\n    cloud_target: frontend\n")
	empty := ""
	edited, err := WithPlacement(doc, Placement{Section: "services", Name: "web", CloudTarget: &empty, CloudTags: &[]string{"eu", "gpu"}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(edited)
	if err != nil {
		t.Fatal(err)
	}
	if set := m.Services["web"].For("production", m.Defaults); set.CloudTarget != "" || !slices.Equal(set.CloudTags, []string{"eu", "gpu"}) {
		t.Fatalf("edit: %s", edited)
	}
	if _, err = WithPlacement(edited, Placement{Section: "services", Name: "web", CloudTarget: &[]string{"frontend"}[0]}); err == nil {
		t.Fatal("edit left both cloud_target and cloud_tags")
	}
	edited, err = WithPlacement(edited, Placement{Section: "services", Name: "web", Environment: "staging", CloudTags: &[]string{"us"}})
	if err != nil || !strings.Contains(string(edited), "staging:") {
		t.Fatalf("environment edit: %s %v", edited, err)
	}
	edited, err = WithPlacement(edited, Placement{Section: "services", Name: "web", CloudTags: &[]string{}})
	if err != nil || strings.Contains(strings.SplitN(string(edited), "environments", 2)[0], "cloud_tags") {
		t.Fatalf("clear: %s %v", edited, err)
	}
	if _, err = WithPlacement([]byte("version: 1\nresources:\n  db: {kind: postgres}\n"), Placement{Section: "resources", Name: "db", CloudTags: &[]string{"eu"}}); err == nil {
		t.Fatal("tags accepted on a resource")
	}
}
