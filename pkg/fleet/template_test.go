package fleet

import (
	"encoding/json"
	"strings"
	"testing"
)

func templateDocument() string {
	return `schema_version: 1
manifest:
  version: 1
  services:
    api:
      kind: http-service
      port: 8080
      build: {image: registry.example.test/app/api@sha256:` + strings.Repeat("a", 64) + `}
    worker:
      kind: worker
      build: {image: registry.example.test/app/worker@sha256:` + strings.Repeat("b", 64) + `}
profiles:
  small:
    isolation: namespace
    services:
      api: {cpu_millis: 500, memory_mib: 256, min_replicas: 1, max_replicas: 2}
      worker: {cpu_millis: 250, memory_mib: 256, min_replicas: 1, max_replicas: 1}
parameters:
  label: {kind: string, max_length: 64}
`
}

func TestTemplateRejectsUnknownFields(t *testing.T) {
	for _, raw := range []string{templateDocument() + "unexpected: true\n", strings.Replace(templateDocument(), "port: 8080", "port: 8080\n      typo: true", 1), templateDocument() + "---\nschema_version: 1\n", strings.Replace(templateDocument(), "schema_version: 1", "schema_version: 1\nschema_version: 1", 1)} {
		if _, err := ParseTemplate([]byte(raw)); err == nil {
			t.Fatal("accepted unknown, duplicate or trailing document")
		}
	}
}
func TestTemplateDigestStable(t *testing.T) {
	first, err := ParseTemplate([]byte(templateDocument()))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseTemplate(raw)
	if err != nil {
		t.Fatal(err)
	}
	a, err := CanonicalDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := CanonicalDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if a != b || !strings.HasPrefix(a, "sha256:") {
		t.Fatalf("digests %q %q", a, b)
	}
	svc := second.Manifest.Services["worker"]
	svc.Build.Image = strings.Replace(svc.Build.Image, strings.Repeat("b", 64), strings.Repeat("c", 64), 1)
	second.Manifest.Services["worker"] = svc
	c, err := CanonicalDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if c == a {
		t.Fatal("component digest did not affect release")
	}
}
func TestTemplateRequiresEveryImageDigest(t *testing.T) {
	for _, raw := range []string{strings.Replace(templateDocument(), "@sha256:"+strings.Repeat("b", 64), ":latest", 1), strings.Replace(templateDocument(), "kind: worker", "kind: worker\n      source: cmd/worker", 1), strings.Replace(templateDocument(), "kind: worker", "kind: worker\n      environments:\n        production:\n          build: {image: registry.example.test/app/worker:latest}", 1), templateDocument() + "jobs:\n  bootstrap: {phase: bootstrap, image: registry.example.test/job:latest, timeout_seconds: 30, max_attempts: 1}\n"} {
		if _, err := ParseTemplate([]byte(raw)); err == nil {
			t.Fatal("accepted mutable or source-based artifact")
		}
	}
}
func TestTemplateRejectsUnboundedParameters(t *testing.T) {
	for _, decl := range []string{"{kind: string}", "{kind: string, max_length: 65536}", "{kind: integer}", "{kind: integer, minimum: 10, maximum: 1}", "{kind: object}", "{kind: boolean, max_length: 10}"} {
		if _, err := ParseTemplate([]byte(strings.Replace(templateDocument(), "{kind: string, max_length: 64}", decl, 1))); err == nil {
			t.Fatalf("accepted %s", decl)
		}
	}
}
func TestTemplateRequiresCompleteProfiles(t *testing.T) {
	for _, raw := range []string{strings.Replace(templateDocument(), "max_replicas: 2", "max_replicas: 0", 1), strings.Replace(templateDocument(), "isolation: namespace", "isolation: arbitrary", 1), strings.Replace(templateDocument(), "      worker: {cpu_millis: 250, memory_mib: 256, min_replicas: 1, max_replicas: 1}\n", "", 1)} {
		if _, err := ParseTemplate([]byte(raw)); err == nil {
			t.Fatal("accepted incomplete or invalid profile")
		}
	}
}
