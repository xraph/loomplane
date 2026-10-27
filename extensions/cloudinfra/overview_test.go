package cloudinfra

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xraph/forge"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
)

type overviewAuthz struct{ allow bool }

func (a overviewAuthz) Require(context.Context, security.Principal, security.Permission, string) error {
	if a.allow {
		return nil
	}
	return security.ErrForbidden
}

type overviewCredentials struct{ asked *string }

func (c overviewCredentials) Credential(_ context.Context, _, id string) (*connections.Connection, []byte, error) {
	*c.asked = id
	return nil, nil, connections.ErrNotFound
}

func TestDomainOverviewRoutes(t *testing.T) {
	serve := func(svc *infra.Service, path string, signedIn bool) int {
		r := forge.NewRouter()
		e := &Extension{svc: svc}
		if err := e.domainRoutes(r, nil); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody)
		if signedIn {
			req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "member"}))
		}
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out.Code
	}
	denied := infra.NewService(infra.Deps{Authz: overviewAuthz{}})
	for _, path := range []string{"/api/v1/domains", "/api/v1/projects/shop/domains", "/api/v1/projects/shop/domain-routes/route/status", "/api/v1/dns-zones/discover?connection_id=cf"} {
		if code := serve(denied, path, false); code != http.StatusUnauthorized {
			t.Fatal(path, "signed out", code)
		}
		if code := serve(denied, path, true); code != http.StatusForbidden {
			t.Fatal(path, "denied", code)
		}
	}
	var asked string
	allowed := infra.NewService(infra.Deps{Authz: overviewAuthz{allow: true}, Domains: struct{ infra.DomainStore }{}, Connections: overviewCredentials{&asked}})
	if code := serve(allowed, "/api/v1/dns-zones/discover?connection_id=cf", true); code != http.StatusNotFound || asked != "cf" {
		t.Fatal("connection_id not passed through", code, asked)
	}
	if code := serve(allowed, "/api/v1/dns-zones/discover", true); code != http.StatusUnprocessableEntity {
		t.Fatal("missing connection_id", code)
	}
}
