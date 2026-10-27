//go:build !acceptance

package planning

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/security"
)

// TestCatalogKindsRouteHidesAcceptanceGPUKind proves catalogKinds.list
// never offers the live qualification's GPU kind outside the acceptance
// build.
func TestCatalogKindsRouteHidesAcceptanceGPUKind(t *testing.T) {
	r := forge.NewRouter()
	if err := (&Extension{}).routes(r, nil); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/catalog/kinds", nil)
	req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "member"}))
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	if out.Code != http.StatusOK {
		t.Fatalf("list: %d %s", out.Code, out.Body.String())
	}
	var list CatalogKindList
	if err := json.Unmarshal(out.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, k := range list.Kinds {
		if k.Name == "lpaccept-gpu" {
			t.Fatal("catalogKinds.list offers lpaccept-gpu without the acceptance tag")
		}
	}
}
