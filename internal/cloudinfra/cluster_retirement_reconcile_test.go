package cloudinfra

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type retirementMemory struct {
	*clusterMemory
	dependencies []string
}

func (m *retirementMemory) ClusterRetirementDependencies(context.Context, string, string, string, string) ([]string, error) {
	return m.dependencies, nil
}
func (m *retirementMemory) ValidateClusterRetirementEffect(_ context.Context, o *ClusterOperation) error {
	if m.op.Revision != o.Revision || m.op.CancelRequested {
		return ErrConflict
	}
	return nil
}
func (*retirementMemory) DisableClusterAttachment(context.Context, *ClusterOperation) error {
	return ErrInvalid
}

type retirementProvider struct {
	deferAbsence bool
	*clusterProvider
	inventory  provider.AssociatedResourceInventory
	deletes    int
	gone, lost bool
	before     func()
}

func (p *retirementProvider) AssociatedResources(context.Context, provider.AccountAccess, provider.ClusterRef) (provider.AssociatedResourceInventory, error) {
	p.inventory.ObservedAt = time.Now()
	return p.inventory, nil
}
func (p *retirementProvider) GetCluster(context.Context, provider.AccountAccess, provider.ClusterRef) (provider.ClusterObservation, error) {
	if p.gone {
		return provider.ClusterObservation{}, provider.ErrInfrastructureNotFound
	}
	return clusterObservation(), nil
}
func (p *retirementProvider) DeleteCluster(ctx context.Context, a provider.AccountAccess, ref provider.ClusterRef, expected string) (provider.ClusterMutation, error) {
	if p.before != nil {
		p.before()
	}
	if err := a.BeforeEffect(ctx); err != nil {
		return provider.ClusterMutation{}, err
	}
	if ref.Incarnation != expected {
		return provider.ClusterMutation{}, provider.ErrInfrastructureIdentity
	}
	p.deletes++
	p.gone = !p.deferAbsence
	if p.lost {
		return provider.ClusterMutation{}, provider.ErrInfrastructureAmbiguous
	}
	return provider.ClusterMutation{Kind: "retire-cluster", Ref: ref}, nil
}
func retirementService(t *testing.T) (*Service, *retirementMemory, *retirementProvider, *clusterAuth, security.Principal, *ClusterOperation) {
	t.Helper()
	s, base, _, auth, p, in := maintenanceFixture(t)
	store := &retirementMemory{clusterMemory: base}
	s.Clusters = store
	adapter := &retirementProvider{clusterProvider: &clusterProvider{}, inventory: provider.AssociatedResourceInventory{Ref: in.Ref, Complete: true, ChildrenQualified: true, Resources: []provider.AssociatedResource{{ID: "volume-1", Kind: "volume", Ownership: "external", SurvivesClusterDeletion: true}}}}
	s.Adapters = NewAdapters()
	if err := s.Adapters.RegisterCluster("digitalocean", adapter, provider.ClusterDeletion, provider.ClusterMutationObservation); err != nil {
		t.Fatal(err)
	}
	in.Kind = "retire-cluster"
	in.Maintenance = nil
	in.Retirement = &ClusterRetirementInput{}
	review, err := s.PreviewClusterRetirement(t.Context(), p, "cloud", in.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	in.Retirement.ReviewDigest = review.Digest
	o := acceptCluster(t, s, p, in)
	return s, store, adapter, auth, p, o
}
func TestClusterRetirementCrashReceipts(t *testing.T) {
	for _, crash := range []bool{false, true} {
		t.Run(map[bool]string{false: "response_loss", true: "checkpoint_loss"}[crash], func(t *testing.T) {
			s, store, adapter, _, p, o := retirementService(t)
			adapter.lost = !crash
			store.crashReceipt = crash
			_ = s.ReconcileClusterOperation(t.Context(), p.OrgID, o.ID, nil)
			store.restart()
			if err := s.ReconcileClusterOperation(t.Context(), p.OrgID, o.ID, nil); err != nil {
				t.Fatal(err)
			}
			if adapter.deletes != 1 || store.op.Status != "needs_attention" || store.resource.DeletedAt != nil {
				t.Fatalf("delete=%d status=%s tombstone=%v", adapter.deletes, store.op.Status, store.resource.DeletedAt)
			}
		})
	}
}
func TestClusterRetirementConfirmedReceipt(t *testing.T) {
	s, store, adapter, _, p, o := retirementService(t)
	if err := s.ReconcileClusterOperation(t.Context(), p.OrgID, o.ID, nil); err != nil {
		t.Fatal(err)
	}
	if adapter.deletes != 1 || store.op.Status != "succeeded" || store.resource.DeletedAt == nil || len(store.resource.RetainedResources) != 1 || store.resource.RetainedResources[0].ID != "volume-1" {
		t.Fatal(store.op.Status, store.resource)
	}
}
func TestClusterRetirementRevokedPermission(t *testing.T) {
	s, store, adapter, auth, p, o := retirementService(t)
	adapter.before = func() { auth.revoked = true }
	if err := s.ReconcileClusterOperation(t.Context(), p.OrgID, o.ID, nil); err != nil {
		t.Fatal(err)
	}
	if adapter.deletes != 0 || store.resource.DeletedAt != nil {
		t.Fatal("effect after revocation")
	}
}

func TestClusterRetirementParentConnectionLoss(t *testing.T) {
	s, store, adapter, _, p, o := retirementService(t)
	s.Connections.(*doksCredentials).status = "error"
	if err := s.ReconcileClusterOperation(t.Context(), p.OrgID, o.ID, nil); err != nil {
		t.Fatal(err)
	}
	if adapter.deletes != 0 || store.op.Status != "needs_attention" {
		t.Fatal("parent loss did not fence retirement", adapter.deletes, store.op.Status)
	}
}

func (m *retirementMemory) ClaimManagedResource(_ context.Context, r *ManagedResource, expected int64) (*ManagedResource, error) {
	if m.resource.Revision != expected {
		return nil, ErrConflict
	}
	m.resource = r
	return r, nil
}
func TestClusterRetirementConfirmedReceiptSurvivesObservation(t *testing.T) {
	s, store, adapter, _, p, o := retirementService(t)
	adapter.deferAbsence = true
	if err := s.ReconcileClusterOperation(t.Context(), p.OrgID, o.ID, nil); err != nil {
		t.Fatal(err)
	}
	if len(store.op.Receipts) != 1 || store.op.Status != "waiting_provider" {
		t.Fatal("confirmed receipt missing")
	}
	adapter.gone = true
	observed, err := s.ObserveClusterDrift(t.Context(), p, "cloud", o.Input.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Status != "missing" || observed.Revision <= o.Input.ResourceRevision {
		t.Fatal("drift did not persist missing observation")
	}
	store.restart()
	if err := s.ReconcileClusterOperation(t.Context(), p.OrgID, o.ID, nil); err != nil {
		t.Fatalf("benign observation stranded confirmed retirement: %v", err)
	}
	if store.op.Status != "succeeded" || store.resource.DeletedAt == nil || adapter.deletes != 1 {
		t.Fatal(store.op.Status, adapter.deletes)
	}
}

func TestClusterRetirementRecoveryPreservesTrustPins(t *testing.T) {
	for name, mutate := range map[string]func(*ManagedResource){
		"endpoint":    func(r *ManagedResource) { r.Observation.Endpoint = "replacement" },
		"ca":          func(r *ManagedResource) { r.Observation.CAHash = "replacement" },
		"incarnation": func(r *ManagedResource) { r.Incarnation = "replacement" },
		"ownership":   func(r *ManagedResource) { r.Ownership = "adopted" },
		"authority":   func(r *ManagedResource) { r.LifecycleAuthority = []string{"replacement"} },
		"creation":    func(r *ManagedResource) { r.CreationReceipt.OperationID = "replacement" },
	} {
		t.Run(name, func(t *testing.T) {
			_, store, _, _, _, o := retirementService(t)
			store.resource.Revision++
			if err := checkRetirementResourcePins(store.resource, "cloud", o.Input, true); err != nil {
				t.Fatal("benign revision rejected", err)
			}
			mutate(store.resource)
			if err := checkRetirementResourcePins(store.resource, "cloud", o.Input, true); !errors.Is(err, ErrConflict) {
				t.Fatal("changed trust admitted", err)
			}
		})
	}
}
func TestClusterRetirementLegacyRecoveryFailsClosed(t *testing.T) {
	_, store, _, _, _, o := retirementService(t)
	o.Input.Retirement.ResourceFingerprint = ""
	store.resource.Revision++
	if err := checkRetirementResourcePins(store.resource, "cloud", o.Input, true); !errors.Is(err, ErrConflict) {
		t.Fatal("legacy unpinned recovery gained authority", err)
	}
}
