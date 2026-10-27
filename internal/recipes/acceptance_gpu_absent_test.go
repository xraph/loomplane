//go:build !acceptance

package recipes_test

import (
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
)

// TestAcceptanceGPUKindIsNotInTheProduct proves the live qualification's
// GPU kind registers only under the acceptance build tag.
func TestAcceptanceGPUKindIsNotInTheProduct(t *testing.T) {
	if _, ok := recipes.Default().Kind("lpaccept-gpu"); ok {
		t.Fatal("lpaccept-gpu is registered in a build without the acceptance tag")
	}
	for _, info := range recipes.Default().Kinds() {
		if info.Name == "lpaccept-gpu" {
			t.Fatal("lpaccept-gpu is listed in a build without the acceptance tag")
		}
	}
}
