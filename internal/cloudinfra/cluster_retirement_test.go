package cloudinfra

import (
	"slices"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

func retirementFixture() (*ManagedResource, provider.AssociatedResourceInventory) {
	ref := provider.ClusterRef{ID: "physical", AccountID: "account", Region: "nyc1", Incarnation: "birth"}
	return &ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", Revision: 2, Observation: &provider.ClusterObservation{Ref: ref}}, provider.AssociatedResourceInventory{Ref: ref, Complete: true, ChildrenQualified: true, ObservedAt: time.Now(), Resources: []provider.AssociatedResource{{ID: "volume-external", Kind: "volume", Ownership: "external", SurvivesClusterDeletion: true}}}
}
func TestClusterRetirementInUse(t *testing.T) {
	r, inventory := retirementFixture()
	deps := []string{"attachment:namespace-a", "attachment:namespace-b", "connection_use:domain_route:route"}
	got := retirementReview(r, inventory, deps, time.Now())
	for _, dep := range deps {
		if !slices.Contains(got.Blockers, dep) {
			t.Fatal(got.Blockers)
		}
	}
}
func TestClusterRetirementInventoryIncomplete(t *testing.T) {
	r, inventory := retirementFixture()
	inventory.Complete = false
	if !slices.Contains(retirementReview(r, inventory, nil, time.Now()).Blockers, "inventory_incomplete") {
		t.Fatal("partial inventory authorized")
	}
	inventory.Complete = true
	inventory.ChildrenQualified = false
	if !slices.Contains(retirementReview(r, inventory, nil, time.Now()).Blockers, "child_ownership_unproven") {
		t.Fatal("unproven children authorized")
	}
}
func TestClusterRetirementRetainsExternalData(t *testing.T) {
	r, inventory := retirementFixture()
	got := retirementReview(r, inventory, nil, time.Now())
	if len(got.DeletionCandidates) != 0 || len(got.RetainedResources) != 1 || got.RetainedResources[0].ID != "volume-external" || len(got.UnknownCosts) != 1 {
		t.Fatal(got)
	}
	first := got.Digest
	inventory.ObservedAt = inventory.ObservedAt.Add(time.Second)
	if second := retirementReview(r, inventory, nil, time.Now().Add(time.Second)).Digest; second != first {
		t.Fatal("observation time changed review")
	}
	inventory.Resources[0].ID = "replacement"
	if retirementReview(r, inventory, nil, time.Now()).Digest == first {
		t.Fatal("identity did not invalidate review")
	}
}
func TestClusterRetirementDetachExactReceipts(t *testing.T) {
	a := &RuntimeAttachment{ID: "a", Namespace: "external", NamespaceUID: "ns-uid", ServiceAccountName: "runtime", ServiceAccountUID: "sa-uid"}
	for _, kind := range []string{"Role", "RoleBinding", "ServiceAccount", "ClusterRole", "ClusterRoleBinding"} {
		obj := RBACObjectReceipt{Kind: kind, Name: "runtime", Namespace: "external", UID: kind + "-uid"}
		if kind == "ServiceAccount" {
			obj.UID = "sa-uid"
		}
		if kind == "ClusterRole" || kind == "ClusterRoleBinding" {
			obj.Name = "loomplane-a"
			obj.Namespace = ""
		}
		a.RBACObjects = append(a.RBACObjects, obj)
	}
	if !validDetachObjects(a) {
		t.Fatal("owned receipts rejected")
	}
	a.RBACObjects = append(a.RBACObjects, RBACObjectReceipt{Kind: "Namespace", Name: "external", UID: "ns-uid"})
	if validDetachObjects(a) {
		t.Fatal("detach allowed namespace deletion")
	}
}
