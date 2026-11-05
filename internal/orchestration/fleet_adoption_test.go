package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/fleet"
)

func adoptionTransferFixture() (FleetAdoptionTransfer, time.Time) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	return FleetAdoptionTransfer{OrgID: "org", FleetID: "fleet", ReviewID: "review", ReviewRevision: 2, InventoryDigest: "inventory", InventoryObservedAt: now.Add(-time.Second), NamespaceUID: "namespace-uid", LeaseID: "lease", LeaseExpiresAt: now.Add(time.Minute), PriorEpoch: 4, Epoch: 5, OldControllerID: "controller", OldAuthorityID: "old-authority", NewAuthorityID: "new-authority", FenceReceiptID: "fence", FencedAuthorityID: "old-authority", FenceEpoch: 5, FenceNamespaceUID: "namespace-uid", FenceVerifiedAt: now.Add(-time.Second), OldWriterRevoked: true, NoChangePlanID: "plan", PlanInventoryDigest: "inventory", PlanVerifiedAt: now.Add(-time.Second), PlanComplete: true, ActiveWriter: "new"}, now
}
func TestFleetAdoptionNoChangePlan(t *testing.T) {
	evidence, now := adoptionTransferFixture()
	if err := ValidateFleetAdoptionTransfer(evidence, now); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*FleetAdoptionTransfer){
		func(e *FleetAdoptionTransfer) { e.PlanComplete = false },
		func(e *FleetAdoptionTransfer) { e.PlanChanges = 1 },
		func(e *FleetAdoptionTransfer) { e.PlanInventoryDigest = "other" },
		func(e *FleetAdoptionTransfer) { e.NoChangePlanID = "" },
		func(e *FleetAdoptionTransfer) { e.PlanVerifiedAt = now.Add(-time.Hour) },
	} {
		e := evidence
		change(&e)
		if err := ValidateFleetAdoptionTransfer(e, now); err == nil {
			t.Fatalf("unproven plan accepted: %+v", e)
		}
	}
}
func TestFleetAdoptionRollbackSingleOwner(t *testing.T) {
	evidence, now := adoptionTransferFixture()
	evidence.ActiveWriter = "old"
	if err := ValidateFleetAdoptionTransfer(evidence, now); err == nil {
		t.Fatal("old writer restored before new writer fenced")
	}
	evidence.OldWriterRevoked = false
	evidence.NewWriterRevoked = true
	if err := ValidateFleetAdoptionTransfer(evidence, now); err == nil {
		t.Fatal("old fence receipt reused for reversal")
	}
	evidence.FencedAuthorityID = "new-authority"
	if err := ValidateFleetAdoptionTransfer(evidence, now); err != nil {
		t.Fatal(err)
	}
	evidence.NewWriterRevoked = false
	if err := ValidateFleetAdoptionTransfer(evidence, now); err == nil {
		t.Fatal("two writers accepted")
	}
}
func TestFleetAdoptionTransferRejectsMissingFenceAndExpiredLease(t *testing.T) {
	evidence, now := adoptionTransferFixture()
	for _, change := range []func(*FleetAdoptionTransfer){
		func(e *FleetAdoptionTransfer) { e.OldWriterRevoked = false },
		func(e *FleetAdoptionTransfer) { e.FenceReceiptID = "" },
		func(e *FleetAdoptionTransfer) { e.FenceVerifiedAt = now.Add(time.Second) },
		func(e *FleetAdoptionTransfer) { e.LeaseExpiresAt = now },
		func(e *FleetAdoptionTransfer) { e.Epoch = e.PriorEpoch },
		func(e *FleetAdoptionTransfer) { e.FenceEpoch = 4 },
		func(e *FleetAdoptionTransfer) { e.FenceNamespaceUID = "recreated" },
		func(e *FleetAdoptionTransfer) { e.NamespaceUID = "" },
		func(e *FleetAdoptionTransfer) { e.NewAuthorityID = e.OldAuthorityID },
		func(e *FleetAdoptionTransfer) { e.ActiveWriter = "both" },
	} {
		e := evidence
		change(&e)
		if err := ValidateFleetAdoptionTransfer(e, now); err == nil {
			t.Fatalf("unsafe transfer accepted: %+v", e)
		}
	}
}

func TestFleetAdoptionRequiresPlanAfterFence(t *testing.T) {
	evidence, now := adoptionTransferFixture()
	evidence.PlanVerifiedAt = evidence.FenceVerifiedAt.Add(-time.Second)
	if err := ValidateFleetAdoptionTransfer(evidence, now); err == nil {
		t.Fatal("pre-fence no-change plan accepted")
	}
}

func TestFleetAdoptionRequiresInventoryAfterFence(t *testing.T) {
	evidence, now := adoptionTransferFixture()
	evidence.InventoryObservedAt = evidence.FenceVerifiedAt.Add(-time.Second)
	if err := ValidateFleetAdoptionTransfer(evidence, now); err == nil {
		t.Fatal("pre-fence inventory accepted")
	}
	evidence.InventoryObservedAt = now
	evidence.PlanVerifiedAt = now.Add(-time.Second)
	if err := ValidateFleetAdoptionTransfer(evidence, now); err == nil {
		t.Fatal("plan predating inventory accepted")
	}
}

type adoptionResourceReader struct {
	resource *providerresources.ProviderResource
	err      error
}

func (r adoptionResourceReader) Get(context.Context, security.Principal, string) (*providerresources.ProviderResource, error) {
	return r.resource, r.err
}
func TestFleetAdoptionInventoryAdapter(t *testing.T) {
	p := security.Principal{OrgID: "org", UserID: "operator"}
	in := fleet.AdoptionInput{Resources: []fleet.AdoptionResourceInput{{InventoryID: "resource"}}}
	resource := &providerresources.ProviderResource{ID: "resource", OrgID: "org", ExternalID: "native-id", Incarnation: "created-1", Consumers: []providerresources.ProviderResourceConsumer{{ProjectID: "other"}}, Host: "private-host", EndpointID: "private-endpoint"}
	reader := NewFleetAdoptionInventory(adoptionResourceReader{resource: resource}, nil)
	got, err := reader.InspectAdoption(t.Context(), p, fleets.FleetScope{OrgID: "org", ProjectID: "project", FleetID: "fleet"}, in)
	if err != nil || len(got.Resources) != 1 || got.Resources[0].ConsumerCount != 1 || got.Resources[0].ExternalID != "native-id" || got.Namespace.NamespaceUID != "" {
		t.Fatalf("inventory: %+v %v", got, err)
	}
	resource.OrgID = "foreign"
	if _, err = reader.InspectAdoption(t.Context(), p, fleets.FleetScope{OrgID: "org", ProjectID: "project", FleetID: "fleet"}, in); !errors.Is(err, fleets.ErrNotFound) {
		t.Fatalf("foreign resource exposed: %v", err)
	}
}
