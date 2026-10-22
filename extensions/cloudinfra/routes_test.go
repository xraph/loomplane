package cloudinfra

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/forge"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type zoneCredentials struct{ details map[string]string }

func (c zoneCredentials) Credential(context.Context, string, string) (*connections.Connection, []byte, error) {
	return &connections.Connection{ID: "cf", OrgID: "org", Cloud: connections.Cloudflare, Details: c.details}, []byte("token"), nil
}

// missingZoneDNS hosts no zones at all.
type missingZoneDNS struct{ provider.DNSAdapter }

func (missingZoneDNS) GetZone(context.Context, provider.InfrastructureCredential, string) (provider.DNSZone, error) {
	return provider.DNSZone{}, provider.ErrInfrastructureNotFound
}

func TestCreateZoneProviderErrorStatuses(t *testing.T) {
	adapters := infra.NewAdapters()
	if err := adapters.RegisterDNS("cloudflare", missingZoneDNS{}); err != nil {
		t.Fatal(err)
	}
	create := func(details map[string]string) int {
		svc := infra.NewService(infra.Deps{Authz: overviewAuthz{allow: true}, Domains: struct{ infra.DomainStore }{}, Connections: zoneCredentials{details}, Adapters: adapters})
		r := forge.NewRouter()
		if err := (&Extension{svc: svc}).domainRoutes(r, nil); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/dns-zones", strings.NewReader(`{"name":"acme.dev","mode":"managed","connection_id":"cf","project_ids":[]}`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "member"}))
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out.Code
	}
	// The account doesn't host the zone: the dashboard says so on a 404.
	if code := create(map[string]string{"account_id": "acct"}); code != http.StatusNotFound {
		t.Fatal("zone not in the account", code)
	}
	// A connection whose account was never verified can't prove the zone.
	if code := create(map[string]string{}); code != http.StatusUnprocessableEntity {
		t.Fatal("unverified account identity", code)
	}
}
