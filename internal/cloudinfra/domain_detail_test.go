package cloudinfra

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// withFamily adds loomplane.com, verified and registered, with
// app.loomplane.com under it and x.app.loomplane.com under that. All three
// sit in the Vercel zone loomplane.com. app keeps a legacy proof of its own,
// never verified; x.app has none. Shop has a ready api.app.loomplane.com
// with a DNS receipt, and docs a pending www.x.app.loomplane.com without one.
func withFamily(svc *Service, st *overviewStore) {
	now := svc.now()
	hosting := provider.DNSZone{ID: "pz", Name: "loomplane.com", AccountID: "account"}
	managed := func(id, name string) *Zone {
		return &Zone{ID: id, OrgID: "org", Name: name, Mode: "managed", ConnectionID: "dns", ConnectionGeneration: 1, Provider: "vercel", AccountID: "account",
			ProviderZone: hosting, ProjectIDs: []string{"shop"}, Revision: 1}
	}
	root := managed("zone_root", "loomplane.com")
	root.ProofName, root.ProofValue, root.ProofExpiresAt, root.VerifiedAt = "_loomplane.loomplane.com", "loomplane=root", now.Add(24*time.Hour), now
	root.ProofRecord = &provider.DNSRecord{ID: "rec-root", ZoneID: "pz", Name: "_loomplane", Type: "TXT", Value: "loomplane=root", TTL: 60}
	root.RegistrationID, root.Revision = "reg_1", 4
	app := managed("zone_app", "app.loomplane.com")
	app.ProofName, app.ProofValue, app.ProofExpiresAt = "_loomplane.app.loomplane.com", "loomplane=app", now.Add(-time.Hour)
	app.ProofRecord = &provider.DNSRecord{ID: "rec-app", ZoneID: "pz", Name: "_loomplane.app", Type: "TXT", Value: "loomplane=app", TTL: 60}
	app.Revision = 2
	st.zones = append(st.zones, root, app, managed("zone_xapp", "x.app.loomplane.com"))
	ready := RouteVerification{DNS: true, TLS: true, Backend: true, CheckedAt: now, CertExpires: now.AddDate(0, 2, 0)}
	st.routes = append(st.routes,
		&Route{ID: "route_api", OrgID: "org", ProjectID: "shop", Status: "ready", Verification: ready, Receipt: &provider.DNSRecord{ID: "rec-api", ZoneID: "pz", Name: "api.app", Type: "A", Value: "203.0.113.10", TTL: 300},
			Input: RouteInput{ZoneID: "zone_app", EnvironmentID: "prod", Service: "web", Hostname: "api.app.loomplane.com", Port: 8080}, Binding: RouteBinding{ServerID: "srv_1"}},
		&Route{ID: "route_www", OrgID: "org", ProjectID: "docs", Status: "pending",
			Input: RouteInput{ZoneID: "zone_xapp", EnvironmentID: "prod", Service: "site", Hostname: "www.x.app.loomplane.com", Port: 80}, Binding: RouteBinding{ServerID: "srv_1"}},
		&Route{ID: "route_old", OrgID: "org", ProjectID: "shop", Status: "removed", Receipt: &provider.DNSRecord{ID: "rec-old", ZoneID: "pz", Name: "old", Type: "A", Value: "203.0.113.11", TTL: 300},
			Input: RouteInput{ZoneID: "zone_root", Hostname: "old.loomplane.com"}},
	)
}

func overviewOfID(t *testing.T, zones []ZoneOverview, id string) ZoneOverview {
	t.Helper()
	for _, zone := range zones {
		if zone.Zone.ID == id {
			return zone
		}
	}
	t.Fatal("zone missing", id)
	return ZoneOverview{}
}

func TestDomainOverviewNamesParentsAndSubdomains(t *testing.T) {
	svc, st := overviewFixture()
	withFamily(svc, st)
	got, err := svc.DomainOverview(t.Context(), overviewAdmin)
	if err != nil {
		t.Fatal(err)
	}
	root := overviewOfID(t, got.Zones, "zone_root")
	if !slices.Equal(root.Subdomains, []string{"zone_app", "zone_xapp"}) || root.ParentZoneID != "" || root.ProofZoneID != "" {
		t.Fatalf("loomplane.com: parent=%q proof=%q subdomains=%v", root.ParentZoneID, root.ProofZoneID, root.Subdomains)
	}
	app := overviewOfID(t, got.Zones, "zone_app")
	if !slices.Equal(app.Subdomains, []string{"zone_xapp"}) || app.ParentZoneID != "zone_root" || app.ProofZoneID != "zone_root" {
		t.Fatalf("app: parent=%q proof=%q subdomains=%v", app.ParentZoneID, app.ProofZoneID, app.Subdomains)
	}
	leaf := overviewOfID(t, got.Zones, "zone_xapp")
	if leaf.ParentZoneID != "zone_app" || leaf.ProofZoneID != "zone_root" || leaf.Subdomains == nil || len(leaf.Subdomains) != 0 {
		t.Fatalf("x.app: parent=%q proof=%q subdomains=%#v", leaf.ParentZoneID, leaf.ProofZoneID, leaf.Subdomains)
	}
	raw, err := json.Marshal(leaf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"subdomains":[]`) || !strings.Contains(string(raw), `"parent_zone_id":"zone_app"`) || !strings.Contains(string(raw), `"proof_zone_id":"zone_root"`) {
		t.Fatalf("leaf JSON: %s", raw)
	}
	// A domain proved by its own record names no proof zone.
	raw, err = json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "proof_zone_id") || strings.Contains(string(raw), "parent_zone_id") {
		t.Fatalf("domain JSON: %s", raw)
	}
}

func TestDomainDetailCollectsSubdomainsHostnamesAndRecords(t *testing.T) {
	svc, st := overviewFixture()
	withFamily(svc, st)
	regs := &memoryRegistrations{bodies: map[string][]byte{}}
	if err := regs.CreateRegistration(t.Context(), &Registration{ID: "reg_1", OrgID: "org", Name: "loomplane.com", Status: "succeeded", Review: "secret-review", ZoneID: "zone_root"}); err != nil {
		t.Fatal(err)
	}
	svc.Registrations = regs
	got, err := svc.DomainDetail(t.Context(), overviewAdmin, "zone_root")
	if err != nil {
		t.Fatal(err)
	}
	if got.Domain.Zone.ID != "zone_root" || got.Parent != nil || got.ProofZone != nil || len(got.Domain.Hostnames) != 0 {
		t.Fatalf("domain: %+v parent=%v proof=%v", got.Domain, got.Parent, got.ProofZone)
	}
	if len(got.Subdomains) != 2 || got.Subdomains[0].Zone.ID != "zone_app" || got.Subdomains[1].Zone.ID != "zone_xapp" {
		t.Fatalf("subdomains: %+v", got.Subdomains)
	}
	if !slices.Equal(hostnames(got.Subdomains[0].Hostnames), []string{"api.app.loomplane.com"}) || !slices.Equal(hostnames(got.Subdomains[1].Hostnames), []string{"www.x.app.loomplane.com"}) {
		t.Fatalf("subdomain hostnames: %+v / %+v", got.Subdomains[0].Hostnames, got.Subdomains[1].Hostnames)
	}
	want := []ManagedRecord{
		{Kind: "proof", ZoneID: "zone_app", Type: "TXT", Name: "_loomplane.app.loomplane.com", Value: "loomplane=app", TTL: 60},
		{Kind: "proof", ZoneID: "zone_root", Type: "TXT", Name: "_loomplane.loomplane.com", Value: "loomplane=root", TTL: 60},
		{Kind: "route", ZoneID: "zone_app", Type: "A", Name: "api.app.loomplane.com", Value: "203.0.113.10", TTL: 300, RouteID: "route_api", Hostname: "api.app.loomplane.com", ProjectID: "shop", ProjectName: "Shop"},
	}
	if !reflect.DeepEqual(got.Records, want) {
		t.Fatalf("records:\n got %+v\nwant %+v", got.Records, want)
	}
	if got.Registration == nil || got.Registration.ID != "reg_1" || got.Registration.Review != "" {
		t.Fatalf("registration: %+v", got.Registration)
	}

	// A subdomain names its parent and the domain whose record proves it,
	// without their hostnames.
	sub, err := svc.DomainDetail(t.Context(), overviewAdmin, "zone_xapp")
	if err != nil {
		t.Fatal(err)
	}
	if sub.Parent == nil || sub.Parent.Zone.ID != "zone_app" || sub.Parent.Hostnames == nil || len(sub.Parent.Hostnames) != 0 {
		t.Fatalf("parent: %+v", sub.Parent)
	}
	if sub.ProofZone == nil || sub.ProofZone.Zone.ID != "zone_root" || len(sub.ProofZone.Hostnames) != 0 {
		t.Fatalf("proof zone: %+v", sub.ProofZone)
	}
	if sub.Subdomains == nil || len(sub.Subdomains) != 0 || sub.Records == nil || len(sub.Records) != 0 || sub.Registration != nil {
		t.Fatalf("leaf detail: %+v", sub)
	}
	raw, err := json.Marshal(sub)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"subdomains":[]`) || !strings.Contains(string(raw), `"records":[]`) {
		t.Fatalf("leaf JSON: %s", raw)
	}
	if st.saves != 0 {
		t.Fatal("a read saved a zone", st.saves)
	}
}

func TestDomainDetailIsOrganizationScoped(t *testing.T) {
	svc, st := overviewFixture()
	withFamily(svc, st)
	for _, id := range []string{"zone_rival", "zone_missing"} {
		if _, err := svc.DomainDetail(t.Context(), overviewAdmin, id); !errors.Is(err, ErrNotFound) {
			t.Fatal("read", id, err)
		}
	}
	if _, err := svc.DomainDetail(t.Context(), overviewDev, "zone_root"); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("a project member read a domain's details", err)
	}
}
