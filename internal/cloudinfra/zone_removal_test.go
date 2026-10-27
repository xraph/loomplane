package cloudinfra

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

// removalDNS is the Vercel zone loomplane.com. It deletes records by ID and
// fails the one named failID.
type removalDNS struct {
	provider.DNSAdapter
	deleted []string
	failID  string
}

func (*removalDNS) GetZone(_ context.Context, c provider.InfrastructureCredential, name string) (provider.DNSZone, error) {
	if name != "loomplane.com" {
		return provider.DNSZone{}, provider.ErrInfrastructureNotFound
	}
	return provider.DNSZone{ID: "pz", Name: name, AccountID: c.AccountID}, nil
}
func (a *removalDNS) DeleteRecord(_ context.Context, _ provider.InfrastructureCredential, req provider.DNSDeleteRequest) error {
	if req.Expected.ID == a.failID {
		return errors.New("provider unavailable")
	}
	a.deleted = append(a.deleted, req.Expected.ID)
	return nil
}

func removalFixture(t *testing.T) (*Service, *overviewStore, *removalDNS, *recordingAudit) {
	t.Helper()
	svc, st := overviewFixture()
	withFamily(svc, st)
	dns, audit := &removalDNS{}, &recordingAudit{}
	registry := NewAdapters()
	if err := registry.RegisterDNS("vercel", dns); err != nil {
		t.Fatal(err)
	}
	svc.Adapters, svc.Connections, svc.Audit = registry, routeDNSCredentials{"vercel"}, audit
	return svc, st, dns, audit
}

// withoutFamilyRoutes drops the routes under loomplane.com.
func withoutFamilyRoutes(st *overviewStore) {
	st.routes = slices.DeleteFunc(st.routes, func(r *Route) bool { return strings.HasSuffix(r.Input.Hostname, "loomplane.com") })
}

func zoneIDs(st *overviewStore) []string {
	out := make([]string, 0, len(st.zones))
	for _, zone := range st.zones {
		out = append(out, zone.ID)
	}
	return out
}

func removals(audit *recordingAudit) []string {
	out := []string{}
	for _, e := range audit.events {
		if e.Action == "domain.zone.remove" {
			out = append(out, e.Attributes["resource_id"].(string))
		}
	}
	return out
}

func TestRemoveZoneRefusesWhileHostnamesUseItOrItsSubdomains(t *testing.T) {
	svc, st, dns, audit := removalFixture(t)
	err := svc.RemoveZone(t.Context(), overviewAdmin, "zone_root", 4)
	var inUse *ZoneInUseError
	if !errors.As(err, &inUse) || !errors.Is(err, ErrConflict) {
		t.Fatalf("removed a domain in use: %v", err)
	}
	want := BlockingHostname{RouteID: "route_api", Hostname: "api.app.loomplane.com", ZoneID: "zone_app", ZoneName: "app.loomplane.com", ProjectID: "shop", ProjectName: "Shop"}
	if inUse.Zone != "loomplane.com" || !inUse.Subdomains || len(inUse.Hostnames) != 2 || inUse.Hostnames[0] != want || inUse.Hostnames[1].Hostname != "www.x.app.loomplane.com" || inUse.Hostnames[1].ProjectName != "Docs" {
		t.Fatalf("refusal: %+v", inUse)
	}
	if err.Error() != "remove the hostnames that use loomplane.com and its subdomains first" {
		t.Fatalf("message: %q", err.Error())
	}
	if len(st.removed) != 0 || len(dns.deleted) != 0 || len(removals(audit)) != 0 {
		t.Fatalf("removed=%v deleted=%v audits=%v", st.removed, dns.deleted, removals(audit))
	}

	// A zone with no subdomains names only itself.
	err = svc.RemoveZone(t.Context(), overviewAdmin, "zone_xapp", 1)
	if !errors.As(err, &inUse) || inUse.Subdomains || len(inUse.Hostnames) != 1 || inUse.Hostnames[0].RouteID != "route_www" {
		t.Fatalf("refusal: %v %+v", err, inUse)
	}
	if err.Error() != "remove the hostnames that use x.app.loomplane.com first" {
		t.Fatalf("message: %q", err.Error())
	}
	if len(st.removed) != 0 || len(dns.deleted) != 0 {
		t.Fatalf("removed=%v deleted=%v", st.removed, dns.deleted)
	}
}

func TestRemoveZoneTakesItsSubdomainsWithIt(t *testing.T) {
	svc, st, dns, audit := removalFixture(t)
	withoutFamilyRoutes(st)
	// The revision is checked before anything is touched, whatever the mode.
	for _, id := range []string{"zone_root", "zone_docs"} {
		if err := svc.RemoveZone(t.Context(), overviewAdmin, id, 3); !errors.Is(err, ErrConflict) {
			t.Fatal("stale revision accepted", id, err)
		}
	}
	if len(st.removed) != 0 || len(dns.deleted) != 0 {
		t.Fatalf("stale removal: removed=%v deleted=%v", st.removed, dns.deleted)
	}
	if err := svc.RemoveZone(t.Context(), overviewAdmin, "zone_root", 4); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(st.removed, []string{"zone_xapp", "zone_app", "zone_root"}) {
		t.Fatalf("removal order: %v", st.removed)
	}
	if !slices.Equal(dns.deleted, []string{"rec-app", "rec-root"}) {
		t.Fatalf("records deleted: %v", dns.deleted)
	}
	if got := removals(audit); !slices.Equal(got, []string{"zone_xapp", "zone_app", "zone_root"}) {
		t.Fatalf("audits: %v", got)
	}
	if !slices.Equal(zoneIDs(st), []string{"zone", "zone_docs", "zone_rival"}) {
		t.Fatalf("zones left: %v", zoneIDs(st))
	}
}

func TestRemoveSubdomainLeavesItsDomain(t *testing.T) {
	svc, st, dns, _ := removalFixture(t)
	withoutFamilyRoutes(st)
	if err := svc.RemoveZone(t.Context(), overviewAdmin, "zone_app", 2); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(st.removed, []string{"zone_xapp", "zone_app"}) || !slices.Equal(dns.deleted, []string{"rec-app"}) {
		t.Fatalf("removed=%v deleted=%v", st.removed, dns.deleted)
	}
	if !slices.Contains(zoneIDs(st), "zone_root") {
		t.Fatal("the domain went with its subdomain", zoneIDs(st))
	}
}

func TestRemoveZoneStopsAtTheFirstZoneItCannotClean(t *testing.T) {
	svc, st, dns, audit := removalFixture(t)
	withoutFamilyRoutes(st)
	dns.failID = "rec-app"
	err := svc.RemoveZone(t.Context(), overviewAdmin, "zone_root", 4)
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "so app.loomplane.com was kept") {
		t.Fatalf("error: %v", err)
	}
	if !slices.Equal(st.removed, []string{"zone_xapp"}) || len(dns.deleted) != 0 || !slices.Equal(removals(audit), []string{"zone_xapp"}) {
		t.Fatalf("removed=%v deleted=%v audits=%v", st.removed, dns.deleted, removals(audit))
	}
	if ids := zoneIDs(st); !slices.Contains(ids, "zone_app") || !slices.Contains(ids, "zone_root") {
		t.Fatalf("zones left: %v", ids)
	}
	for _, id := range []string{"zone_app", "zone_root"} {
		if kept, _ := st.Zone(t.Context(), "org", id); kept.ProofRecord == nil {
			t.Fatal("a kept zone lost its receipt", id)
		}
	}
}
