package cloudinfra

import "testing"

func TestCleanupTombstoneMatchesOnlyOldReleaseOnSharedController(t *testing.T) {
	tomb := ClusterCleanupTombstone{SchemaVersion: 1, NamespaceUID: "ns", ReleaseID: "old", Shared: true, Deployments: []string{"deployment"}, ReplicaSets: []string{"old-set"}, HPAs: []ClusterCleanupHPA{{Name: "app", UID: "hpa"}}}
	inv := &ClusterCleanupInventory{SchemaVersion: 1, Complete: true, NamespaceUIDs: []string{"ns", "another-ns"}, Objects: []ClusterCleanupObject{
		{Resource: "deployments", UID: "deployment", NamespaceUID: "ns", ReleaseID: "new", Generation: 2},
		{Resource: "replicasets", UID: "new-set", ControllerUID: "deployment", NamespaceUID: "ns", ReleaseID: "new"},
		{Resource: "pods", UID: "late-orphan", NamespaceUID: "ns", ReleaseID: "old"},
		{Resource: "pods", UID: "broken-chain", NamespaceUID: "ns", ControllerUID: "old-set"},
		{Resource: "pods", UID: "separate", NamespaceUID: "another-ns", ReleaseID: "old"},
	}}
	got := indexCleanupInventory(inv).match(&tomb, nil)
	if got.complete || len(got.residuals) != 2 || !got.residuals[cleanupResidualKey{"pods", "late-orphan"}] || !got.residuals[cleanupResidualKey{"pods", "broken-chain"}] || len(got.surviving) != 1 {
		t.Fatalf("HPA coverage or shared-release matching: %+v", got)
	}
	inv.HPAs = []ClusterCleanupHPAObservation{{NamespaceUID: "ns", Name: "app", UID: "hpa", DeploymentName: "app", Generation: 3, Complete: true}}
	got = indexCleanupInventory(inv).match(&tomb, nil)
	if !got.complete || len(got.servingHPAs) != 1 || len(got.residuals) != 2 {
		t.Fatalf("shared HPA lost: %+v", got)
	}
	tomb.Shared = false
	got = indexCleanupInventory(inv).match(&tomb, nil)
	if !got.residuals[cleanupResidualKey{"deployments", "deployment"}] || !got.residuals[cleanupResidualKey{"replicasets", "new-set"}] || !got.residuals[cleanupResidualKey{"horizontalpodautoscalers", "hpa"}] {
		t.Fatalf("exclusive parent chain missing: %+v", got)
	}
	inv.Objects[2].ReleaseID = ""
	active := []cleanupResidualKey{{"pods", "late-orphan"}}
	if !indexCleanupInventory(inv).match(&tomb, active).residuals[active[0]] {
		t.Fatal("stripping a known residual marker cleared drift")
	}
	inv.Objects[2].NamespaceUID = "another-ns"
	if indexCleanupInventory(inv).match(&tomb, active).complete {
		t.Fatal("contradictory identity of known residual permitted clearance")
	}
	inv.NamespaceUIDs = []string{"replacement-namespace"}
	if indexCleanupInventory(inv).match(&tomb, nil).complete {
		t.Fatal("wrong physical namespace cleared drift")
	}
}

func TestCleanupTombstoneAfterServingOwnerDisappears(t *testing.T) {
	tomb := &ClusterCleanupTombstone{SchemaVersion: 1, Shared: true, ReleaseID: "old", NamespaceUID: "ns", Deployments: []string{"controller"}, HPAs: []ClusterCleanupHPA{{Name: "api", UID: "hpa"}}}
	for _, tc := range []struct {
		name     string
		complete bool
		serving  bool
		objects  []ClusterCleanupObject
		hpa      ClusterCleanupHPAObservation
	}{
		{name: "all gone", complete: true, hpa: ClusterCleanupHPAObservation{Complete: true}},
		{name: "missing HPA coverage"},
		{name: "controller in another namespace", objects: []ClusterCleanupObject{{Resource: "deployments", UID: "controller", NamespaceUID: "other", ReleaseID: "new"}}, hpa: ClusterCleanupHPAObservation{Complete: true}},
		{name: "live newer controller", complete: true, serving: true, objects: []ClusterCleanupObject{{Resource: "deployments", UID: "controller", NamespaceUID: "ns", ReleaseID: "new"}}, hpa: ClusterCleanupHPAObservation{Complete: true}},
		{name: "same HPA without controller", complete: true, serving: true, hpa: ClusterCleanupHPAObservation{UID: "hpa", Complete: true, DeploymentName: "api"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.hpa.Name, tc.hpa.NamespaceUID = "api", "ns"
			inv := ClusterCleanupInventory{SchemaVersion: 1, Complete: true, NamespaceUIDs: []string{"ns", "other"}, HPAs: []ClusterCleanupHPAObservation{tc.hpa}, Objects: tc.objects}
			got := indexCleanupInventory(&inv).match(tomb, nil)
			if got.complete != tc.complete || got.needsServingProof(tomb, false) != tc.serving || !got.needsServingProof(tomb, true) {
				t.Fatalf("shared owner proof: %+v", got)
			}
		})
	}
}
