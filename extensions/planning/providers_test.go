package planning

import (
	"testing"

	"github.com/xraph/loomplane/internal/providers"
)

// GET /providers feeds the dashboard's provider pickers, which never offer
// Loomplane Cloud: it is reached only through its Cloud targets.
func TestListedProvidersOmitLoomplaneCloud(t *testing.T) {
	out := listedProviders(providers.Registry().List())
	if len(out.Providers) == 0 {
		t.Fatal("no providers listed")
	}
	for _, p := range out.Providers {
		if p.Name == "platform" || p.Unlisted {
			t.Fatalf("listed %+v", p)
		}
	}
}
