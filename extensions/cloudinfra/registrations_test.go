package cloudinfra

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/forge"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/security"
)

func TestDomainRegistrationRoutes(t *testing.T) {
	r := forge.NewRouter()
	e := &Extension{svc: infra.NewService(infra.Deps{Authz: overviewAuthz{}})}
	if err := e.routes(r, nil); err != nil {
		t.Fatal(err)
	}
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/domain-registration/extensions?connection_id=cf"},
		{http.MethodGet, "/api/v1/domain-registration/search?connection_id=cf&q=acme"},
		{http.MethodPost, "/api/v1/domain-registration/check"},
		{http.MethodPost, "/api/v1/domain-registrations/preview"},
		{http.MethodPost, "/api/v1/domain-registrations/reg_1/approve"},
		{http.MethodGet, "/api/v1/domain-registrations"},
		{http.MethodGet, "/api/v1/domain-registrations/reg_1"},
		{http.MethodPost, "/api/v1/domain-registrations/reg_1/auto-renew"},
	} {
		for _, signedIn := range []bool{false, true} {
			req := httptest.NewRequestWithContext(t.Context(), rt.method, rt.path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			if signedIn {
				req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "member"}))
			}
			out := httptest.NewRecorder()
			r.ServeHTTP(out, req)
			if want := map[bool]int{false: http.StatusUnauthorized, true: http.StatusForbidden}[signedIn]; out.Code != want {
				t.Fatal(rt.method, rt.path, signedIn, out.Code)
			}
		}
	}
}
