package planning

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/security"
)

type denyPoolRecovery struct{}

func (denyPoolRecovery) Require(context.Context, security.Principal, security.Permission, string) error {
	return security.ErrForbidden
}
func TestPoolRecoveryRoutesEnforceManagerAndBody(t *testing.T) {
	r := forge.NewRouter()
	e := &Extension{}
	svc := cloudinfra.NewService(cloudinfra.Deps{Authz: denyPoolRecovery{}})
	if err := e.poolRecoveryRoutes(r, nil, svc); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"preview-recovery", "recover"} {
		for _, authenticated := range []bool{false, true} {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/clouds/cloud/placements/"+path, strings.NewReader(`{"plan_id":"plan","service":"api"}`))
			req.Header.Set("Content-Type", "application/json")
			if authenticated {
				req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "member"}))
			}
			out := httptest.NewRecorder()
			r.ServeHTTP(out, req)
			want := http.StatusUnauthorized
			if authenticated {
				want = http.StatusForbidden
			}
			if out.Code != want {
				t.Fatalf("%s authenticated %v: %d %s", path, authenticated, out.Code, out.Body.String())
			}
		}
	}
}
