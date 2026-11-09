package cloudinfra

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
)

func TestClusterScalingStableReportedRequests(t *testing.T) {
	req := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi")}
	equal := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1000m"), corev1.ResourceMemory: resource.MustParse("1024Mi")}
	for _, which := range []string{"equal", "pod-report", "pod-cgroup", "pod-mismatch", "pod-missing-effective", "pod-overhead", "other-name", "duplicate", "changed-allocation", "changed-effective", "missing-effective", "missing-allocation", "in-progress", "unknown-condition", "extra-resource", "missing-key"} {
		t.Run(which, func(t *testing.T) {
			p := corev1.Pod{}
			p.UID = "uid"
			p.Spec.Containers = []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Requests: req.DeepCopy()}}}
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", AllocatedResources: equal.DeepCopy(), Resources: &corev1.ResourceRequirements{Requests: equal.DeepCopy()}}}
			status := &p.Status.ContainerStatuses[0]
			if which == "pod-cgroup" || which == "pod-report" || which == "pod-mismatch" || which == "pod-missing-effective" || which == "pod-overhead" {
				p.Status.AllocatedResources = equal.DeepCopy()
				p.Status.Resources = &corev1.ResourceRequirements{Requests: equal.DeepCopy()}
			}
			switch which {
			case "pod-cgroup":
				delete(p.Status.Resources.Requests, corev1.ResourceCPU)
			case "pod-mismatch":
				p.Status.AllocatedResources[corev1.ResourceCPU] = resource.MustParse("2")
			case "pod-missing-effective":
				p.Status.Resources = nil
			case "pod-overhead":
				p.Spec.Overhead = equal.DeepCopy()

			case "other-name":
				status.Name = "other"
			case "duplicate":
				p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, *status)
			case "changed-allocation":
				status.AllocatedResources[corev1.ResourceCPU] = resource.MustParse("2")
			case "changed-effective":
				status.Resources.Requests[corev1.ResourceMemory] = resource.MustParse("2Gi")
			case "missing-effective":
				status.Resources = nil
			case "missing-allocation":
				status.AllocatedResources = nil
			case "in-progress":
				p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodResizeInProgress, Status: corev1.ConditionTrue}}
			case "unknown-condition":
				p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodResizePending, Status: corev1.ConditionUnknown}}
			case "extra-resource":
				for _, m := range []corev1.ResourceList{p.Spec.Containers[0].Resources.Requests, status.AllocatedResources, status.Resources.Requests} {
					m["vendor.io/resource"] = resource.MustParse("1")
				}
			case "missing-key":
				delete(status.AllocatedResources, corev1.ResourceMemory)
			}
			p.Spec.NodeName = "worker"
			providerFacts, kubeFacts := clusterCapacityFixture()
			kubeFacts.Inventory.Pods = []corev1.Pod{p}
			snapshot, mergeErr := MergeClusterCapacity(providerFacts, kubeFacts)
			if mergeErr != nil || snapshot.Complete != (which == "equal" || which == "pod-report" || which == "pod-cgroup") {
				t.Fatal("node allocation qualification", snapshot.Conditions, mergeErr)
			}
			_, complete, err := clusterWorkloadObservations(kubernetes.CapacityInventory{Pods: []corev1.Pod{p}})
			if err != nil || complete != (which == "equal" || which == "pod-report" || which == "pod-cgroup") || podAllocationsCurrent(&p) != (which == "equal" || which == "pod-report" || which == "pod-cgroup") {
				t.Fatal("allocation qualification", complete, err)
			}
		})
	}
}
