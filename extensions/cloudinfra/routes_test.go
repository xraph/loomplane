package cloudinfra

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func (missingZoneDNS) ListZones(context.Context, provider.InfrastructureCredential) ([]provider.DNSZone, error) {
	return nil, nil
}

func (missingZoneDNS) GetZone(context.Context, provider.InfrastructureCredential, string) (provider.DNSZone, error) {
	return provider.DNSZone{}, provider.ErrInfrastructureNotFound
}

// noZonesStore is an organization without zones.
type noZonesStore struct{ infra.DomainStore }

func (noZonesStore) Zones(context.Context, string) ([]*infra.Zone, error) {
	return []*infra.Zone{}, nil
}

func TestCreateZoneProviderErrorStatuses(t *testing.T) {
	adapters := infra.NewAdapters()
	if err := adapters.RegisterDNS("cloudflare", missingZoneDNS{}); err != nil {
		t.Fatal(err)
	}
	create := func(details map[string]string) int {
		svc := infra.NewService(infra.Deps{Authz: overviewAuthz{allow: true}, Domains: noZonesStore{}, Connections: zoneCredentials{details}, Adapters: adapters})
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

// wildcardStore holds one verified external zone, which takes no challenge
// credential, and one wildcard route on it.
type wildcardStore struct{ infra.DomainStore }

func (wildcardStore) Zone(_ context.Context, org, id string) (*infra.Zone, error) {
	return &infra.Zone{ID: id, OrgID: org, Name: "acme.dev", Mode: "external", ProjectIDs: []string{"shop"}, VerifiedAt: time.Now(), Revision: 1,
		ProofName: "_loomplane.acme.dev", ProofValue: "proof", ProofExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (s wildcardStore) Zones(ctx context.Context, org string) ([]*infra.Zone, error) {
	zone, err := s.Zone(ctx, org, "zone")
	return []*infra.Zone{zone}, err
}
func (wildcardStore) OrgRoutes(context.Context, string) ([]*infra.Route, error) {
	return []*infra.Route{{ID: "route_w", Status: "ready", Input: infra.RouteInput{ZoneID: "zone", Hostname: "*.acme.dev"}}}, nil
}

func TestWildcardRouteStatuses(t *testing.T) {
	svc := infra.NewService(infra.Deps{Authz: overviewAuthz{allow: true}, Domains: wildcardStore{}})
	r := forge.NewRouter()
	if err := (&Extension{svc: svc}).domainRoutes(r, nil); err != nil {
		t.Fatal(err)
	}
	post := func(path, body string) (int, string) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "member"}))
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out.Code, out.Body.String()
	}
	if code, body := post("/api/v1/projects/shop/domain-routes/preview", `{"zone_id":"zone","environment_id":"prod","service":"web","hostname":"*.acme.dev","port":8080}`); code != http.StatusUnprocessableEntity || !strings.Contains(body, "DNS challenge credential") {
		t.Fatal("wildcard on a zone without a challenge credential", code, body)
	}
	if code, _ := post("/api/v1/dns-zones/zone/challenge-credential", `{"connection_id":"cf","revision":1}`); code != http.StatusUnprocessableEntity {
		t.Fatal("challenge credential on an external zone", code)
	}
	if code, _ := post("/api/v1/dns-zones/zone/challenge-credential", `{"connection_id":"","revision":1}`); code != http.StatusConflict {
		t.Fatal("detached under a wildcard route", code)
	}
}
