package platform

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	nodesext "github.com/xraph/loomplane/extensions/nodes"
	"github.com/xraph/loomplane/extensions/web"
	"github.com/xraph/loomplane/internal/config"
)

// The nodes extension registers before the dashboard's catch-all, and the
// catch-all leaves /node/ and /_/ to it.
func TestNodesAreMountedAheadOfTheDashboard(t *testing.T) {
	exts := Extensions(config.Defaults())
	names := make([]string, 0, len(exts))
	for _, ext := range exts {
		names = append(names, ext.Name())
	}
	nodesAt, webAt := slices.Index(names, nodesext.Name), slices.Index(names, web.Name)
	if nodesAt < 0 || webAt < 0 || nodesAt > webAt || slices.Index(names, "loomplane-identity") > nodesAt {
		t.Fatalf("order %v", names)
	}
	dash, ok := exts[webAt].(*web.Extension)
	if !ok {
		t.Fatalf("%T", exts[webAt])
	}
	for _, path := range []string{"/node/v1/connect", "/node/v1/anything", "/_/nodes/hop"} {
		rec := httptest.NewRecorder()
		dash.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s fell through to the dashboard: %d %q", path, rec.Code, rec.Body)
		}
	}
}
