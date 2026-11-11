package manifest

import (
	"strings"
	"testing"
)

func TestVercelCommandsPreserveEmptyAndEnvironmentOverride(t *testing.T) {
	doc := []byte("version: 1\nservices:\n  web:\n    source: apps/web\n    provider: vercel\n    commands: {install: npm ci, build: npm run build}\n    environments:\n      preview:\n        commands: {build: ''}\n")
	m, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	c := m.Services["web"].For("preview", m.Defaults).Commands
	if c.Build == nil || *c.Build != "" || c.Install != nil {
		t.Fatalf("override = %+v", c)
	}
	changed, err := WithPlacement(doc, Placement{Section: "services", Name: "web", Environment: "preview", Commands: &BuildCommands{}})
	if err != nil {
		t.Fatal(err)
	}
	m, err = Parse(changed)
	if err != nil {
		t.Fatal(err)
	}
	if c = m.Services["web"].For("preview", m.Defaults).Commands; c == nil || c.Build != nil || c.Install != nil {
		t.Fatalf("reset = %+v", c)
	}
	if !strings.Contains(string(changed), "npm ci") {
		t.Fatal("edited the base commands")
	}
}

func TestVercelSettingsValidation(t *testing.T) {
	for _, settings := range []string{
		"vercel: {services: true, root: ../private}",
		"vercel: {services: false, service_commands: {web: {build: test}}}",
		"vercel: {services: true}, commands: {build: test}",
		"commands: {build: '" + strings.Repeat("x", 4097) + "'}",
	} {
		if _, err := Parse([]byte("version: 1\nservices:\n  web: {source: apps/web, " + settings + "}\n")); err == nil {
			t.Fatalf("accepted %s", settings)
		}
	}
}
