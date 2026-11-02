package planning

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/pkg/fleet"
)

func TestArtifactInputWithoutSources(t *testing.T) {
	raw := `schema_version: 1
manifest:
  version: 1
  services:
    api:
      kind: http-service
      provider: server
      target: conn_01m4vewksnea2bwdxwdy5aekhn
      public: false
      port: 8080
      build: {image: registry.example.test/api@sha256:` + strings.Repeat("a", 64) + `}
profiles:
  small:
    isolation: namespace
    services:
      api: {cpu_millis: 500, memory_mib: 256, min_replicas: 1, max_replicas: 2}
`
	tmpl, err := fleet.ParseTemplate([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := fleet.CanonicalDigest(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := ArtifactSnapshot{ReleaseID: "release", Digest: digest, Template: *tmpl}
	in, err := ArtifactInput(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if in.Catalog || len(in.Graph.Sources) != 0 || len(in.Graph.Services) != 1 || in.Graph.Services[0].Local() != in.Manifest.Services["api"].Source {
		t.Fatalf("artifact graph: %+v", in)
	}
	if in.Manifest.Services["api"].Build.Image != tmpl.Manifest.Services["api"].Build.Image {
		t.Fatal("lost digest")
	}
	in.Environment = "production"
	in.Providers = infrastructureProviders(t)
	in.Connections = infrastructureConnections(t)
	result := Compute(in)
	if !result.Executable {
		t.Fatalf("artifact plan blocked: %+v", result.Problems)
	}
	a := action(t, result, "service:api")
	if a.Service.BuildTarget == nil || a.Service.BuildTarget.Image != tmpl.Manifest.Services["api"].Build.Image {
		t.Fatalf("planner lost pinned artifact: %+v", a.Service)
	}
	svc := tmpl.Manifest.Services["api"]
	svc.Build.Image = "changed"
	if in.Manifest.Services["api"].Build.Image == "changed" {
		t.Fatal("planner shares mutable template")
	}
	if _, err = ArtifactInput(snapshot); err == nil {
		t.Fatal("accepted tampered snapshot")
	}
}
func TestSourceProjectStillRequiresAnalysis(t *testing.T) {
	store := &unpinnedStore{plan: &Plan{ID: "plan", OrgID: "org", Status: StatusPlanning, Sources: []PlanSource{}}}
	svc := NewService(Deps{Store: store})
	status, err := svc.Plan(t.Context(), "org", "plan")
	if err != nil || status != StatusFailed || !strings.Contains(store.failed, "this plan pins no repositories") {
		t.Fatalf("unlocked source plan: %s %v %s", status, err, store.failed)
	}
	store.plan.Status = StatusReady
	store.plan.Revision = "r1"
	status, err = svc.ApplyDecision(t.Context(), "org", "plan", orchestration.Decision{Approve: true, Revision: "r1", By: "usr"})
	if err != nil || status != StatusStale {
		t.Fatalf("approved unlocked source: %s %v", status, err)
	}
}
