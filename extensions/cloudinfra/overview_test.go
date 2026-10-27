package cloudinfra

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	for _, path := range []string{"/api/v1/domains", "/api/v1/projects/shop/domains", "/api/v1/projects/shop/domain-routes/route/status", "/api/v1/dns-zones/discover?connection_id=cf", "/api/v1/dns-zones/zone"} {
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

// detailStore is one zone, example.com, with the routes given.
type detailStore struct {
	infra.DomainStore
	routes []*infra.Route
}

func (detailStore) Zones(_ context.Context, org string) ([]*infra.Zone, error) {
	return []*infra.Zone{{ID: "zone", OrgID: org, Name: "example.com", Mode: "external", ProjectIDs: []string{"shop"}, Revision: 1}}, nil
}
func (s detailStore) OrgRoutes(context.Context, string) ([]*infra.Route, error) { return s.routes, nil }
func (detailStore) LatestRouteOperations(context.Context, string, []string) (map[string]*infra.RouteOperation, error) {
	return map[string]*infra.RouteOperation{}, nil
}
func (detailStore) ProjectNames(context.Context, string) (map[string]string, error) {
	return map[string]string{"shop": "Shop"}, nil
}
func (detailStore) RuntimeNames(context.Context, string) (map[string]string, error) {
	return map[string]string{}, nil
}

func serveDomains(t *testing.T, svc *infra.Service, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := forge.NewRouter()
	e := &Extension{svc: svc}
	if err := e.domainRoutes(r, nil); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "member"}))
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	return out
}

func TestDomainZoneDetailRoute(t *testing.T) {
	var asked string
	svc := infra.NewService(infra.Deps{Authz: overviewAuthz{allow: true}, Domains: detailStore{}, Connections: overviewCredentials{&asked}})
	out := serveDomains(t, svc, http.MethodGet, "/api/v1/dns-zones/zone", "")
	if out.Code != http.StatusOK {
		t.Fatal("known zone", out.Code, out.Body.String())
	}
	var detail infra.DomainDetail
	if err := json.Unmarshal(out.Body.Bytes(), &detail); err != nil || detail.Domain.Zone == nil || detail.Domain.Zone.Name != "example.com" {
		t.Fatalf("detail: %s %v", out.Body.String(), err)
	}
	if out = serveDomains(t, svc, http.MethodGet, "/api/v1/dns-zones/missing", ""); out.Code != http.StatusNotFound {
		t.Fatal("unknown zone", out.Code)
	}
	if out = serveDomains(t, svc, http.MethodGet, "/api/v1/dns-zones/discover?connection_id=cf", ""); out.Code != http.StatusNotFound || asked != "cf" {
		t.Fatal("discover no longer reached", out.Code, asked)
	}
}

func TestRemoveDomainZoneListsBlockingHostnames(t *testing.T) {
	st := detailStore{routes: []*infra.Route{{ID: "route", OrgID: "org", ProjectID: "shop", Status: "ready", Input: infra.RouteInput{ZoneID: "zone", Hostname: "api.example.com"}}}}
	svc := infra.NewService(infra.Deps{Authz: overviewAuthz{allow: true}, Domains: st})
	out := serveDomains(t, svc, http.MethodPost, "/api/v1/dns-zones/zone/remove", `{"revision":1}`)
	if out.Code != http.StatusConflict {
		t.Fatal("removed a zone in use", out.Code, out.Body.String())
	}
	var refusal ZoneRemovalRefusal
	if err := json.Unmarshal(out.Body.Bytes(), &refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.Code != "zone_in_use" || refusal.Message != "Remove the hostnames that use example.com first." || len(refusal.Details.Hostnames) != 1 {
		t.Fatalf("refusal: %s", out.Body.String())
	}
	want := infra.BlockingHostname{RouteID: "route", Hostname: "api.example.com", ZoneID: "zone", ZoneName: "example.com", ProjectID: "shop", ProjectName: "Shop"}
	if refusal.Details.Hostnames[0] != want {
		t.Fatalf("hostname: %+v", refusal.Details.Hostnames[0])
	}
}
