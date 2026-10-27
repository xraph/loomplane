package cloudinfra

import (
	"context"
	"errors"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

// perNameResolver answers TXT lookups per name. A name it doesn't hold is
// missing from public DNS.
type perNameResolver map[string][]string

func (r perNameResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	values, ok := r[name]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	return values, nil
}
func (perNameResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) { return nil, nil }

// zoneTreeStore holds an organization's zones and hands each out linked,
// the way a caller that loads the whole list sees them.
type zoneTreeStore struct {
	DomainStore
	zones []*Zone
	saved map[string]Zone
}

func (s *zoneTreeStore) Zones(_ context.Context, org string) ([]*Zone, error) {
	out := []*Zone{}
	for _, z := range s.zones {
		if z.OrgID == org {
			out = append(out, z)
		}
	}
	return out, nil
}

func (s *zoneTreeStore) Zone(ctx context.Context, org, id string) (*Zone, error) {
	zones, _ := s.Zones(ctx, org)
	LinkZoneParents(zones)
	for _, z := range zones {
		if z.ID == id {
			return z, nil
		}
	}
	return nil, ErrNotFound
}

func (s *zoneTreeStore) SaveZone(_ context.Context, z *Zone) error {
	if s.saved == nil {
		s.saved = map[string]Zone{}
	}
	s.saved[z.ID] = *z
	return nil
}

func treeZone(id, org, name string) *Zone {
	return &Zone{ID: id, OrgID: org, Name: name, Mode: "external", Revision: 1}
}

// proven gives the zone a TXT proof of its own, checked at verifiedAt when
// that isn't zero.
func proven(z *Zone, value string, verifiedAt time.Time) *Zone {
	z.ProofName, z.ProofValue, z.ProofExpiresAt, z.VerifiedAt = "_loomplane."+z.Name, value, time.Now().Add(time.Hour), verifiedAt
	return z
}

func TestLinkZoneParentsPicksTheLongestSuffix(t *testing.T) {
	root := treeZone("root", "org", "loomplane.com")
	app := treeZone("app", "org", "app.loomplane.com")
	deep := treeZone("deep", "org", "x.app.loomplane.com")
	lookalike := treeZone("lookalike", "org", "notloomplane.com")
	foreign := treeZone("foreign", "other", "app.loomplane.com")
	zones := []*Zone{deep, lookalike, root, foreign, app}
	for pass := range 2 {
		if pass == 1 {
			// Stale links from elsewhere are cleared, not kept.
			lookalike.parent, root.parent, foreign.parent = root, app, root
		}
		LinkZoneParents(zones)
		if deep.parent != app || app.parent != root || root.parent != nil {
			t.Fatalf("pass %d: chain deep=%v app=%v root=%v", pass, deep.parent, app.parent, root.parent)
		}
		if lookalike.parent != nil {
			t.Fatalf("pass %d: notloomplane.com sits under %s", pass, lookalike.parent.Name)
		}
		if foreign.parent != nil {
			t.Fatalf("pass %d: another org's zone linked to %s", pass, foreign.parent.ID)
		}
	}
	if got := nearestAncestor("y.x.app.loomplane.com", "other", zones); got != foreign {
		t.Fatalf("nearest ancestor in the other org: %v", got)
	}
	if got := nearestAncestor("loomplane.com", "org", zones); got != nil {
		t.Fatalf("a domain has no ancestor: %v", got)
	}
}

func TestAncestorNames(t *testing.T) {
	if got := ancestorNames("x.app.loomplane.com"); !slices.Equal(got, []string{"app.loomplane.com", "loomplane.com"}) {
		t.Fatalf("ancestors: %q", got)
	}
	if got := ancestorNames("loomplane.com"); len(got) != 0 {
		t.Fatalf("a domain has no ancestors: %q", got)
	}
}

func TestSubdomainsOfListsEveryZoneUnderIt(t *testing.T) {
	root := treeZone("root", "org", "loomplane.com")
	zones := []*Zone{
		treeZone("deep", "org", "x.app.loomplane.com"), treeZone("lookalike", "org", "notloomplane.com"), root,
		treeZone("foreign", "other", "api.loomplane.com"), treeZone("app", "org", "app.loomplane.com"), treeZone("api", "org", "api.loomplane.com"),
	}
	subdomains := subdomainsOf(root, zones)
	names := make([]string, 0, len(subdomains))
	for _, z := range subdomains {
		names = append(names, z.ID)
	}
	if !slices.Equal(names, []string{"api", "app", "deep"}) {
		t.Fatalf("subdomains: %q", names)
	}
}

func TestSubdomainInheritsItsDomainsVerification(t *testing.T) {
	root := proven(treeZone("root", "org", "loomplane.com"), "root-proof", time.Now())
	app := treeZone("app", "org", "app.loomplane.com")
	LinkZoneParents([]*Zone{root, app})
	if !app.verified() || !app.Verified() || app.proofZone() != root || app.ownVerified() {
		t.Fatalf("subdomain of a verified domain: verified=%v proof zone=%s", app.verified(), app.proofZone().ID)
	}
	root.VerifiedAt = time.Time{}
	if app.verified() {
		t.Fatal("subdomain verified under an unverified domain")
	}
	if app.proofZone() != root {
		t.Fatalf("a proofless subdomain waits on its domain's proof, not %s", app.proofZone().ID)
	}
	root.VerifiedAt = time.Now()
	LinkZoneParents([]*Zone{app})
	if app.verified() || app.proofZone() != app {
		t.Fatal("an unlinked subdomain verified without a proof of its own")
	}
}

func TestOwnProofStillVerifiesALegacySubdomain(t *testing.T) {
	root := proven(treeZone("root", "org", "loomplane.com"), "root-proof", time.Time{})
	app := proven(treeZone("app", "org", "app.loomplane.com"), "app-proof", time.Now())
	LinkZoneParents([]*Zone{root, app})
	if !app.verified() || app.proofZone() != app {
		t.Fatalf("own-verified subdomain: verified=%v proof zone=%s", app.verified(), app.proofZone().ID)
	}
	// A pending proof of its own is what it waits on while the domain isn't verified.
	app.VerifiedAt = time.Time{}
	if app.verified() || app.proofZone() != app {
		t.Fatalf("pending subdomain: verified=%v proof zone=%s", app.verified(), app.proofZone().ID)
	}
}

func TestHostingOnlyTimestampStillGrantsNothing(t *testing.T) {
	zone := treeZone("legacy", "org", "legacy.dev")
	zone.VerifiedAt = time.Now()
	LinkZoneParents([]*Zone{zone})
	if zone.verified() || zone.Verified() || zone.proofZone() != zone {
		t.Fatal("hosting-only verification granted routing")
	}
}

func TestVerifyZoneControlReadsTheDomainsPublicRecord(t *testing.T) {
	root := proven(treeZone("root", "org", "loomplane.com"), "root-proof", time.Now())
	app := treeZone("app", "org", "app.loomplane.com")
	LinkZoneParents([]*Zone{root, app})
	resolver := perNameResolver{"_loomplane.loomplane.com": {"root-proof"}}
	svc := NewService(Deps{Resolver: resolver})
	if err := svc.verifyZoneControl(t.Context(), app); err != nil {
		t.Fatalf("subdomain under a visible domain record: %v", err)
	}
	delete(resolver, "_loomplane.loomplane.com")
	if err := svc.verifyZoneControl(t.Context(), app); !errors.Is(err, ErrConflict) {
		t.Fatalf("subdomain passed without its domain's record: %v", err)
	}
}

func TestCheckZoneProofNeverVerifiesThroughTheParent(t *testing.T) {
	root := proven(treeZone("root", "org", "loomplane.com"), "root-proof", time.Now())
	app := proven(treeZone("app", "org", "app.loomplane.com"), "app-proof", time.Time{})
	st := &zoneTreeStore{zones: []*Zone{root, app}}
	resolver := perNameResolver{"_loomplane.loomplane.com": {"root-proof"}}
	svc := NewService(Deps{Domains: st, Resolver: resolver, Authz: cleanupTestAuth{}})
	p := security.Principal{OrgID: "org", UserID: "manager"}
	if _, err := svc.VerifyZone(t.Context(), p, "app"); !errors.Is(err, ErrConflict) {
		t.Fatalf("Check again passed through the domain's record: %v", err)
	}
	if saved, ok := st.saved["app"]; !ok || !saved.VerifiedAt.IsZero() {
		t.Fatalf("subdomain stamped from its domain: %+v", saved)
	}
	resolver["_loomplane.app.loomplane.com"] = []string{"app-proof"}
	if _, err := svc.VerifyZone(t.Context(), p, "app"); err != nil || st.saved["app"].VerifiedAt.IsZero() {
		t.Fatalf("own record visible: %v", err)
	}
}

func TestVerifiedProjectZonesIncludeInheritedSubdomains(t *testing.T) {
	root := proven(treeZone("root", "org", "loomplane.com"), "root-proof", time.Now())
	root.ProjectIDs = []string{"other"}
	app := treeZone("app", "org", "app.loomplane.com")
	app.ProjectIDs = []string{"shop"}
	legacy := treeZone("legacy", "org", "legacy.dev")
	legacy.ProjectIDs, legacy.VerifiedAt = []string{"shop"}, time.Now()
	pending := proven(treeZone("pending", "org", "pending.dev"), "pending-proof", time.Time{})
	pending.ProjectIDs = []string{"shop"}
	got := VerifiedProjectZones([]*Zone{root, app, legacy, pending}, "shop")
	want := []ProjectZone{{ID: "app", Name: "app.loomplane.com", Mode: "external", Verified: true}, {ID: "legacy", Name: "legacy.dev", Mode: "external", Verified: true}}
	if !slices.Equal(got, want) {
		t.Fatalf("project zones: %+v", got)
	}
}

func TestLinkedZoneLoadsTheOrganizationsTree(t *testing.T) {
	root := treeZone("root", "org", "loomplane.com")
	app := treeZone("app", "org", "app.loomplane.com")
	foreign := treeZone("foreign", "other", "x.app.loomplane.com")
	st := &zoneTreeStore{zones: []*Zone{root, app, foreign}}
	svc := NewService(Deps{Domains: st})
	got, err := svc.linkedZone(t.Context(), st, "org", "app")
	if err != nil || got != app || got.parent != root {
		t.Fatalf("linked zone: %+v %v", got, err)
	}
	if _, err = svc.linkedZone(t.Context(), st, "org", "foreign"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org's zone: %v", err)
	}
}
