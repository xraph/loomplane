package recipes_test

import (
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

func TestSearchRequirementRegistration(t *testing.T) {
	for _, tc := range []struct {
		engine string
		modes  []string
		valid  bool
	}{
		{"elasticsearch", nil, true}, {"elasticsearch", []string{"disable"}, true}, {"elasticsearch", []string{"verify-full"}, true}, {"elasticsearch", []string{"disable", "verify-full"}, true},
		{"opensearch", nil, true}, {"opensearch", []string{"verify-full"}, true},
		{"opensearch", []string{"disable"}, false}, {"opensearch", []string{"require"}, false}, {"elasticsearch", []string{"require"}, false},
		{"elasticsearch", []string{"prefer"}, false}, {"opensearch", []string{"unknown"}, false}, {"duckdb", nil, false},
	} {
		k := recipestest.Sample()
		k.Dependencies[0].External = &recipes.Requirement{Engine: tc.engine, TLS: tc.modes}
		k.Components[1].Env = append(k.Components[1].Env, recipes.EnvVar{Name: "SEARCH_CA_CERT", From: &recipes.Ref{Dependency: "postgres", Field: recipes.FieldCACert}})
		err := recipes.NewRegistry().Register(k)
		if (err == nil) != tc.valid {
			t.Fatalf("engine %s modes %v validity %t: %v", tc.engine, tc.modes, tc.valid, err)
		}
	}
}

func TestSearchRequirementsRejectPostgresRightsAndPrivateFields(t *testing.T) {
	for _, engine := range []string{"elasticsearch", "opensearch"} {
		for _, change := range []func(*recipes.Kind){
			func(k *recipes.Kind) { k.Dependencies[0].External.Extensions = []string{"vector"} },
			func(k *recipes.Kind) { k.Dependencies[0].External.Preload = []string{"shared_library"} },
			func(k *recipes.Kind) { k.Dependencies[0].External.CreateRole = true },
			func(k *recipes.Kind) { k.Components[1].Env[0].From.Field = "output:password" },
			func(k *recipes.Kind) { k.Components[1].Env[0].From.Field = "secret:password" },
		} {
			k := recipestest.Sample()
			k.Dependencies[0].External = &recipes.Requirement{Engine: engine, TLS: []string{"verify-full"}}
			change(&k)
			if err := recipes.NewRegistry().Register(k); err == nil {
				t.Fatal("accepted unsupported search contract")
			}
		}
	}
}
