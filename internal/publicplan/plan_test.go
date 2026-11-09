package publicplan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/planning"
)

func TestProjectionPreservesReviewAndOmitsAuthority(t *testing.T) {
	target := planning.ManagedCleanupTarget{Kind: planning.ManagedCleanupRemoveRelease, Scope: kubernetes.ManagedScope{Service: "api", Plan: "old-plan", Deployment: "old-deployment", Namespace: "app", RuntimeRef: "private-runtime", ClusterCA: "private-ca"}, AttachmentID: "private-attachment", Unresolved: []string{"private-issuance"}, Accepted: []planning.ManagedCleanupOutcome{{ID: "private-outcome", Request: kubernetes.ManagedRequest{Resource: "deployments", Name: "api", Namespace: "app", UID: "private-uid", CleanupSource: "private-capture"}}}}
	p := &planning.Plan{ID: "plan", Revision: "original-digest", Actions: []planning.Action{{ID: "service:api", Name: "api", Op: planning.Delete, CleanupTargets: []planning.ManagedCleanupTarget{target}, Service: &planning.ServiceConfig{TargetAccountIdentity: "selected-account", TargetSettings: map[string]string{"namespace": "app", "managed_attachment_id": "private-attachment"}}, Resource: &planning.ResourceConfig{AccountIdentity: "selected-resource-account", ExistingResource: &planning.ExistingResourceConfig{ResourceID: "resource", AccountIdentity: "selected-existing-account"}}}}}
	before, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	got := Project(p)
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-") {
		t.Fatalf("private authority leaked: %s", raw)
	}
	if got.Revision != p.Revision || len(got.Actions[0].CleanupTargets) != 1 || got.Actions[0].CleanupTargets[0].UnresolvedWrites != 1 || got.Actions[0].CleanupTargets[0].Objects[0].Name != "api" {
		t.Fatalf("lost review: %+v", got)
	}
	after, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("projection changed stored signed plan")
	}
}

func TestProjectionRetainsReviewedPlacement(t *testing.T) {
	in := map[string]string{"docker_id": "selected-host", "resource_group": "selected-group", "ingress_domain": "apps.example.test", "registry_hostname": "registry.example.test", "storage_expansion": "true", "managed_attachment_id": "private-attachment", "build_runtime_identity": "private-qualification", "unknown_future_authority": "private-future"}
	got := settings(in)
	for _, key := range []string{"docker_id", "resource_group", "ingress_domain", "registry_hostname", "storage_expansion"} {
		if got[key] != in[key] {
			t.Fatalf("lost reviewed placement %s", key)
		}
	}
	for _, key := range []string{"managed_attachment_id", "build_runtime_identity", "unknown_future_authority"} {
		if _, ok := got[key]; ok {
			t.Fatalf("private setting %s", key)
		}
	}
}

func TestPublicPlanRequiredCollections(t *testing.T) {
	got := Project(&planning.Plan{})
	if got.Leftovers == nil || got.Artifacts == nil || got.Actions == nil || got.Sources == nil || got.Problems == nil || got.Warnings == nil {
		t.Fatal("required public collections must encode as arrays")
	}
}

func TestCapacityProjectionExcludesUnrelatedLocationPolicy(t *testing.T) {
	var p planning.Plan
	raw := `{"id":"p","project_id":"restricted","actions":[{"id":"service:api","service":{"capacity_intent":{"demand":{"project_id":"restricted","target_id":"granted-target","resources":{"cpu_millis":1000},"location_review":{"cloud_id":"cloud","policy":{"preferred_locations":["private-ungranted-location"]},"candidates":[{"target_id":"granted-target","runtime_connection_id":"private-runtime-pin","connection_generation":9}]}}}}}]}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(&p)
	out, err := json.Marshal(Project(&p))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "private-") || strings.Contains(string(out), "location_review") {
		t.Fatalf("restricted project saw private authority: %s", out)
	}
	if !strings.Contains(string(out), "granted-target") || !strings.Contains(string(out), `"cpu_millis":1000`) {
		t.Fatalf("lost safe capacity review: %s", out)
	}
	after, _ := json.Marshal(&p)
	if string(before) != string(after) {
		t.Fatal("projection modified persisted authority")
	}
}
