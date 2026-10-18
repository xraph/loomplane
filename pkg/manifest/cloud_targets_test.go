package manifest

import (
	"strings"
	"testing"
)

func TestCloudTargetParseOverrideAndEdit(t *testing.T) {
	doc := []byte("version: 1\nservices:\n  web:\n    source: web\n    cloud_target: frontend\n    environments:\n      staging: {cloud_target: preview, health_path: /ready}\n")
	m, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	set := m.Services["web"].For("staging", m.Defaults)
	if set.CloudTarget != "preview" || set.HealthPath != "/ready" {
		t.Fatalf("override lost: %+v", set)
	}
	target := "production"
	edited, err := WithPlacement(doc, Placement{Section: "services", Name: "web", CloudTarget: &target})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(edited), "cloud_target: production") || !strings.Contains(string(edited), "cloud_target: preview") {
		t.Fatalf("edit lost placement: %s", edited)
	}
	if _, err = WithPlacement([]byte("version: 1\nresources:\n  db: {kind: postgres}\n"), Placement{Section: "resources", Name: "db", CloudTarget: &target}); err == nil {
		t.Fatal("resource accepted Cloud target")
	}
}

func TestCloudTargetRejectsInvalidSlug(t *testing.T) {
	if _, err := Parse([]byte("version: 1\nservices:\n  web: {source: web, cloud_target: 'bad target'}\n")); err == nil {
		t.Fatal("invalid slug accepted")
	}
}

func TestCloudTargetAcceptsFullTargetSlugAndClearsLocalPlacement(t *testing.T) {
	slug := strings.Repeat("a", 48)
	if _, err := Parse([]byte("version: 1\nservices:\n  web: {source: web, cloud_target: " + slug + "}\n")); err != nil {
		t.Fatal(err)
	}
	doc := []byte("version: 1\nservices:\n  web: {source: web, provider: server, target: conn_01m4vewksnea2bwdxwdy5aekhn}\n")
	empty, target := "", "frontend"
	edited, err := WithPlacement(doc, Placement{Section: "services", Name: "web", CloudTarget: &target, Provider: &empty, Target: &empty})
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(edited)
	if err != nil {
		t.Fatal(err)
	}
	set := m.Services["web"].Settings
	if set.CloudTarget != "frontend" || set.Provider != "" || set.Target != "" {
		t.Fatalf("conflicting fields remain: %+v", set)
	}
}
