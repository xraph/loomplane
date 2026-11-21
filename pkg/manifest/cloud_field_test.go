package manifest

import (
	"strings"
	"testing"
)

func TestCloudFieldPrecedence(t *testing.T) {
	m, err := Parse([]byte(`version: 1
services:
  trainer:
    source: trainer
    cloud: GPU Lab
    cloud_target: a100
    environments:
      preview: {cloud_target: eu-run}
      staging: {cloud: GPU Lab EU}
      nightly: {cloud: GPU Lab EU, cloud_tags: [small]}
`))
	if err != nil {
		t.Fatal(err)
	}
	s := m.Services["trainer"]
	for env, want := range map[string]struct{ cloud, target, tags string }{
		// The service's own Cloud and target.
		"production": {"GPU Lab", "a100", ""},
		// A target named without a Cloud is in the project's default Cloud.
		"preview": {"", "eu-run", ""},
		// A Cloud alone keeps the service's target, in that Cloud.
		"staging": {"GPU Lab EU", "a100", ""},
		"nightly": {"GPU Lab EU", "", "small"},
	} {
		got := s.For(env, m.Defaults)
		if got.Cloud != want.cloud || got.CloudTarget != want.target || strings.Join(got.CloudTags, ",") != want.tags {
			t.Errorf("%s: cloud %q target %q tags %v, want %+v", env, got.Cloud, got.CloudTarget, got.CloudTags, want)
		}
	}
}

func TestCloudFieldValidation(t *testing.T) {
	for doc, want := range map[string]string{
		"services:\n  web: {source: web, cloud: GPU Lab, provider: vercel}\n":                                  "services.web.cloud",
		"services:\n  web: {source: web, cloud: ' GPU Lab'}\n":                                                 "services.web.cloud",
		"services:\n  web:\n    source: web\n    environments:\n      staging: {cloud: Lab, target: conn_1}\n": "services.web.environments.staging.cloud",
	} {
		_, err := Parse([]byte("version: 1\n" + doc))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want a finding at %s", doc, err, want)
		}
	}
	if _, err := Parse([]byte("version: 1\nservices:\n  web: {source: web, cloud: GPU Lab}\n")); err != nil {
		t.Errorf("a Cloud alone is its default placement: %v", err)
	}
}
