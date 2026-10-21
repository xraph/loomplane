package agentinstall

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestPreparationOwnershipRejectsUnrelatedResources(t *testing.T) {
	plan, _, _, _ := launchFixture(t, false)
	sa, pod, err := plan.PreparationResources("namespace-uid")
	if err != nil {
		t.Fatal(err)
	}
	if len(sa.OwnerReferences) != 1 || sa.OwnerReferences[0].Kind != "Namespace" || string(sa.OwnerReferences[0].UID) != "namespace-uid" || len(pod.OwnerReferences) != 1 {
		t.Fatal("creation is not bound to namespace UID")
	}
	sa.UID = "service-account-uid"
	pod.UID = "pod-uid"
	if !plan.MatchesPreparedAccount(sa, "namespace-uid") || !plan.MatchesPreparedPod(pod, "namespace-uid") {
		t.Fatal("owned resource not recognized")
	}
	for _, mode := range []string{"marker", "namespace UID", "token", "pull secret", "owner"} {
		t.Run("account/"+mode, func(t *testing.T) {
			obj := sa.DeepCopy()
			switch mode {
			case "marker":
				obj.Annotations = nil
			case "namespace UID":
				obj.Annotations[PreparationNamespaceAnnotation] = "foreign"
			case "token":
				obj.Secrets = []corev1.ObjectReference{{Name: "token"}}
			case "pull secret":
				obj.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "secret"}}
			case "owner":
				obj.OwnerReferences = podOwner("foreign")
			}
			if plan.MatchesPreparedAccount(obj, "namespace-uid") {
				t.Fatal("foreign account adopted")
			}
		})
	}
	for _, mode := range []string{"marker", "image", "sidecar", "host network", "mount", "owner"} {
		t.Run("pod/"+mode, func(t *testing.T) {
			obj := pod.DeepCopy()
			switch mode {
			case "marker":
				obj.Annotations = nil
			case "image":
				obj.Spec.Containers[0].Image = "other"
			case "sidecar":
				obj.Spec.Containers = append(obj.Spec.Containers, corev1.Container{Name: "other"})
			case "host network":
				obj.Spec.HostNetwork = true
			case "mount":
				obj.Spec.Volumes = nil
			case "owner":
				obj.OwnerReferences = podOwner("foreign")
			}
			if plan.MatchesPreparedPod(obj, "namespace-uid") {
				t.Fatal("foreign pod adopted")
			}
		})
	}
	pod.Spec.NodeName = "scheduled-node"
	pod.Status.Phase = corev1.PodPending
	if !plan.MatchesPreparedPod(pod, "namespace-uid") {
		t.Fatal("scheduling changed ownership")
	}
}

func podOwner(uid string) []metav1.OwnerReference {
	return []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "owner", UID: types.UID(uid)}}
}

func TestPreparedPodStillRequiresScheduledLaunchBinding(t *testing.T) {
	plan, _, record, _ := launchFixture(t, false)
	_, pod, err := plan.PreparationResources(record.Binding.NamespaceUID)
	if err != nil {
		t.Fatal(err)
	}
	pod.UID = types.UID(record.Binding.PodUID)
	if plan.matches(pod, record.Binding.PodUID, record.Binding.NamespaceUID) {
		t.Fatal("unscheduled preparation accepted for delivery")
	}
	pod.Spec.NodeName = "node"
	pod.Status.Phase = corev1.PodPending
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}}
	if !plan.matches(pod, record.Binding.PodUID, record.Binding.NamespaceUID) {
		t.Fatal("prepared namespace ownership rejected for delivery")
	}
	if plan.matches(pod, record.Binding.PodUID, "different-namespace") {
		t.Fatal("foreign namespace owner accepted")
	}
}
