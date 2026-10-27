package planning

import (
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

// featuredKinds is recipestest.Registry plus kind "featured", which
// belongs to feature "ai".
func featuredKinds(t *testing.T) *recipes.Registry {
	t.Helper()
	r := recipestest.Registry(t)
	if err := r.Register(recipestest.Featured()); err != nil {
		t.Fatal(err)
	}
	return r
}

const featuredApp = "version: 1\nresources:\n  helper: {kind: featured, provider: server, target: " + targetA + "}\n"

func featurePlan(t *testing.T, doc string, features map[string]bool) *Result {
	t.Helper()
	return Compute(Input{
		Environment: "production", Graph: infrastructureGraph(), Manifest: parse(t, doc), Providers: providers.Registry(),
		Connections: composedConnections(t), Kinds: featuredKinds(t), Features: features,
	})
}

func TestKindOfAnOffFeatureIsRefused(t *testing.T) {
	for _, features := range []map[string]bool{{"ai": false}, nil, {"other": true}} {
		r := featurePlan(t, featuredApp, features)
		if r.Executable {
			t.Fatalf("features %v: a kind of an off feature planned", features)
		}
		want := Finding{
			Code: "catalog-feature-off", Subject: ResourceSubject, Name: "helper", Path: "resources.helper",
			Message: "Featured needs AI, which is off on this Loomplane server. An operator turns it on with ai.enabled.",
		}
		if !slices.ContainsFunc(r.Problems, func(f Finding) bool {
			return f.Code == want.Code && f.Subject == want.Subject && f.Name == want.Name && f.Path == want.Path && f.Message == want.Message
		}) {
			t.Fatalf("features %v: problems %+v, want %+v", features, r.Problems, want)
		}
		for _, a := range r.Actions {
			if a.Name == "helper" || strings.HasPrefix(a.Name, "helper--") {
				t.Fatalf("features %v: planned %s for a kind of an off feature", features, a.ID)
			}
		}
	}
}

func TestKindOfAnOnFeaturePlans(t *testing.T) {
	r := featurePlan(t, featuredApp, map[string]bool{"ai": true})
	if !r.Executable {
		t.Fatalf("problems: %+v", r.Problems)
	}
	for _, id := range []string{"resource:helper--postgres", "resource:helper--migrate", "resource:helper--api", "resource:helper--worker", "resource:helper"} {
		if a := findAction(r, id); a == nil || a.Op != Create {
			t.Fatalf("%s: %+v", id, a)
		}
	}
}

func TestKindsWithoutAFeatureIgnoreFeatures(t *testing.T) {
	want := featurePlan(t, sampleApp, nil)
	if !want.Executable {
		t.Fatalf("problems: %+v", want.Problems)
	}
	for _, features := range []map[string]bool{{"ai": false}, {"ai": true}} {
		got := featurePlan(t, sampleApp, features)
		if !got.Executable || len(got.Actions) != len(want.Actions) {
			t.Fatalf("features %v: %+v", features, got)
		}
		for i := range want.Actions {
			if got.Actions[i].ID != want.Actions[i].ID || got.Actions[i].Op != want.Actions[i].Op {
				t.Fatalf("features %v: action %d %s %s, want %s %s", features, i, got.Actions[i].ID, got.Actions[i].Op, want.Actions[i].ID, want.Actions[i].Op)
			}
		}
	}
}
