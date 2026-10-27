package recipes_test

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

func TestRegisterValidatesSettings(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings []recipes.Setting
		want     string
	}{
		{"bad name", []recipes.Setting{{Name: "Model", Title: "Model", Pattern: ".+"}}, `setting name "Model"`},
		{"empty pattern", []recipes.Setting{{Name: "model", Title: "Model"}}, `setting "model" has no pattern`},
		{"pattern that doesn't compile", []recipes.Setting{{Name: "model", Title: "Model", Pattern: "("}}, `setting "model" pattern "(" doesn't compile`},
		{"duplicate", []recipes.Setting{{Name: "model", Title: "Model", Pattern: ".+"}, {Name: "model", Title: "Again", Pattern: ".+"}}, `setting "model" is declared more than once`},
		{"default that doesn't match", []recipes.Setting{{Name: "size", Title: "Size", Pattern: "^[0-9]+$", Default: "x"}}, `setting "size" default "x"`},
		{"no title", []recipes.Setting{{Name: "model", Pattern: ".+"}}, `setting "model" has no title`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := recipestest.Sample()
			k.Settings = tc.settings
			err := recipes.NewRegistry().Register(k)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Register: %v, want an error with %q", err, tc.want)
			}
		})
	}

	k := recipestest.Sample()
	k.Settings = []recipes.Setting{
		{Name: "model", Title: "Model", Pattern: "[a-z0-9-]+/[a-z0-9.-]+", Required: true},
		{Name: "size", Title: "Size", Pattern: "^[0-9]+$", Default: "1"},
	}
	r := recipes.NewRegistry()
	if err := r.Register(k); err != nil {
		t.Fatalf("Register: %v", err)
	}
	k.Settings[0].Name = "changed"
	got, _ := r.Kind("sample")
	if len(got.Settings) != 2 || got.Settings[0].Name != "model" || got.Settings[1].Default != "1" {
		t.Fatalf("registered settings: %+v", got.Settings)
	}

	// A dependency-only kind has no manifest resource to set them.
	graph := recipestest.Graph()
	graph.Settings = []recipes.Setting{{Name: "size", Title: "Size", Pattern: "[0-9]+"}}
	if err := recipes.NewRegistry().Register(graph); err == nil || !strings.Contains(err.Error(), "takes no settings") {
		t.Fatalf("dependency-only kind with settings: %v", err)
	}
}

func TestSettingMatchesTheWholeValue(t *testing.T) {
	s := recipes.Setting{Name: "size", Title: "Size", Pattern: "[0-9]+"}
	// Twice, so the second round reads the compiled pattern.
	for range 2 {
		for value, want := range map[string]bool{"1": true, "12": true, "": false, "1x": false, "x1": false} {
			if got := s.Matches(value); got != want {
				t.Errorf("Matches(%q) = %v, want %v", value, got, want)
			}
		}
		if bad := (recipes.Setting{Name: "bad", Pattern: "("}); bad.Matches("(") || bad.Matches("") {
			t.Error("a pattern that doesn't compile matched")
		}
	}
}
