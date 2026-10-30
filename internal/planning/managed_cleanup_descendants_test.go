package planning

import (
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func TestManagedCleanupDescendantEvidence(t *testing.T) {
	now := time.Now()
	scope := kube.ManagedScope{Org: "org", Project: "project", Environment: "env", Service: "api", Connection: "connection", Instance: id.New(id.PrefixInstance).String(), Namespace: "apps", NamespaceUID: "namespace", ClusterCA: strings.Repeat("a", 64), Release: "old-release"}
	target := ManagedCleanupTarget{Kind: ManagedCleanupRetireDependents, Scope: scope, Accepted: []ManagedCleanupOutcome{{Request: kube.ManagedRequest{Group: "apps", Resource: "deployments", Method: "POST"}, Outcome: kube.ManagedOutcome{State: "accepted", UID: "deployment"}}}}
	request := kube.ManagedRequest{Version: "v1", Resource: "pods", Namespace: "apps", Name: "old-pod", UID: "pod", Method: "DELETE"}
	receipt := kube.ControllerReceipt{Scope: domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, ConnectionID: scope.Connection, InstanceID: scope.Instance, PlanID: "new-plan", RuntimeRef: "org/env/api/runtime_new", RuntimeRevision: "new-deployment/service:api", ServiceID: "api", Container: "api", Image: "example/image@sha256:" + strings.Repeat("b", 64), Namespace: scope.Namespace, NamespaceUID: scope.NamespaceUID, ClusterCA: scope.ClusterCA, ServiceAccount: "default", DeploymentUID: "deployment", DeploymentGeneration: 2}
	receipt.ReleaseID = kube.WorkloadReleaseID(receipt.RuntimeRef, receipt.RuntimeRevision)
	evidence := ManagedCleanupDescendantEvidence{Scope: scope, Request: request, ObservedAt: now, DeploymentUID: "deployment", ReplicaSetUID: "replicaset", PodUID: "pod", ResourceVersion: "3", ReplicaSetResourceVersion: "2", Release: scope.Release, NewerController: &receipt}
	if !validCleanupDescendant(target, request, evidence, now) {
		t.Fatal("safe evidence shape rejected")
	}
	for _, change := range []string{"scope", "request", "release", "stale", "future", "resource-version", "owner", "uid", "receipt", "shared-controller"} {
		t.Run(change, func(t *testing.T) {
			e := evidence
			r := request
			switch change {
			case "scope":
				e.Scope.NamespaceUID = "foreign"
			case "request":
				e.Request.Name = "another-pod"
			case "release":
				e.Release = receipt.ReleaseID
			case "stale":
				e.ObservedAt = now.Add(-3 * time.Minute)
			case "future":
				e.ObservedAt = now.Add(time.Minute)
			case "resource-version":
				e.ResourceVersion = ""
			case "owner":
				e.DeploymentUID = "different"
			case "uid":
				e.PodUID = "different"
			case "receipt":
				e.NewerController = nil
			case "shared-controller":
				r.Resource = "deployments"
				r.Group = "apps"
				e.Request = r
			}
			if validCleanupDescendant(target, r, e, now) {
				t.Fatal("unproved descendant accepted")
			}
		})
	}
}
