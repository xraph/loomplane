package recipes_test

import (
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

func TestFalkorRequirementRegistration(t *testing.T) {
	for _, modes := range [][]string{nil, {"disable"}, {"verify-full"}, {"disable", "verify-full"}} {
		k := recipestest.Sample()
		k.Dependencies[0].External = &recipes.Requirement{Engine: "falkordb", TLS: modes}
		if err := recipes.NewRegistry().Register(k); err != nil {
			t.Fatal("FalkorDB requirement rejected")
		}
	}
	for _, change := range []func(*recipes.Kind){
		func(k *recipes.Kind) { k.Dependencies[0].External.TLS = []string{"require"} },
		func(k *recipes.Kind) { k.Dependencies[0].External.TLS = []string{"prefer"} },
		func(k *recipes.Kind) { k.Dependencies[0].External.Extensions = []string{"vector"} },
		func(k *recipes.Kind) { k.Dependencies[0].External.Preload = []string{"shared_library"} },
		func(k *recipes.Kind) { k.Dependencies[0].External.CreateRole = true },
		func(k *recipes.Kind) { k.Components[1].Env[0].From.Field = "output:password" },
		func(k *recipes.Kind) { k.Components[1].Env[0].From.Field = "secret:password" },
	} {
		k := recipestest.Sample()
		k.Dependencies[0].External = &recipes.Requirement{Engine: "falkordb"}
		change(&k)
		if err := recipes.NewRegistry().Register(k); err == nil {
			t.Fatal("unsupported FalkorDB requirement accepted")
		}
	}
}
