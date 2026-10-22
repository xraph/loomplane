package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// overviewStore keeps zones, routes and their operations in memory.
type overviewStore struct {
	DomainStore
	zones    []*Zone
	routes   []*Route
	ops      []*RouteOperation
	projects map[string]map[string]string
	runtimes map[string]map[string]string
}

func (s *overviewStore) DomainProject(_ context.Context, org, project string) error {
	if _, ok := s.projects[org][project]; ok {
		return nil
	}
	return ErrNotFound
}
func (s *overviewStore) CreateZone(_ context.Context, zone *Zone) error {
	v := *zone
	s.zones = append(s.zones, &v)
	return nil
}
func (s *overviewStore) Zone(_ context.Context, org, id string) (*Zone, error) {
	for _, zone := range s.zones {
		if zone.OrgID == org && zone.ID == id {
			v := *zone
			return &v, nil
		}
	}
	return nil, ErrNotFound
}
func (s *overviewStore) Zones(_ context.Context, org string) ([]*Zone, error) {
	out := []*Zone{}
	for _, zone := range s.zones {
		if zone.OrgID == org {
			v := *zone
			out = append(out, &v)
		}
	}
	slices.SortFunc(out, func(a, b *Zone) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}
func (s *overviewStore) SaveZone(_ context.Context, zone *Zone) error {
	for i, current := range s.zones {
		if current.OrgID == zone.OrgID && current.ID == zone.ID {
			if current.Revision != zone.Revision {
				return ErrConflict
			}
			zone.Revision++
			v := *zone
			s.zones[i] = &v
			return nil
		}
	}
	return ErrNotFound
}
func (s *overviewStore) find(keep func(*Route) bool) []*Route {
	out := []*Route{}
	for _, route := range s.routes {
		if keep(route) {
			v := *route
			out = append(out, &v)
		}
	}
	slices.SortFunc(out, func(a, b *Route) int { return strings.Compare(a.Input.Hostname, b.Input.Hostname) })
	return out
}
func (s *overviewStore) Route(_ context.Context, org, project, id string) (*Route, error) {
	found := s.find(func(r *Route) bool { return r.OrgID == org && r.ProjectID == project && r.ID == id })
	if len(found) == 0 {
		return nil, ErrNotFound
	}
	return found[0], nil
}
func (s *overviewStore) Routes(_ context.Context, org, project string) ([]*Route, error) {
	return s.find(func(r *Route) bool { return r.OrgID == org && r.ProjectID == project }), nil
}
func (s *overviewStore) OrgRoutes(_ context.Context, org string) ([]*Route, error) {
	return s.find(func(r *Route) bool { return r.OrgID == org && r.Status != "removed" }), nil
}
func (s *overviewStore) ServiceRoutes(_ context.Context, org, project, environment, service string) ([]*Route, error) {
	return s.find(func(r *Route) bool {
		return r.OrgID == org && r.ProjectID == project && r.Input.EnvironmentID == environment && r.Input.Service == service && r.Status != "removed"
	}), nil
}
func (s *overviewStore) LatestRouteOperations(_ context.Context, org string, ids []string) (map[string]*RouteOperation, error) {
	out := map[string]*RouteOperation{}
	for _, op := range s.ops {
		if op.Route.OrgID != org || !slices.Contains(ids, op.Route.ID) {
			continue
		}
		if prior := out[op.Route.ID]; prior == nil || op.CreatedAt.After(prior.CreatedAt) {
			v := *op
			out[op.Route.ID] = &v
		}
	}
	return out, nil
}
func (s *overviewStore) ProjectNames(_ context.Context, org string) (map[string]string, error) {
	return s.projects[org], nil
}
func (s *overviewStore) RuntimeNames(_ context.Context, org string) (map[string]string, error) {
	return s.runtimes[org], nil
}

type grant struct {
	permission security.Permission
	scope      string
}

// overviewAuth grants each user exactly the listed permissions.
type overviewAuth map[string][]grant

func (a overviewAuth) Require(_ context.Context, p security.Principal, permission security.Permission, scope string) error {
	if slices.Contains(a[p.UserID], grant{permission, scope}) {
		return nil
	}
	return security.ErrForbidden
}

var (
	overviewAdmin = security.Principal{OrgID: "org", UserID: "admin"}
	overviewDev   = security.Principal{OrgID: "org", UserID: "dev"}
)

func overviewFixture() (*Service, *overviewStore) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	ready := RouteVerification{DNS: true, TLS: true, Backend: true, CheckedAt: now, CertExpires: now.AddDate(0, 2, 0)}
	st := &overviewStore{
		projects: map[string]map[string]string{"org": {"shop": "Shop", "docs": "Docs"}, "other": {"rival": "Rival"}},
		runtimes: map[string]map[string]string{"org": {"srv_1": "web-1", "conn_srv": "web-1 runtime", "conn_k8s": "prod-cluster"}},
		zones: []*Zone{
			{ID: "zone", OrgID: "org", Name: "acme.dev", Mode: "managed", Provider: "cloudflare", ProjectIDs: []string{"shop"}, VerifiedAt: now, Revision: 1},
			{ID: "zone_docs", OrgID: "org", Name: "docs.dev", Mode: "external", ProjectIDs: []string{"docs"}, Revision: 1},
			{ID: "zone_rival", OrgID: "other", Name: "rival.dev", Mode: "managed", ProjectIDs: []string{"rival"}, VerifiedAt: now, Revision: 1},
		},
		routes: []*Route{
			{ID: "route_a", OrgID: "org", ProjectID: "shop", Status: "ready", Verification: ready, Input: RouteInput{ZoneID: "zone", EnvironmentID: "prod", Service: "web", Hostname: "api.acme.dev", Port: 8080}, Binding: RouteBinding{ServerID: "srv_1", ConnectionID: "conn_srv"}},
			{ID: "route_k", OrgID: "org", ProjectID: "shop", Status: "ready", Verification: ready, Input: RouteInput{ZoneID: "zone", EnvironmentID: "prod", Service: "app", Hostname: "app.acme.dev", Port: 80}, Binding: RouteBinding{Backend: RouteBackendKubernetes, ConnectionID: "conn_k8s"}},
			// Docs lost its grant on acme.dev after adding this route.
			{ID: "route_d", OrgID: "org", ProjectID: "docs", Status: "ready", Verification: ready, Input: RouteInput{ZoneID: "zone", EnvironmentID: "prod", Service: "site", Hostname: "old.acme.dev", Port: 80}, Binding: RouteBinding{ServerID: "srv_1"}},
			{ID: "route_gone", OrgID: "org", ProjectID: "shop", Status: "removed", Input: RouteInput{ZoneID: "zone", Hostname: "gone.acme.dev"}},
			{ID: "route_r", OrgID: "other", ProjectID: "rival", Status: "ready", Input: RouteInput{ZoneID: "zone_rival", Hostname: "api.rival.dev"}},
		},
	}
	svc := NewService(Deps{Domains: st, Authz: overviewAuth{
		"admin": {{security.OrganizationCloudManage, "org"}},
		"dev":   {{security.InfrastructureRead, "shop"}, {security.InfrastructureRead, "docs"}, {security.InfrastructureModify, "shop"}},
	}})
	svc.now = func() time.Time { return now }
	return svc, st
}

func hostnames(uses []HostnameUse) []string {
	out := make([]string, 0, len(uses))
	for _, use := range uses {
		out = append(out, use.Hostname)
	}
	return out
}

func TestDomainOverviewIsOrganizationScoped(t *testing.T) {
	svc, _ := overviewFixture()
	if _, err := svc.DomainOverview(t.Context(), overviewDev); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("a project member read the organization's domains", err)
	}
	got, err := svc.DomainOverview(t.Context(), overviewAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Zones) != 2 || got.Zones[0].Zone.ID != "zone" || got.Zones[1].Zone.ID != "zone_docs" {
		t.Fatalf("zones: %+v", got.Zones)
	}
	acme := got.Zones[0]
	if acme.State != DomainReady || !slices.Equal(hostnames(acme.Hostnames), []string{"api.acme.dev", "app.acme.dev", "old.acme.dev"}) {
		t.Fatalf("acme.dev: %+v", acme)
	}
	if acme.Hostnames[0].ProjectName != "Shop" || acme.Hostnames[2].ProjectName != "Docs" || acme.Hostnames[0].Status.Route != DomainReady {
		t.Fatalf("hostname uses: %+v", acme.Hostnames)
	}
	if docs := got.Zones[1]; docs.State != DomainAwaitingVerification || len(docs.Hostnames) != 0 || docs.Hostnames == nil {
		t.Fatalf("docs.dev: %+v", docs)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"registrations":[]`) || strings.Contains(string(raw), "rival") {
		t.Fatalf("overview JSON: %s", raw)
	}
}

func TestProjectDomainsListsOnlyGrantedZones(t *testing.T) {
	svc, st := overviewFixture()
	st.zones = append(st.zones, &Zone{ID: "zone_ungranted", OrgID: "org", Name: "spare.dev", Mode: "external", ProjectIDs: []string{}})
	shop, err := svc.ProjectDomains(t.Context(), overviewDev, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(shop) != 1 || shop[0].ZoneID != "zone" || shop[0].Name != "acme.dev" || shop[0].Mode != "managed" || shop[0].Provider != "cloudflare" || !shop[0].Verified {
		t.Fatalf("shop domains: %+v", shop)
	}
	if !slices.Equal(hostnames(shop[0].Hostnames), []string{"api.acme.dev", "app.acme.dev"}) {
		t.Fatalf("shop sees another project's or a removed hostname: %+v", shop[0].Hostnames)
	}
	docs, err := svc.ProjectDomains(t.Context(), overviewDev, "docs")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].ZoneID != "zone_docs" || len(docs[0].Hostnames) != 0 || docs[0].Hostnames == nil {
		t.Fatalf("docs sees a zone it lost: %+v", docs)
	}
	if _, err = svc.ProjectDomains(t.Context(), overviewDev, "rival"); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("read a project without access", err)
	}
	if _, err = svc.ProjectDomains(t.Context(), overviewDev, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty project accepted", err)
	}
}

type discoverCredentials struct{}

func (discoverCredentials) Credential(_ context.Context, org, id string) (*connections.Connection, []byte, error) {
	if org != "org" || id != "cf" {
		return nil, nil, connections.ErrNotFound
	}
	return &connections.Connection{ID: id, OrgID: org, Cloud: "cloudflare", Generation: 1, Details: map[string]string{"account_id": "account"}}, []byte("token"), nil
}

type discoverDNS struct {
	provider.DNSAdapter
	zones []provider.DNSZone
}

func (a discoverDNS) ListZones(_ context.Context, c provider.InfrastructureCredential) ([]provider.DNSZone, error) {
	if c.AccountID != "account" || c.Token != "token" {
		return nil, provider.ErrInfrastructureIdentity
	}
	return a.zones, nil
}

func TestDiscoverProviderZonesHidesAddedZones(t *testing.T) {
	svc, st := overviewFixture()
	st.zones = append(st.zones, &Zone{ID: "zone_example", OrgID: "org", Name: "example.test", Mode: "managed"}, &Zone{ID: "zone_foreign", OrgID: "other", Name: "other.test", Mode: "managed"})
	registry := NewAdapters()
	if err := registry.RegisterDNS("cloudflare", discoverDNS{zones: []provider.DNSZone{{ID: "z1", Name: "example.test", AccountID: "account", Verified: true}, {ID: "z2", Name: "other.test", AccountID: "account", Verified: true}}}); err != nil {
		t.Fatal(err)
	}
	svc.Adapters, svc.Connections = registry, discoverCredentials{}
	got, err := svc.DiscoverProviderZones(t.Context(), overviewAdmin, "cf")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Zones) != 1 || got.Zones[0].Name != "other.test" {
		t.Fatalf("discovered: %+v", got.Zones)
	}
	if _, err = svc.DiscoverProviderZones(t.Context(), overviewDev, "cf"); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("a project member listed the account's zones", err)
	}
	if _, err = svc.DiscoverProviderZones(t.Context(), overviewAdmin, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty connection accepted", err)
	}
}

func TestHostnameUseNamesWhereItRuns(t *testing.T) {
	svc, _ := overviewFixture()
	got, err := svc.DomainOverview(t.Context(), overviewAdmin)
	if err != nil {
		t.Fatal(err)
	}
	uses := got.Zones[0].Hostnames
	if uses[0].Hostname != "api.acme.dev" || uses[0].RunsOn != "web-1" || uses[0].Status.Backend != RouteBackendServer {
		t.Fatalf("server route: %+v", uses[0])
	}
	if uses[1].Hostname != "app.acme.dev" || uses[1].RunsOn != "prod-cluster" || uses[1].Status.Backend != RouteBackendKubernetes {
		t.Fatalf("kubernetes route: %+v", uses[1])
	}
	want := HostnameUse{RouteID: "route_a", Hostname: "api.acme.dev", ProjectID: "shop", ProjectName: "Shop", EnvironmentID: "prod", Service: "web", Port: 8080, RunsOn: "web-1", Status: uses[0].Status}
	if uses[0] != want {
		t.Fatalf("hostname use: %+v", uses[0])
	}
}

func TestVerifyZoneRecordsFailedChecks(t *testing.T) {
	svc, st := overviewFixture()
	now := svc.now()
	st.zones = append(st.zones, &Zone{ID: "zone_proof", OrgID: "org", Name: "proof.dev", Mode: "external", ProofName: "_loomplane.proof.dev", ProofValue: "loomplane=zone_proof-token", ProofExpiresAt: now.Add(time.Hour), Revision: 1})
	svc.Resolver = proofResolver{[]string{"something else"}}
	if _, err := svc.VerifyZone(t.Context(), overviewAdmin, "zone_proof"); !errors.Is(err, ErrConflict) {
		t.Fatal("a missing TXT record verified the zone", err)
	}
	saved, _ := st.Zone(t.Context(), "org", "zone_proof")
	if !saved.ProofCheckedAt.Equal(now) || !saved.VerifiedAt.IsZero() {
		t.Fatalf("failed check not recorded: %+v", saved)
	}
	later := now.Add(time.Minute)
	svc.now = func() time.Time { return later }
	svc.Resolver = proofResolver{[]string{"loomplane=zone_proof-token"}}
	got, err := svc.VerifyZone(t.Context(), overviewAdmin, "zone_proof")
	if err != nil {
		t.Fatal(err)
	}
	saved, _ = st.Zone(t.Context(), "org", "zone_proof")
	if !got.VerifiedAt.Equal(later) || !saved.VerifiedAt.Equal(later) || !saved.ProofCheckedAt.Equal(later) {
		t.Fatalf("successful check: %+v", saved)
	}
}

func TestServiceHostnamesForBackgroundJobs(t *testing.T) {
	svc, st := overviewFixture()
	now := svc.now()
	gateway := func(org, id, hostname, status string, v RouteVerification) *Route {
		return &Route{ID: id, OrgID: org, ProjectID: "shop", Status: status, Verification: v, Input: RouteInput{ZoneID: "zone", EnvironmentID: "prod", Service: "octo--gateway", Hostname: hostname}}
	}
	// The fixture's own api.acme.dev route belongs to the web service.
	st.routes = []*Route{
		gateway("org", "route_a", "api.acme.dev", "ready", RouteVerification{DNS: true, TLS: true, Backend: true, CertExpires: now.AddDate(0, 2, 0)}),
		gateway("org", "route_w", "*.acme.dev", "waiting", RouteVerification{DNS: true}),
		gateway("org", "route_old", "old-api.acme.dev", "removed", RouteVerification{}),
		gateway("other", "route_rival", "api.acme.dev", "ready", RouteVerification{}),
		{ID: "route_web", OrgID: "org", ProjectID: "shop", Status: "ready", Input: RouteInput{ZoneID: "zone", EnvironmentID: "prod", Service: "web", Hostname: "web.acme.dev"}},
	}
	st.ops = append(st.ops, &RouteOperation{ID: "op_w", Kind: "apply", Status: "waiting", CreatedAt: now, Receipt: &provider.DNSRecord{ID: "w"}, Route: Route{ID: "route_w", OrgID: "org"}})
	got, err := svc.ServiceHostnames(t.Context(), "org", "shop", "prod", "octo--gateway")
	if err != nil {
		t.Fatal(err)
	}
	want := []ServiceHostname{{RouteID: "route_w", ZoneID: "zone", Hostname: "*.acme.dev", Status: "provisioning"}, {RouteID: "route_a", ZoneID: "zone", Hostname: "api.acme.dev", Status: "ready"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	for _, missing := range [][4]string{{"", "shop", "prod", "x"}, {"org", "", "prod", "x"}, {"org", "shop", "", "x"}, {"org", "shop", "prod", ""}} {
		if _, err = svc.ServiceHostnames(t.Context(), missing[0], missing[1], missing[2], missing[3]); !errors.Is(err, ErrInvalid) {
			t.Fatal("empty argument accepted", missing, err)
		}
	}
	none, err := svc.ServiceHostnames(t.Context(), "org", "shop", "prod", "nothing")
	if err != nil || none == nil || len(none) != 0 {
		t.Fatal("a service without routes", none, err)
	}
}

func TestCreateZoneWithoutGrants(t *testing.T) {
	svc, st := overviewFixture()
	zone, err := svc.CreateZone(t.Context(), overviewAdmin, ZoneInput{Name: "fresh.dev", Mode: "external"})
	if err != nil {
		t.Fatal(err)
	}
	if zone.ProjectIDs == nil || len(zone.ProjectIDs) != 0 {
		t.Fatalf("grants: %#v", zone.ProjectIDs)
	}
	for _, project := range []string{"shop", "docs"} {
		domains, listErr := svc.ProjectDomains(t.Context(), overviewDev, project)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if slices.ContainsFunc(domains, func(d ProjectDomain) bool { return d.ZoneID == zone.ID }) {
			t.Fatal("an ungranted zone reached", project)
		}
	}
	for _, saved := range st.zones {
		if saved.ID == zone.ID {
			saved.VerifiedAt = svc.now()
		}
	}
	if _, err = svc.PreviewRoute(t.Context(), overviewDev, "shop", RouteInput{ZoneID: zone.ID, Hostname: "api.fresh.dev", Port: 80}); !errors.Is(err, ErrNotFound) {
		t.Fatal("route under an ungranted zone", err)
	}
	updated, err := svc.UpdateZone(t.Context(), overviewAdmin, "zone", ZoneUpdate{Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ProjectIDs == nil || len(updated.ProjectIDs) != 0 {
		t.Fatalf("revoked grants: %#v", updated.ProjectIDs)
	}
}

func TestRouteByHostnameAndStatus(t *testing.T) {
	svc, st := overviewFixture()
	st.routes = append(st.routes, &Route{ID: "route_star", OrgID: "org", ProjectID: "shop", Status: "waiting", Input: RouteInput{ZoneID: "zone", Hostname: "*.acme.dev"}})
	for hostname, id := range map[string]string{"api.acme.dev": "route_a", "*.acme.dev": "route_star"} {
		got, err := svc.RouteByHostname(t.Context(), overviewDev, "shop", hostname)
		if err != nil || got.ID != id {
			t.Fatal(hostname, got, err)
		}
	}
	for _, hostname := range []string{"gone.acme.dev", "old.acme.dev", "x.acme.dev", "API.acme.dev"} {
		if _, err := svc.RouteByHostname(t.Context(), overviewDev, "shop", hostname); !errors.Is(err, ErrNotFound) {
			t.Fatal("matched", hostname, err)
		}
	}
	if _, err := svc.RouteByHostname(t.Context(), overviewAdmin, "shop", "api.acme.dev"); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("read without project access", err)
	}
	st.ops = append(st.ops, &RouteOperation{ID: "op_rm", Kind: "remove", Status: "pending", CreatedAt: svc.now(), Route: Route{ID: "route_a", OrgID: "org"}})
	status, err := svc.RouteStatus(t.Context(), overviewDev, "shop", "route_a")
	if err != nil || status.RouteID != "route_a" || status.Route != DomainCleanupPending || status.OperationID != "op_rm" {
		t.Fatal(status, err)
	}
	if _, err = svc.RouteStatus(t.Context(), overviewDev, "shop", "route_gone"); !errors.Is(err, ErrNotFound) {
		t.Fatal("status of a removed route", err)
	}
	if _, err = svc.RouteStatus(t.Context(), overviewDev, "shop", "route_d"); !errors.Is(err, ErrNotFound) {
		t.Fatal("status of another project's route", err)
	}
}
