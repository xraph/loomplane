package planning

import (
	"testing"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
	"github.com/xraph/loomplane/pkg/manifest"
)

func federationPlanFixture() FederationInput {
	r := reachabilityFixture()
	return FederationInput{Scope: r.Scope, Environment: "production", Now: r.Now, Manifest: &manifest.Manifest{Version: 2, Federation: &manifest.Federation{
		Destinations:   map[string]manifest.DestinationRef{"east": {ID: "a"}, "west": {ID: "b"}},
		NetworkLinks:   map[string]manifest.FederationLink{"path": {From: "east", To: "west", Transport: "imported"}},
		ServiceExports: map[string]manifest.FederationExport{"api": {Service: "api", Destination: "west", Exposure: "private", Port: 8080, Protocol: domain.HTTP, Identity: domain.WorkloadIdentity}},
	}}, Destinations: map[string]*domain.Destination{"a": r.Source, "b": r.Target}}
}

func TestFederationPlanRevision(t *testing.T) {
	in := federationPlanFixture()
	first := FederationRevision(in)
	in.Now = in.Now.Add(time.Second)
	in.Destinations["a"].UpdatedAt = in.Now
	in.Destinations["a"].Capabilities.CheckedAt = in.Now
	if next := FederationRevision(in); next != first {
		t.Fatal("observation or credential timing changed desired state")
	}
	in.Destinations["a"].Capabilities.Revision = "new qualification"
	if FederationRevision(in) == first {
		t.Fatal("capability revision omitted")
	}
	in = federationPlanFixture()
	in.Destinations["a"].Identity.Account = "different-account"
	if FederationRevision(in) == first {
		t.Fatal("physical account omitted")
	}
	actions, findings := PlanFederation(in)
	if len(actions) == 0 || len(findings) == 0 {
		t.Fatal("expected reviewable intent with activation barrier")
	}
	for _, a := range actions {
		if len(a.Changes) == 0 {
			t.Fatal("action does not pin reviewed intent")
		}
	}
}

func TestCyclicRuntimeCalls(t *testing.T) {
	in := federationPlacementFixture(t)
	svc := in.Manifest.Services["api"]
	svc.Target = targetA
	in.Manifest.Services["caller"] = svc
	in.Manifest.Federation.Placements["caller-east"] = manifest.FederationPlacement{Service: "caller", Destination: "east"}
	export := in.Manifest.Federation.ServiceExports["api"]
	export.Service, export.Destination = "caller", "east"
	in.Manifest.Federation.ServiceExports["caller"] = export
	in.Manifest.Federation.AccessGrants = map[string]manifest.FederationGrant{"a-b": {SourceService: "caller", SourceDestination: "east", Export: "api"}, "b-a": {SourceService: "api", SourceDestination: "west", Export: "caller"}}
	actions, _ := PlanFederation(in)
	graph := map[string][]string{}
	for _, action := range actions {
		graph[action.ID] = action.DependsOn
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string)
	visit = func(id string) {
		if visiting[id] {
			t.Fatalf("runtime calls created a deployment cycle at %s", id)
		}
		if visited[id] {
			return
		}
		dependencies, ok := graph[id]
		if !ok {
			t.Fatalf("missing dependency %s", id)
		}
		visiting[id] = true
		for _, dependency := range dependencies {
			visit(dependency)
		}
		visiting[id], visited[id] = false, true
	}
	visit("policy:activation")
	if len(visited) != len(actions) {
		t.Fatal("activation does not wait for all prerequisites")
	}
}

func TestFederationCannotUseLegacyExecutor(t *testing.T) {
	result := Compute(Input{Manifest: &manifest.Manifest{Version: 2}})
	if result.Executable || len(result.Problems) == 0 {
		t.Fatal("version 2 bypassed activation barrier")
	}
}
