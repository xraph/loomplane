package planning

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/graph"

	"github.com/xraph/loomplane/pkg/manifest"
)

func federationPlacementFixture(t *testing.T) FederationInput {
	t.Helper()
	in := federationPlanFixture()
	in.Providers = infrastructureProviders(t)
	private := false
	in.Manifest.Services = map[string]manifest.Service{
		"api": {Repo: "backend", Source: "cmd/api", Settings: manifest.Settings{Provider: "kubernetes", Target: targetB, Public: &private, Port: 8080}},
	}
	in.Manifest.Federation.Placements = map[string]manifest.FederationPlacement{"api-west": {Service: "api", Destination: "west"}}
	in.Destinations["a"].ConnectionID = targetA
	in.Destinations["b"].ConnectionID = targetB
	return in
}

func TestFederationPlacementReview(t *testing.T) {
	in := federationPlacementFixture(t)
	actions, findings := PlanFederation(in)
	for _, f := range findings {
		if f.Code != "federation-activation-unavailable" {
			t.Fatalf("unexpected finding: %+v", f)
		}
	}
	byID := map[string]Action{}
	for _, a := range actions {
		byID[a.ID] = a
	}
	placement, ok := byID["policy:placement:api-west"]
	if !ok || placement.ConnectionID != targetB || !slices.Contains(placement.DependsOn, "policy:destination:west") {
		t.Fatalf("missing scoped placement: %+v", placement)
	}
	if !slices.Contains(byID["policy:export:api"].DependsOn, placement.ID) {
		t.Fatal("export does not wait for its placement")
	}
	for id := range byID {
		if id != "policy:activation" && !slices.Contains(byID["policy:activation"].DependsOn, id) {
			t.Fatalf("activation omits %s", id)
		}
	}
	for _, a := range actions {
		if a.Op != Keep || a.Subject == ServiceSubject {
			t.Fatalf("preview became executable: %+v", a)
		}
		for _, dep := range a.DependsOn {
			if _, exists := byID[dep]; !exists {
				t.Fatalf("missing dependency %s", dep)
			}
		}
	}
	again, _ := PlanFederation(in)
	if !reflect.DeepEqual(actions, again) {
		t.Fatal("placement preview is not deterministic")
	}
}

func TestFederationPlacementConflicts(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		change     func(*FederationInput)
	}{
		{"other connection", "federation-placement-target", func(in *FederationInput) {
			s := in.Manifest.Services["api"]
			s.Target = targetA
			in.Manifest.Services["api"] = s
		}},
		{"other cloud", "federation-placement-provider", func(in *FederationInput) {
			s := in.Manifest.Services["api"]
			s.Provider = "server"
			in.Manifest.Services["api"] = s
		}},
		{"unknown provider", "federation-placement-provider", func(in *FederationInput) { in.Providers = nil }},
		{"disabled", "federation-placement-disabled", func(in *FederationInput) {
			s := in.Manifest.Services["api"]
			no := false
			s.Enabled = &no
			in.Manifest.Services["api"] = s
		}},
		{"held", "federation-placement-held", func(in *FederationInput) { in.Held = map[string]bool{"backend": true} }},
		{"ambiguous held repository", "federation-placement-held", func(in *FederationInput) {
			in.Held = map[string]bool{"backend": true}
			s := in.Manifest.Services["api"]
			s.Repo = ""
			in.Manifest.Services["api"] = s
		}},
		{"public export", "federation-export-public", func(in *FederationInput) {
			s := in.Manifest.Services["api"]
			yes := true
			s.Public = &yes
			in.Manifest.Services["api"] = s
		}},
		{"implicit public export", "federation-export-public", func(in *FederationInput) {
			s := in.Manifest.Services["api"]
			s.Public = nil
			in.Manifest.Services["api"] = s
		}},
		{"wrong exported port", "federation-export-port", func(in *FederationInput) {
			s := in.Manifest.Services["api"]
			s.Port = 9090
			in.Manifest.Services["api"] = s
		}},
		{"unresolved cloud", "federation-placement-cloud", func(in *FederationInput) {
			s := in.Manifest.Services["api"]
			s.CloudTarget = "shared"
			in.Manifest.Services["api"] = s
		}},
		{"resolved other account", "federation-placement-target", func(in *FederationInput) {
			in.CloudPlacements = map[string]CloudPlacement{"api": {ConnectionID: targetA}}
		}},
		{"wrong destination record", "federation-destination-not-found", func(in *FederationInput) { in.Destinations["b"].ID = "foreign" }},
		{"missing service", "federation-placement-service", func(in *FederationInput) { delete(in.Manifest.Services, "api") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := federationPlacementFixture(t)
			tc.change(&in)
			_, findings := PlanFederation(in)
			if !slices.ContainsFunc(findings, func(f Finding) bool { return f.Code == tc.code }) {
				t.Fatalf("missing %s: %+v", tc.code, findings)
			}
		})
	}
}

func TestFederationPlacementEnvironmentAndRevision(t *testing.T) {
	in := federationPlacementFixture(t)
	original := FederationRevision(in)
	in.Destinations["b"].Generation++
	if FederationRevision(in) == original {
		t.Fatal("replacement generation omitted from review")
	}
	in = federationPlacementFixture(t)
	s := in.Manifest.Services["api"]
	s.Source = "cmd/replacement"
	in.Manifest.Services["api"] = s
	if FederationRevision(in) == original {
		t.Fatal("service source omitted from review")
	}
	in = federationPlacementFixture(t)
	in.CloudPlacements = map[string]CloudPlacement{"api": {ConnectionID: targetB, GrantRevision: 1}}
	first := FederationRevision(in)
	pin := in.CloudPlacements["api"]
	pin.GrantRevision++
	in.CloudPlacements["api"] = pin
	if FederationRevision(in) == first {
		t.Fatal("shared Cloud grant revision omitted")
	}
	in = federationPlacementFixture(t)
	s = in.Manifest.Services["api"]
	s.Target = targetA
	s.Environments = map[string]manifest.Settings{"production": {Target: targetB}}
	in.Manifest.Services["api"] = s
	result := Compute(Input{Manifest: in.Manifest, Environment: in.Environment, Federation: &in, Providers: in.Providers, CloudPlacements: map[string]CloudPlacement{"api": {ConnectionID: targetB}}})
	if slices.ContainsFunc(result.Problems, func(f Finding) bool { return f.Code == "federation-placement-target" }) {
		t.Fatalf("ignored effective environment: %+v", result.Problems)
	}
	if result.Executable {
		t.Fatal("placement review bypassed activation gate")
	}
}

func TestFederationPlacementUsesResolvedCloud(t *testing.T) {
	in := federationPlacementFixture(t)
	assigned, options := cloudTargetFixture()
	svc := in.Manifest.Services["api"]
	svc.Provider, svc.Target, svc.CloudTarget = "", "", "frontend"
	in.Manifest.Services["api"] = svc
	// The named Cloud target resolves to a verified Kubernetes connection.
	assigned.Targets[0].Provider = "kubernetes"
	assigned.Targets[0].ConnectionID = targetB
	options[0] = ConnectionOption{ID: targetB, Cloud: "kubernetes", Ready: true, Details: map[string]string{"cluster_ca": "ca", "namespace_uid": "namespace"}}
	resolved, pins, err := ResolveCloudTargets(in.Manifest, in.Environment, assigned, options)
	if err != nil {
		t.Fatal(err)
	}
	input := Input{Manifest: resolved, Environment: in.Environment, Providers: in.Providers, Federation: &in, CloudPlacements: pins}
	result := Compute(input)
	for _, f := range result.Problems {
		if f.Code != "federation-activation-unavailable" {
			t.Fatalf("resolved placement rejected: %+v", f)
		}
	}
	first, err := Digest(result.Actions)
	if err != nil {
		t.Fatal(err)
	}
	pin := pins["api"]
	pin.GrantRevision++
	pins["api"] = pin
	next, err := Digest(Compute(input).Actions)
	if err != nil || first == next {
		t.Fatalf("shared Cloud permission revision was not pinned: %v", err)
	}
	pin.ConnectionID = targetA
	pins["api"] = pin
	result = Compute(input)
	if !slices.ContainsFunc(result.Problems, func(f Finding) bool { return f.Code == "federation-placement-target" }) {
		t.Fatal("Compute ignored conflicting Cloud placement")
	}
	if result.Executable {
		t.Fatal("Cloud resolution enabled federation execution")
	}
}

func TestFederationPlacementPreservesHeldBinding(t *testing.T) {
	in := federationPlacementFixture(t)
	raw, err := json.Marshal(ServiceConfig{Repo: "old-backend", Source: "old/api"})
	if err != nil {
		t.Fatal(err)
	}
	in.Bindings = []*Binding{{ID: "binding-api", Name: "api", Subject: ServiceSubject, Config: raw, Revision: "old", OrgID: in.Scope.OrgID, ProjectID: in.Scope.ProjectID, EnvironmentID: in.Scope.EnvironmentID}}
	in.Held = map[string]bool{"old-backend": true}
	input := Input{Manifest: in.Manifest, Environment: in.Environment, Federation: &in, Providers: in.Providers, Bindings: in.Bindings, Held: in.Held}
	result := Compute(input)
	if !slices.ContainsFunc(result.Problems, func(f Finding) bool { return f.Code == "federation-placement-held" }) {
		t.Fatal("moving the manifest repository bypassed the held deployed source")
	}
	first := FederationRevision(in)
	in.Bindings[0].Revision = "replacement"
	if FederationRevision(in) == first {
		t.Fatal("review omitted deployed replacement")
	}
	in = federationPlacementFixture(t)
	in.Graph = &graph.Graph{Sources: []graph.SourceRef{{Alias: "backend"}}}
	in.Held = map[string]bool{"other": true}
	svc := in.Manifest.Services["api"]
	svc.Repo = ""
	in.Manifest.Services["api"] = svc
	_, findings := PlanFederation(in)
	if slices.ContainsFunc(findings, func(f Finding) bool { return f.Code == "federation-placement-held" }) {
		t.Fatal("single known repository incorrectly treated as held")
	}
}
