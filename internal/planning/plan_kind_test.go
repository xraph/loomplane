package planning

import (
	"encoding/json"
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
)

func TestDataBearing(t *testing.T) {
	component := func(p recipes.Protection) *ComponentConfig { return &ComponentConfig{Protection: p} }
	tests := []struct {
		name string
		a    Action
		want bool
	}{
		{"postgres resource", Action{Subject: ResourceSubject, Resource: &ResourceConfig{Kind: "postgres"}}, true},
		{"stateless composed component", Action{Subject: ResourceSubject, Resource: &ResourceConfig{Component: component(recipes.ProtectionNone)}}, false},
		{"rebuildable composed component", Action{Subject: ResourceSubject, Resource: &ResourceConfig{Component: component(recipes.ProtectionRebuildable)}}, true},
		{"catalog recipe service", Action{Subject: ServiceSubject, Service: &ServiceConfig{Catalog: &recipes.Recipe{}}}, true},
		{"rebuildable component service", Action{Subject: ServiceSubject, Service: &ServiceConfig{Component: component(recipes.ProtectionRebuildable)}}, true},
		{"plain service", Action{Subject: ServiceSubject, Service: &ServiceConfig{}}, false},
		{"gateway service", Action{Subject: ServiceSubject, Service: &ServiceConfig{Gateway: nil, Provider: "cloudrun"}}, false},
		{"deleted plain service", Action{Op: Delete, Subject: ServiceSubject}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DataBearing(tt.a); got != tt.want {
				t.Fatalf("DataBearing = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestChangesIgnoreReusedArtifacts(t *testing.T) {
	withArtifact := &ServiceConfig{Provider: "cloudrun", Artifact: &ReusedArtifact{
		Reference: "repo@sha256:" + "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90",
		CommitSHA: "abc", FromEnvironment: "staging", FromDeploymentID: "dep_1",
	}}
	without := &ServiceConfig{Provider: "cloudrun"}
	stored := func(c *ServiceConfig) json.RawMessage {
		data, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if got := changes(stored(withArtifact), without); len(got) != 0 {
		t.Fatalf("binding with an artifact against none: %+v", got)
	}
	if got := changes(stored(without), withArtifact); len(got) != 0 {
		t.Fatalf("binding without an artifact against one: %+v", got)
	}
}

func TestRecountMatchesActions(t *testing.T) {
	r := &Result{
		Actions: []Action{{Op: Create}, {Op: Update}, {Op: Keep}, {Op: Keep}},
		Summary: Summary{Delete: 9},
	}
	r.recount()
	if want := (Summary{Create: 1, Update: 1, Keep: 2}); r.Summary != want {
		t.Fatalf("summary %+v, want %+v", r.Summary, want)
	}
}

func TestNormalizeDefaultsKindToRelease(t *testing.T) {
	p := &Plan{}
	p.normalize()
	if p.Kind != KindRelease {
		t.Fatalf("kind %q", p.Kind)
	}
	p = &Plan{Kind: KindPromotion}
	p.normalize()
	if p.Kind != KindPromotion {
		t.Fatalf("kind %q", p.Kind)
	}
}
