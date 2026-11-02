package digitalocean

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestDOKSOptionsCatalogFailures(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"missing", "{}"},
		{"nil", "{\"options\":null}"},
		{"duplicate", `{"options":{"regions":[{"slug":"nyc1"},{"slug":"nyc1"}],"versions":[{"slug":"v"}],"sizes":[{"slug":"doks"}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := doksFixture(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, tc.body) })
			if _, err := c.Options(t.Context(), doksAccess(), "nyc1"); !errors.Is(err, provider.ErrInfrastructureInvalid) {
				t.Fatal(err)
			}
		})
	}
	for _, resource := range []string{"sizes", "vpcs"} {
		t.Run(resource, func(t *testing.T) {
			c := doksFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v2/kubernetes/options":
					_, _ = fmt.Fprint(w, `{"options":{"regions":[{"slug":"nyc1"}],"versions":[{"slug":"v"}],"sizes":[{"slug":"doks"}]}}`)
				case "/v2/sizes":
					if resource == "sizes" {
						_, _ = fmt.Fprint(w, `{"sizes":[],"links":{"pages":{"next":"?page=1"}}}`)
					} else {
						_, _ = fmt.Fprint(w, `{"sizes":[]}`)
					}
				case "/v2/vpcs":
					_, _ = fmt.Fprint(w, `{"vpcs":[],"links":{"pages":{"next":"?page=1"}}}`)
				default:
					t.Fatal(r.URL.Path)
				}
			})
			if _, err := c.Options(t.Context(), doksAccess(), "nyc1"); !errors.Is(err, provider.ErrInfrastructurePagination) {
				t.Fatal(err)
			}
		})
	}
}
