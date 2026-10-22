package planning

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
	"github.com/xraph/loomplane/internal/security"
)

func TestCatalogKindsRouteListsKinds(t *testing.T) {
	r := forge.NewRouter()
	if err := (&Extension{}).routes(r, nil); err != nil {
		t.Fatal(err)
	}
	for _, authenticated := range []bool{false, true} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/catalog/kinds", nil)
		if authenticated {
			req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "member"}))
		}
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		if !authenticated {
			if out.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous: %d %s", out.Code, out.Body.String())
			}
			continue
		}
		if out.Code != http.StatusOK {
			t.Fatalf("authenticated: %d %s", out.Code, out.Body.String())
		}
		var list CatalogKindList
		if err := json.Unmarshal(out.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Kinds) < 5 {
			t.Fatalf("kinds: %+v", list.Kinds)
		}
		first := list.Kinds[0]
		if first.Name != "postgres" || !first.Engine || first.Placement != recipes.PlacementServiceProject {
			t.Fatalf("first kind: %+v", first)
		}
	}
}

func TestCatalogKindListIncludesRegisteredKinds(t *testing.T) {
	list := catalogKindList(recipestest.Registry(t))
	last := list.Kinds[len(list.Kinds)-1]
	if last.Name != "sample" || last.Engine || last.Placement != recipes.PlacementAppProject {
		t.Fatalf("last kind: %+v", last)
	}
	if len(list.Kinds) != 6 || list.Kinds[0].Name != "postgres" {
		t.Fatalf("kinds: %+v", list.Kinds)
	}
}
