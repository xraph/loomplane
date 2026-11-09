package fleet

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTemplateLifecycleDefaultsPreservePublishedBytes(t *testing.T) {
	image := "registry.example.test/migrate@sha256:" + strings.Repeat("a", 64)
	old := `{"phase":"pre-deploy","image":"` + image + `","timeout_seconds":30,"max_attempts":1}`
	var job LifecycleJob
	if err := json.Unmarshal([]byte(old), &job); err != nil {
		t.Fatal(err)
	}
	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}
	cpu, memory := job.EffectiveResources()
	if cpu != 500 || memory != 256 {
		t.Fatal(cpu, memory)
	}
	encoded, err := json.Marshal(job)
	if err != nil || string(encoded) != old {
		t.Fatal("published job bytes changed", string(encoded), err)
	}
	release, err := ParseTemplate([]byte(templateDocument()))
	if err != nil {
		t.Fatal(err)
	}
	release.Jobs = map[string]LifecycleJob{"migrate": job}
	previous, err := CanonicalDigest(release)
	if err != nil {
		t.Fatal(err)
	}
	job.Phase = "pre_deploy"
	release.Jobs["migrate"] = job
	next, err := CanonicalDigest(release)
	if err != nil || previous == next {
		t.Fatal("new spelling silently rewrote a release", err)
	}
}
func TestTemplateLifecycleDependenciesNeedDeclaredBindings(t *testing.T) {
	release, err := ParseTemplate([]byte(templateDocument()))
	if err != nil {
		t.Fatal(err)
	}
	release.Jobs = map[string]LifecycleJob{"migrate": {Phase: "pre_deploy", Image: "registry.example.test/migrate@sha256:" + strings.Repeat("a", 64), TimeoutSeconds: 30, MaxAttempts: 1, Dependencies: []string{"missing"}}}
	if _, err = CanonicalDigest(release); err == nil {
		t.Fatal("undeclared job dependency accepted")
	}
}
