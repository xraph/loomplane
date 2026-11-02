package planning

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/security"
)

type providerRouteFake struct {
	err       error
	principal security.Principal
	id        string
	dsn       string
	calls     int
}

func (f *providerRouteFake) List(_ context.Context, p security.Principal) (*providerresources.ProviderResourceList, error) {
	f.principal = p
	f.calls++
	return &providerresources.ProviderResourceList{Resources: []*providerresources.ProviderResource{}, Syncs: []*providerresources.ProviderResourceSync{}, SupportedProviders: []string{"digitalocean"}}, f.err
}
func (f *providerRouteFake) Get(_ context.Context, p security.Principal, id string) (*providerresources.ProviderResource, error) {
	f.principal = p
	f.id = id
	f.calls++
	return &providerresources.ProviderResource{ID: id, OrgID: p.OrgID, Ownership: "external"}, f.err
}
func (f *providerRouteFake) Sync(_ context.Context, p security.Principal, id string) (*providerresources.ProviderResourceSync, error) {
	f.principal = p
	f.id = id
	f.calls++
	return &providerresources.ProviderResourceSync{ID: "sync", OrgID: p.OrgID, ConnectionID: id, Status: "complete"}, f.err
}
func (f *providerRouteFake) Connect(_ context.Context, p security.Principal, id string, in providerresources.ConnectInput) (*providerresources.ProviderResource, error) {
	f.principal = p
	f.id = id
	f.dsn = in.DSN
	f.calls++
	return &providerresources.ProviderResource{ID: id, OrgID: p.OrgID, EndpointID: "endpoint"}, f.err
}
func TestProviderResourceRoutesAuthorizeAndPreserveScope(t *testing.T) {
	f := &providerRouteFake{}
	r := forge.NewRouter()
	if err := registerProviderResourceRoutes(r, nil, f); err != nil {
		t.Fatal(err)
	}
	paths := []struct{ method, path, body string }{{http.MethodGet, "/api/v1/provider-resources", ""}, {http.MethodGet, "/api/v1/provider-resources/resource", ""}, {http.MethodPost, "/api/v1/connections/connection/resource-syncs", ""}, {http.MethodPut, "/api/v1/provider-resources/resource/connection", `{"dsn":"postgres://u:TOPSECRET@db.example/app?sslmode=require"}`}}
	for _, p := range paths {
		out := send(t, r, p.method, p.path, p.body, false)
		if out.Code != http.StatusUnauthorized || f.calls != 0 {
			t.Fatalf("unauthorized %s: %d %s", p.path, out.Code, out.Body.String())
		}
	}
	for _, p := range paths {
		out := send(t, r, p.method, p.path, p.body, true)
		if out.Code != http.StatusOK || strings.Contains(out.Body.String(), "TOPSECRET") {
			t.Fatalf("%s: %d %s", p.path, out.Code, out.Body.String())
		}
		if f.principal.OrgID != "org" || f.principal.UserID != "ada" {
			t.Fatalf("scope lost: %+v", f.principal)
		}
	}
	if f.id != "resource" || !strings.Contains(f.dsn, "TOPSECRET") {
		t.Fatal("connect input was not delivered to authorized service")
	}
}
func TestProviderResourceRoutesMapSafeErrors(t *testing.T) {
	f := &providerRouteFake{}
	r := forge.NewRouter()
	if err := registerProviderResourceRoutes(r, nil, f); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		err    error
		status int
	}{{security.ErrForbidden, 403}, {providerresources.ErrNotFound, 404}, {providerresources.ErrInvalid, 422}, {providerresources.ErrChanged, 409}, {providerresources.ErrUnverified, 409}, {providerresources.ErrDiscovery, 503}, {errors.New("postgres://u:TOPSECRET@db.example"), 500}} {
		f.err = test.err
		out := send(t, r, http.MethodGet, "/api/v1/provider-resources", "", true)
		if out.Code != test.status || strings.Contains(out.Body.String(), "TOPSECRET") {
			t.Fatalf("error %v: %d %s", test.err, out.Code, out.Body.String())
		}
	}
	if out := send(t, r, http.MethodPut, "/api/v1/provider-resources/resource/connection", `not json`, true); out.Code != 400 {
		t.Fatalf("invalid JSON: %d", out.Code)
	}
}
