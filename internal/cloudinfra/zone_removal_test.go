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

// racingStore runs before just ahead of each RemoveZone, the way another
// request can land between the cascade's checks and a delete. Like the
// database, it deletes only at the revision given, a zone that changed or
// went is not found, and a zone a live route uses is a plain conflict.
type racingStore struct {
	*overviewStore
	before func(id string)
}

func (s *racingStore) RemoveZone(ctx context.Context, org, id string, revision int64) error {
	if s.before != nil {
		s.before(id)
	}
	if zone, err := s.Zone(ctx, org, id); err != nil || zone.Revision != revision {
		return ErrNotFound
	}
	for _, route := range s.routes {
		if route.Input.ZoneID == id && route.Status != "removed" {
			return ErrConflict
		}
	}
	return s.overviewStore.RemoveZone(ctx, org, id, revision)
}

func racing(t *testing.T) (*Service, *overviewStore, *racingStore, *removalDNS) {
	t.Helper()
	svc, st, dns, _ := removalFixture(t)
	withoutFamilyRoutes(st)
	race := &racingStore{overviewStore: st}
	svc.Domains = race
	return svc, st, race, dns
}

func bumpRevision(st *overviewStore, id string) {
	for _, zone := range st.zones {
		if zone.ID == id {
			zone.Revision++
		}
	}
}

func TestRemoveZoneReportsAChangedSubdomainAsAConflict(t *testing.T) {
	svc, st, race, _ := racing(t)
	race.before = func(id string) {
		if id == "zone_app" {
			bumpRevision(st, id)
		}
	}
	err := svc.RemoveZone(t.Context(), overviewAdmin, "zone_root", 4)
	if errors.Is(err, ErrNotFound) || !errors.Is(err, ErrConflict) {
		t.Fatalf("a changed subdomain: %v", err)
	}
	if !strings.Contains(err.Error(), "app.loomplane.com changed since you looked, so loomplane.com was kept. Try again") {
		t.Fatalf("message: %q", err.Error())
	}
	if ids := zoneIDs(st); !slices.Contains(ids, "zone_app") || !slices.Contains(ids, "zone_root") {
		t.Fatalf("zones left: %v", ids)
	}

	// One that went is reported the same way.
	svc, st, race, _ = racing(t)
	race.before = func(id string) {
		if id == "zone_xapp" {
			st.zones = slices.DeleteFunc(st.zones, func(z *Zone) bool { return z.ID == id })
		}
	}
	err = svc.RemoveZone(t.Context(), overviewAdmin, "zone_root", 4)
	if errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "x.app.loomplane.com changed since you looked, so loomplane.com was kept") {
		t.Fatalf("a vanished subdomain: %v", err)
	}

	// The zone you asked to remove changing is still not found.
	svc, st, race, _ = racing(t)
	race.before = func(id string) {
		if id == "zone_root" {
			bumpRevision(st, id)
		}
	}
	if err = svc.RemoveZone(t.Context(), overviewAdmin, "zone_root", 4); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the requested zone: %v", err)
	}
}

func TestRemoveZoneListsAHostnameThatRacedIt(t *testing.T) {
	// The route lands on x.app just before its row goes, so the store
	// refuses, or on app before its proof record goes, so that refuses.
	for _, zoneID := range []string{"zone_xapp", "zone_app"} {
		svc, st, race, dns := racing(t)
		race.before = func(id string) {
			if id == "zone_xapp" {
				st.routes = append(st.routes, &Route{ID: "route_late", OrgID: "org", ProjectID: "shop", Status: "pending",
					Input: RouteInput{ZoneID: zoneID, Hostname: "late.loomplane.com"}})
			}
		}
		err := svc.RemoveZone(t.Context(), overviewAdmin, "zone_root", 4)
		var inUse *ZoneInUseError
		if !errors.As(err, &inUse) || !errors.Is(err, ErrConflict) {
			t.Fatalf("%s: a route that raced the removal: %v", zoneID, err)
		}
		zoneName := map[string]string{"zone_xapp": "x.app.loomplane.com", "zone_app": "app.loomplane.com"}[zoneID]
		want := BlockingHostname{RouteID: "route_late", Hostname: "late.loomplane.com", ZoneID: zoneID, ZoneName: zoneName, ProjectID: "shop", ProjectName: "Shop"}
		if inUse.Zone != "loomplane.com" || !inUse.Subdomains || len(inUse.Hostnames) != 1 || inUse.Hostnames[0] != want {
			t.Fatalf("%s: refusal: %+v", zoneID, inUse)
		}
		if ids := zoneIDs(st); !slices.Contains(ids, "zone_app") || !slices.Contains(ids, "zone_root") || len(dns.deleted) != 0 {
			t.Fatalf("%s: zones left %v, records deleted %v", zoneID, ids, dns.deleted)
		}
	}
}
