package manifest

import (
	"strings"
	"testing"
)

func TestPodGroupManifestRoundTrip(t *testing.T) {
	raw := `version: 1
services:
  api:
    source: api
    kind: http-service
    pod_group:
      surge_replicas: 1
      sidecars:
        - name: helper
          image: registry.example.test/helper@sha256:` + strings.Repeat("a", 64) + `
          architecture: arm64
          cpu_millis: 250
          memory_mib: 128
          readiness: {port: 9001}
`
	if err := compileSchema(t).Validate(yamlToJSON(t, []byte(raw))); err != nil {
		t.Fatal(err)
	}
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.Services["api"].PodGroup == nil || len(m.Services["api"].PodGroup.Sidecars) != 1 {
		t.Fatal("pod group lost during parsing")
	}
}
func TestPodGroupEnvironmentOverrideDetached(t *testing.T) {
	group := &PodGroup{SurgeReplicas: 1}
	service := Service{Settings: Settings{PodGroup: group}}
	settings := service.For("production", Defaults{})
	settings.PodGroup.SurgeReplicas = 2
	if group.SurgeReplicas != 1 {
		t.Fatal("effective settings alias operator declaration")
	}
}
