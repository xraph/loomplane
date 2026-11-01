package cloudprofiles

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

func TestCloudLocationUnknownFailureDomain(t *testing.T) {
	in := LocationInput{Name: "Independent datacenter", Provider: "digitalocean", AccountID: "account", ConnectionID: "conn", Region: "nyc1", CloudRevision: 1, FailureDomains: []FailureDomain{{Kind: "zone", ID: "unknown", Source: "operator"}}}
	location, err := normalizeLocation(in)
	if err != nil {
		t.Fatal(err)
	}
	if location.FailureDomains[0].Verified || location.EvidenceRevision != 0 || !location.CheckedAt.IsZero() {
		t.Fatal("label manufactured evidence")
	}
	in.FailureDomains[0].Verified = true
	in.EvidenceRevision = 1
	in.CheckedAt = time.Now()
	if _, err = normalizeLocation(in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("caller claimed verification: %v", err)
	}
}

func TestCloudLocationScopedRead(t *testing.T) {
	location := CloudLocation{ID: "location", AccountID: "private-account", ConnectionID: "private-connection", NetworkRefs: []string{"vpc-private"}, CIDRs: []string{"10.0.0.0/8"}, FailureDomains: []FailureDomain{{Kind: "zone", ID: "zone-one", Source: "provider", Verified: true, DependencyIDs: []string{"private-db"}}}}
	sanitized := projectLocation(location)
	if sanitized.AccountID != "" || sanitized.ConnectionID != "" || len(sanitized.NetworkRefs) != 0 || len(sanitized.CIDRs) != 0 || len(sanitized.FailureDomains[0].DependencyIDs) != 0 {
		t.Fatalf("private facts leaked: %+v", sanitized)
	}
	if !sanitized.FailureDomains[0].Verified || len(location.FailureDomains[0].DependencyIDs) != 1 {
		t.Fatal("summary lost evidence or mutated source")
	}
}

func TestCloudLocationImmutableResourcePlacement(t *testing.T) {
	old := CloudLocation{Provider: "digitalocean", AccountID: "account", Region: "nyc1"}
	next := old
	next.Region = "sfo3"
	if sameLocationPlacement(old, next) {
		t.Fatal("region relocation allowed")
	}
	next = old
	next.AccountID = "other"
	if sameLocationPlacement(old, next) {
		t.Fatal("account relocation allowed")
	}
}

func TestCloudLocationAttestation(t *testing.T) {
	location := CloudLocation{ID: "location", Provider: "digitalocean", AccountID: "account", Region: "nyc1", Revision: 2, FailureDomains: []FailureDomain{{Kind: "dependency", ID: "unknown", Source: "unknown"}}}
	input := LocationVerificationInput{CloudRevision: 2, Revision: 2, EvidenceRef: "audit:change-123", FailureDomains: []AttestedFailureDomain{{Kind: "zone", ID: "zone-a"}}}
	checked, err := attestLocation(location, input, "operator", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if checked.EvidenceSource != "operator-attested" || checked.VerifiedBy != "operator" || checked.EvidenceRevision != 3 || checked.FailureDomains[0].Verified || !checked.FailureDomains[1].Verified || checked.FailureDomains[1].Source != "operator-attested" {
		t.Fatalf("bad provenance: %+v", checked)
	}
	for _, ref := range []string{"", "plain text", "https://user:secret@example.com/evidence", "https://example.com/evidence?token=secret"} {
		input.EvidenceRef = ref
		if _, err = attestLocation(location, input, "operator", time.Now()); !errors.Is(err, ErrInvalid) {
			t.Fatalf("malformed evidence accepted: %q %v", ref, err)
		}
	}
}

type locationScopeProbe struct {
	Store
	LocationStore
	err   error
	reads int
}

func (st *locationScopeProbe) GetLocation(_ context.Context, org, project, cloud, id string) (*CloudLocation, error) {
	st.reads++
	if org != "org_1" || project != "proj_1" || cloud != "cloud_1" || id != "location_1" {
		return nil, ErrNotFound
	}
	return nil, st.err
}
func TestCloudLocationGrantRevocation(t *testing.T) {
	st := &locationScopeProbe{err: ErrNotFound}
	a := &authz{}
	svc := NewService(Deps{Store: st, Projects: projectLookup{}, Authz: a})
	p := security.Principal{OrgID: "org_1"}
	if _, err := svc.GetProjectLocation(t.Context(), p, "proj_1", "cloud_1", "location_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked grant ignored: %v", err)
	}
	if a.permission != security.CloudAccountRead {
		t.Fatal("project permission not checked")
	}
	a.err = security.ErrForbidden
	if _, err := svc.GetLocation(t.Context(), p, "cloud_1", "location_1"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("manager permission ignored: %v", err)
	}
	if st.reads != 1 || a.permission != security.OrganizationCloudManage {
		t.Fatal("unauthorized read reached storage")
	}
	if _, err := svc.VerifyLocation(t.Context(), p, "cloud_1", "location_1", LocationVerificationInput{}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("verification permission ignored: %v", err)
	}
}

func TestCloudLocationScopedDependencyIDs(t *testing.T) {
	for _, kind := range []string{"network", "dependency", "database", "account", "arbitrary-kind"} {
		location := CloudLocation{FailureDomains: []FailureDomain{{Kind: kind, ID: "private-resource", Source: "operator-attested", Verified: true, DependencyIDs: []string{"other-private"}}}}
		sanitized := projectLocation(location)
		if sanitized.FailureDomains[0].ID != "" || len(sanitized.FailureDomains[0].DependencyIDs) != 0 {
			t.Fatalf("%s identity leaked: %+v", kind, sanitized)
		}
		if sanitized.FailureDomains[0].Kind != kind || !sanitized.FailureDomains[0].Verified || location.FailureDomains[0].ID != "private-resource" {
			t.Fatal("dependency summary or original lost")
		}
	}
}
