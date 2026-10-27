package recipes_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

func TestFeatureNameIsValidated(t *testing.T) {
	for _, feature := range []string{"AI!", "Ai", "a1", "abcdefghijklmnopq"} {
		k := recipestest.Featured()
		k.Feature = feature
		err := recipes.NewRegistry().Register(k)
		if err == nil {
			t.Errorf("feature %q was accepted", feature)
			continue
		}
		if !strings.Contains(err.Error(), "feature "+`"`+feature+`"`) {
			t.Errorf("feature %q: error %q doesn't name it", feature, err)
		}
	}
	r := recipes.NewRegistry()
	if err := r.Register(recipestest.Featured()); err != nil {
		t.Fatalf("feature %q: %v", recipes.FeatureAI, err)
	}
	k, _ := r.Kind("featured")
	if k.Feature != recipes.FeatureAI {
		t.Errorf("registered kind's feature = %q", k.Feature)
	}
	for _, info := range r.Kinds() {
		if info.Name == "featured" && info.Feature != recipes.FeatureAI {
			t.Errorf("KindInfo feature = %q, want %q", info.Feature, recipes.FeatureAI)
		}
	}
}

// TestExistingKindsHaveNoFeature checks that every kind built into
// Loomplane before the AI section is planned whatever the server's
// features are. The model kinds belong to AI, and only they do.
func TestExistingKindsHaveNoFeature(t *testing.T) {
	var ai []string
	for _, info := range recipes.Default().Kinds() {
		if info.Feature == recipes.FeatureAI {
			ai = append(ai, info.Name)
			continue
		}
		if info.Feature != "" {
			t.Errorf("%s belongs to feature %q", info.Name, info.Feature)
		}
		if k, ok := recipes.Default().Kind(info.Name); ok && k.Feature != "" {
			t.Errorf("kind %s belongs to feature %q", k.Name, k.Feature)
		}
	}
	slices.Sort(ai)
	if want := slices.Sorted(slices.Values(recipes.ModelKinds())); !slices.Equal(ai, want) {
		t.Errorf("AI kinds = %v, want the model kinds %v", ai, want)
	}
}
