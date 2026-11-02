//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

func (fleetAuth) RequireCurrentMembership(_ context.Context, p security.Principal) error {
	if p.OrgID != "org" || p.UserID != "usr" || p.IsServiceAccount() {
		return security.ErrForbidden
	}
	return nil
}

type fleetCredentialVerifier struct{ revoked bool }

func (v *fleetCredentialVerifier) ValidatePrincipal(_ context.Context, p security.Principal) error {
	if v.revoked || !p.IsFleet() || p.KeyID != "credential" {
		return security.ErrForbidden
	}
	return nil
}

type fleetAuthorityFixture struct {
	fleetFixture
	keys      *fleetCredentialVerifier
	principal security.Principal
	grant     *fleet.Grant
	request   fleets.OperationRequest
}

func newFleetAuthorityFixture(t *testing.T, limits ...fleet.GrantLimits) fleetAuthorityFixture {
	t.Helper()
	f := newFleetFixture(t, 10)
	keys := &fleetCredentialVerifier{}
	service := fleets.NewService(fleets.Deps{Store: f.store, Projects: f.projects, Authz: fleetAuth{}, Keys: keys})
	f.service = service
	release, err := service.PublishRelease(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, f.scope.FleetID, fleetReleaseDocument())
	if err != nil {
		t.Fatal(err)
	}
	budget := fleet.GrantLimits{MaxInstances: 2, CPUMillis: 2000, MemoryMiB: 1024, StorageGiB: 100, HourlyCostMicros: 1000000, Currency: "USD"}
	if len(limits) > 0 {
		budget = limits[0]
	}
	grant, err := service.CreateGrant(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, f.scope.FleetID, fleet.CreateGrantInput{CredentialID: "credential", StageID: f.stage.ID, ReleaseIDs: []string{release.ID}, ProfileIDs: []string{"small"}, CloudIDs: []string{"cloud"}, Regions: []string{"region-a"}, Actions: []string{"create"}, ExpiresAt: time.Now().Add(time.Hour), Limits: budget})
	if err != nil {
		t.Fatal(err)
	}
	p := security.Principal{OrgID: "org", UserID: security.FleetSubject("credential"), KeyID: "credential", Kind: security.KindFleet, Fleet: &security.FleetScope{ProjectID: "proj", FleetID: f.scope.FleetID}}
	req := fleets.OperationRequest{GrantID: grant.ID, Kind: "create", StageID: f.stage.ID, ReleaseID: release.ID, ProfileID: "small", CloudID: "cloud", Region: "region-a", ExternalRef: "a", IdempotencyKey: "create-a", SnapshotDigest: release.Digest, Demand: fleets.OperationDemand{CPUMillis: 1000, MemoryMiB: 512, StorageGiB: 1, HourlyCostMicros: 10000, Currency: "USD", PriceKnown: true}}
	return fleetAuthorityFixture{f, keys, p, grant, req}
}
func TestFleetCredentialCannotEditGrant(t *testing.T) {
	f := newFleetAuthorityFixture(t)
	if _, err := f.service.CreateGrant(t.Context(), f.principal, f.scope.FleetID, f.grant.Input); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("service changed grant: %v", err)
	}
	if err := f.service.RevokeGrant(t.Context(), f.principal, f.scope.FleetID, f.grant.ID, f.grant.Revision); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("service revoked grant: %v", err)
	}
}
func TestFleetGrantRevokedDuringWait(t *testing.T) {
	f := newFleetAuthorityFixture(t)
	a, err := f.service.AuthorizeOperation(t.Context(), f.principal, f.scope, f.request)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.RevalidateAuthority(t.Context(), *a); err != nil {
		t.Fatal(err)
	}
	forged := *a
	forged.Request.CloudID = "other"
	if err = f.service.RevalidateAuthority(t.Context(), forged); err == nil {
		t.Fatal("authority fields were caller controlled")
	}
	if err = f.service.RevokeGrant(t.Context(), security.Principal{OrgID: "org", UserID: "usr"}, f.scope.FleetID, f.grant.ID, f.grant.Revision); err != nil {
		t.Fatal(err)
	}
	if err = f.service.RevalidateAuthority(t.Context(), *a); !errors.Is(err, fleets.ErrAuthorityRevoked) {
		t.Fatalf("revoked grant survived: %v", err)
	}
}
func TestFleetGrantScopeMismatch(t *testing.T) {
	f := newFleetAuthorityFixture(t)
	for _, change := range []func(*fleets.OperationRequest){func(r *fleets.OperationRequest) { r.CloudID = "other" }, func(r *fleets.OperationRequest) { r.ReleaseID = "other" }, func(r *fleets.OperationRequest) { r.Region = "other" }, func(r *fleets.OperationRequest) { r.Kind = "delete" }, func(r *fleets.OperationRequest) { r.Demand.PriceKnown = false }} {
		req := f.request
		change(&req)
		if _, err := f.service.AuthorizeOperation(t.Context(), f.principal, f.scope, req); err == nil {
			t.Fatalf("out-of-grant request accepted: %+v", req)
		}
	}
	scope := f.scope
	scope.FleetID = "other"
	if _, err := f.service.AuthorizeOperation(t.Context(), f.principal, scope, f.request); !errors.Is(err, fleets.ErrNotFound) {
		t.Fatalf("fleet scope leaked: %v", err)
	}
	p := f.principal
	p.ReadOnly = true
	if _, err := f.service.AuthorizeOperation(t.Context(), p, f.scope, f.request); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("read-only mutation: %v", err)
	}
}
func TestFleetGrantBoundsConcurrent(t *testing.T) {
	f := newFleetAuthorityFixture(t)
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Go(func() {
			r := f.request
			r.ExternalRef = fmt.Sprintf("instance-%d", n)
			r.IdempotencyKey = r.ExternalRef
			_, err := f.service.AuthorizeOperation(t.Context(), f.principal, f.scope, r)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, fleets.ErrCapacity) {
			t.Fatal(err)
		}
	}
	if accepted != 2 {
		t.Fatalf("accepted %d, want 2", accepted)
	}
}
func TestFleetAuthorityReplayAndCredentialRevocation(t *testing.T) {
	f := newFleetAuthorityFixture(t)
	a, err := f.service.AuthorizeOperation(t.Context(), f.principal, f.scope, f.request)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.service.AuthorizeOperation(t.Context(), f.principal, f.scope, f.request)
	if err != nil || b.ID != a.ID {
		t.Fatalf("authority replay: %+v %v", b, err)
	}
	req := f.request
	req.ExternalRef = "changed"
	if _, err = f.service.AuthorizeOperation(t.Context(), f.principal, f.scope, req); !errors.Is(err, fleets.ErrConflict) {
		t.Fatalf("changed replay: %v", err)
	}
	f.keys.revoked = true
	if err = f.service.RevalidateAuthority(t.Context(), *a); err == nil {
		t.Fatal("revoked credential kept authority")
	}
}

func TestFleetGrantResourceBoundIndependentOfInstanceLimit(t *testing.T) {
	f := newFleetAuthorityFixture(t, fleet.GrantLimits{MaxInstances: 10, CPUMillis: 1000, MemoryMiB: 5120, StorageGiB: 100, HourlyCostMicros: 1000000, Currency: "USD"})
	if _, err := f.service.AuthorizeOperation(t.Context(), f.principal, f.scope, f.request); err != nil {
		t.Fatal(err)
	}
	next := f.request
	next.ExternalRef = "next"
	next.IdempotencyKey = "next"
	if _, err := f.service.AuthorizeOperation(t.Context(), f.principal, f.scope, next); !errors.Is(err, fleets.ErrCapacity) {
		t.Fatalf("CPU bound bypassed: %v", err)
	}
}
func TestFleetGrantPolicyChangedDuringWait(t *testing.T) {
	f := newFleetAuthorityFixture(t)
	a, err := f.service.AuthorizeOperation(t.Context(), f.principal, f.scope, f.request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `UPDATE loomplane_fleet_stages SET policy_revision=policy_revision+1 WHERE id=$1`, f.stage.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.service.RevalidateAuthority(t.Context(), *a); !errors.Is(err, fleets.ErrAuthorityRevoked) {
		t.Fatalf("stale stage policy accepted: %v", err)
	}
}
