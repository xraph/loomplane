package cloudinfra

import (
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
)

func TestCleanupInventoryRetainsBrokenOwnerWithoutCredit(t *testing.T) {
	in := workloadObservationFixture()
	marker := in.Pods[0].Annotations[kubernetes.WorkloadReleaseAnnotation]
	in.ReplicaSets = nil
	info := NormalizeCleanupInventory(in)
	workloads, complete, err := clusterWorkloadObservations(in)
	if err != nil || !complete || len(workloads) != 1 || workloads[0].Requests.Pods != 1 || workloads[0].Requests.CPUMillis != 500 || workloads[0].ReleaseID != "" || workloads[0].ReplicaSetUID != "" {
		t.Fatalf("orphan credit or request accounting changed: %+v %v", workloads, err)
	}
	if len(info.Objects) != 2 {
		t.Fatalf("safe identities: %+v", info)
	}
	pod := info.Objects[1]
	if pod.Resource != "pods" || pod.UID != "pod" || pod.ControllerUID != "rs" || pod.ReleaseID != marker || pod.NamespaceUID == "" {
		t.Fatalf("broken-chain identity lost: %+v", pod)
	}
	raw, err := json.Marshal(info)
	if err != nil || strings.Contains(string(raw), "private-image") || strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "never-public-value") {
		t.Fatal("private workload data in residual identities", err)
	}
	in.Pods[0].Annotations[kubernetes.WorkloadReleaseAnnotation] = "never-public-value"
	in.Pods[0].OwnerReferences = nil
	info = NormalizeCleanupInventory(in)
	if info.Objects[1].ReleaseID != "" || info.Objects[1].ControllerUID != "" {
		t.Fatal("invented identity from malformed metadata")
	}
}

func TestCleanupInventoryRequiresNamespaceAndUniqueUIDs(t *testing.T) {
	for _, which := range []string{"missing-namespace", "namespace-replaced", "duplicate-pod", "incomplete"} {
		in := workloadObservationFixture()
		in.Complete = true
		switch which {
		case "missing-namespace":
			in.Namespaces = nil
		case "namespace-replaced":
			now := metav1.Now()
			in.Namespaces[0].DeletionTimestamp = &now
		case "duplicate-pod":
			in.Pods = append(in.Pods, in.Pods[0])
		case "incomplete":
			in.Complete = false
		}
		info := NormalizeCleanupInventory(in)
		if info.Complete {
			t.Fatalf("%s qualified absence", which)
		}
		var matches int
		for _, obj := range info.Objects {
			if obj.Resource == "pods" && obj.UID == "pod" {
				matches++
			}
		}
		if matches != 1 {
			t.Fatalf("duplicate pod: %s %d", which, matches)
		}
	}
}
