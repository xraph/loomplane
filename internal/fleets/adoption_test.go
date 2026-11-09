package fleets

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

type adoptionStore struct{ Store }

func (adoptionStore) GetFleet(_ context.Context, org, id string) (*fleet.Fleet, error) {
	if org != "org" || id != "fleet" {
		return nil, ErrNotFound
	}
	return &fleet.Fleet{ID: id, OrgID: org, ProjectID: "project", Revision: 2, Status: "active"}, nil
}

type adoptionAuth struct{ err error }

func (a adoptionAuth) Require(context.Context, security.Principal, security.Permission, string) error {
	return a.err
}

type adoptionReader struct {
	snapshot AdoptionInventorySnapshot
	err      error
}

func (r adoptionReader) InspectAdoption(context.Context, security.Principal, FleetScope, fleet.AdoptionInput) (AdoptionInventorySnapshot, error) {
	return r.snapshot, r.err
}
func adoptionFixture() (*Service, security.Principal, fleet.AdoptionInput, AdoptionInventorySnapshot) {
	now := time.Now().UTC()
	in := fleet.AdoptionInput{CloudID: "cloud", ClusterID: "cluster", AttachmentID: "attachment", AttachmentRevision: 3, Namespace: "existing", NamespaceUID: "uid-1", OldControllerID: "controller", OldAuthorityID: "authority", OwnershipEpoch: 4, Resources: []fleet.AdoptionResourceInput{{InventoryID: "resource", ExternalID: "native-id", Incarnation: "created-1", Revision: 5, Ownership: "attached"}}}
	snap := AdoptionInventorySnapshot{Namespace: AdoptionNamespaceObservation{CloudID: "cloud", ClusterID: "cluster", AttachmentID: "attachment", AttachmentRevision: 3, Namespace: "existing", NamespaceUID: "uid-1", ObservedAt: now}, Resources: []AdoptionResourceObservation{{ID: "resource", OrgID: "org", Provider: "example", ConnectionID: "connection", ConnectionGeneration: 1, AccountIdentity: "account", Scope: "scope", ExternalID: "native-id", Incarnation: "created-1", Revision: 5, ObservedAt: now, ObservationState: "observed", Ownership: "external"}}}
	s := NewService(Deps{Store: adoptionStore{}, Authz: adoptionAuth{}})
	s.adoptionInventory = adoptionReader{snapshot: snap}
	return s, security.Principal{OrgID: "org", UserID: "operator"}, in, snap
}
func hasAdoptionBlocker(r *fleet.AdoptionReview, code string) bool {
	for _, b := range r.Blockers {
		if b.Code == code {
			return true
		}
	}
	return false
}
func TestFleetAdoptionNoWriteDuringPreview(t *testing.T) {
	s, p, in, _ := adoptionFixture()
	// The embedded store has no write implementation. A write panics here.
	got, err := s.PreviewAdoption(t.Context(), p, "fleet", in)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || got.Revision != 2 || got.CanApply || !got.RequiresRecreateCutover || !hasAdoptionBlocker(got, "old_writer_fencing_unavailable") {
		t.Fatalf("unsafe review: %+v", got)
	}
	again, err := s.PreviewAdoption(t.Context(), p, "fleet", in)
	if err != nil || again.ID != got.ID {
		t.Fatalf("unstable review: %+v %v", again, err)
	}
}
func TestFleetAdoptionStaleNamespace(t *testing.T) {
	for _, uid := range []string{"", "uid-recreated"} {
		s, p, in, snap := adoptionFixture()
		snap.Namespace.NamespaceUID = uid
		s.adoptionInventory = adoptionReader{snapshot: snap}
		got, err := s.PreviewAdoption(t.Context(), p, "fleet", in)
		if err != nil || !hasAdoptionBlocker(got, "namespace_identity_unverified") {
			t.Fatalf("namespace %q: %+v %v", uid, got, err)
		}
	}
}
func TestFleetAdoptionPreservesData(t *testing.T) {
	s, p, in, snap := adoptionFixture()
	before := snap.Resources[0]
	got, err := s.PreviewAdoption(t.Context(), p, "fleet", in)
	if err != nil || len(got.Resources) != 1 || got.Resources[0].Ownership != "attached" || !got.Resources[0].PreserveOnFailure {
		t.Fatalf("data retention: %+v %v", got, err)
	}
	if !reflect.DeepEqual(before, snap.Resources[0]) {
		t.Fatal("inventory changed")
	}
	in.Resources[0].Ownership = "owned"
	got, err = s.PreviewAdoption(t.Context(), p, "fleet", in)
	if err != nil || !hasAdoptionBlocker(got, "physical_ownership_transfer_unverified") {
		t.Fatalf("external data claimed: %+v %v", got, err)
	}
}
func TestFleetAdoptionResourceConflicts(t *testing.T) {
	for _, change := range []func(*AdoptionResourceObservation){
		func(r *AdoptionResourceObservation) { r.Incarnation = "recreated" },
		func(r *AdoptionResourceObservation) { r.Revision++ },
		func(r *AdoptionResourceObservation) { r.ExternalID = "different" },
		func(r *AdoptionResourceObservation) { r.ObservedAt = time.Now().Add(-time.Hour) },
		func(r *AdoptionResourceObservation) {
			r.ConsumerCount = 1
		},
	} {
		s, p, in, snap := adoptionFixture()
		change(&snap.Resources[0])
		s.adoptionInventory = adoptionReader{snapshot: snap}
		got, err := s.PreviewAdoption(t.Context(), p, "fleet", in)
		if err != nil || !hasAdoptionBlocker(got, "resource_identity_or_use_conflict") {
			t.Fatalf("conflict accepted: %+v %v", got, err)
		}
	}
}
func TestFleetAdoptionRejectsUnfencedOwner(t *testing.T) {
	s, p, _, _ := adoptionFixture()
	if _, err := s.ApplyAdoption(t.Context(), p, "review", 2); !errors.Is(err, ErrAdoptionBlocked) {
		t.Fatalf("unqualified transfer accepted: %v", err)
	}
	p.Kind = security.KindFleet
	if _, err := s.ApplyAdoption(t.Context(), p, "review", 2); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("create grant became adopt authority: %v", err)
	}
}
func TestFleetAdoptionScopeAndFailure(t *testing.T) {
	s, p, in, _ := adoptionFixture()
	p.OrgID = "foreign"
	if _, err := s.PreviewAdoption(t.Context(), p, "fleet", in); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	p.OrgID = "org"
	s.authz = adoptionAuth{err: security.ErrForbidden}
	if _, err := s.PreviewAdoption(t.Context(), p, "fleet", in); !errors.Is(err, security.ErrForbidden) {
		t.Fatal(err)
	}
	s.authz = adoptionAuth{}
	s.adoptionInventory = adoptionReader{err: errors.New("secret provider error")}
	if _, err := s.PreviewAdoption(t.Context(), p, "fleet", in); !errors.Is(err, ErrAdoptionInventory) {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestFleetAdoptionInputBoundsAndDuplicateIdentity(t *testing.T) {
	s, p, in, _ := adoptionFixture()
	in.Resources = append(in.Resources, in.Resources[0])
	if _, err := s.PreviewAdoption(t.Context(), p, "fleet", in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate identity: %v", err)
	}
	_, _, in, _ = adoptionFixture()
	in.OwnershipEpoch = 0
	if _, err := s.PreviewAdoption(t.Context(), p, "fleet", in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing epoch: %v", err)
	}
}

func TestFleetAdoptionDoesNotLaunderServiceIdentity(t *testing.T) {
	s, p, in, _ := adoptionFixture()
	p.Kind = security.KindFleet
	if _, err := s.PreviewAdoption(t.Context(), p, "fleet", in); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("service inventory access: %v", err)
	}
}
func TestFleetAdoptionMissingInventoryIsNotEmpty(t *testing.T) {
	s, p, in, _ := adoptionFixture()
	s.adoptionInventory = nil
	got, err := s.PreviewAdoption(t.Context(), p, "fleet", in)
	if err != nil || got.InventoryState != "unavailable" || !hasAdoptionBlocker(got, "inventory_unavailable") {
		t.Fatalf("missing inventory: %+v %v", got, err)
	}
}
func TestFleetAdoptionPreviewDigestTracksObservedIdentity(t *testing.T) {
	s, p, in, snap := adoptionFixture()
	first, err := s.PreviewAdoption(t.Context(), p, "fleet", in)
	if err != nil {
		t.Fatal(err)
	}
	snap.Resources[0].Incarnation = "new-incarnation"
	s.adoptionInventory = adoptionReader{snapshot: snap}
	second, err := s.PreviewAdoption(t.Context(), p, "fleet", in)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("review identity ignored changed resource")
	}
}

func TestFleetAdoptionSanitizesWrappedReadErrors(t *testing.T) {
	s, p, in, _ := adoptionFixture()
	s.adoptionInventory = adoptionReader{err: fmt.Errorf("private provider detail: %w", security.ErrForbidden)}
	_, err := s.PreviewAdoption(t.Context(), p, "fleet", in)
	if !errors.Is(err, security.ErrForbidden) || err.Error() != security.ErrForbidden.Error() {
		t.Fatalf("wrapped permission details escaped: %v", err)
	}
}

type adoptionInventoryFunc func(context.Context, security.Principal, FleetScope, fleet.AdoptionInput) (AdoptionInventorySnapshot, error)

func (f adoptionInventoryFunc) InspectAdoption(ctx context.Context, p security.Principal, scope FleetScope, in fleet.AdoptionInput) (AdoptionInventorySnapshot, error) {
	return f(ctx, p, scope, in)
}
func TestFleetAdoptionAcceptsFreshObservationDuringRead(t *testing.T) {
	s, p, in, snap := adoptionFixture()
	s.adoptionInventory = adoptionInventoryFunc(func(context.Context, security.Principal, FleetScope, fleet.AdoptionInput) (AdoptionInventorySnapshot, error) {
		// Make the read finish after the entry-time validation clock.
		time.Sleep(time.Millisecond)
		snap.Namespace.ObservedAt = time.Now().UTC()
		snap.Resources[0].ObservedAt = snap.Namespace.ObservedAt
		return snap, nil
	})
	got, err := s.PreviewAdoption(t.Context(), p, "fleet", in)
	if err != nil || hasAdoptionBlocker(got, "namespace_identity_unverified") || hasAdoptionBlocker(got, "resource_identity_or_use_conflict") {
		t.Fatalf("fresh reader observation rejected: %+v %v", got, err)
	}
}

func TestFleetAdoptionPhysicalIdentityIncludesAccount(t *testing.T) {
	s, p, in, snap := adoptionFixture()
	second := in.Resources[0]
	second.InventoryID = "resource-other-account"
	in.Resources = append(in.Resources, second)
	observed := snap.Resources[0]
	observed.ID = second.InventoryID
	observed.AccountIdentity = "other-account"
	snap.Resources = append(snap.Resources, observed)
	s.adoptionInventory = adoptionReader{snapshot: snap}
	got, err := s.PreviewAdoption(t.Context(), p, "fleet", in)
	if err != nil || len(got.Resources) != 2 || hasAdoptionBlocker(got, "resource_identity_or_use_conflict") {
		t.Fatalf("distinct accounts rejected: %+v %v", got, err)
	}
	snap.Resources[1].AccountIdentity = "account"
	s.adoptionInventory = adoptionReader{snapshot: snap}
	got, err = s.PreviewAdoption(t.Context(), p, "fleet", in)
	if err != nil || !hasAdoptionBlocker(got, "resource_identity_or_use_conflict") {
		t.Fatalf("duplicate physical resource accepted: %+v %v", got, err)
	}
}

func TestFleetAdoptionMissingIncarnationRequiresRecreate(t *testing.T) {
	s, p, in, snap := adoptionFixture()
	in.NamespaceUID = ""
	in.Resources[0].Incarnation = ""
	snap.Namespace.NamespaceUID = ""
	snap.Resources[0].Incarnation = ""
	s.adoptionInventory = adoptionReader{snapshot: snap}
	got, err := s.PreviewAdoption(t.Context(), p, "fleet", in)
	if err != nil || !got.RequiresRecreateCutover || !hasAdoptionBlocker(got, "namespace_identity_unverified") || !hasAdoptionBlocker(got, "resource_identity_or_use_conflict") {
		t.Fatalf("missing immutable identity needs a review blocker: %+v %v", got, err)
	}
}
