package cloudinfra

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
)

func workloadObservationFixture() kubernetes.CapacityInventory {
	control := true
	release := kubernetes.WorkloadReleaseID("runtime", "deployment/action")
	template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{kubernetes.WorkloadReleaseAnnotation: release}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "private-image", Env: []corev1.EnvVar{{Name: "SECRET", Value: "never-public-value"}}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("128Mi")}}}}}}
	dep := appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ns", UID: "deployment", Generation: 2}, Spec: appsv1.DeploymentSpec{Template: template}}
	rs := appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "rs", Namespace: "ns", UID: "rs", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", UID: dep.UID, Controller: &control}}}, Spec: appsv1.ReplicaSetSpec{Template: *template.DeepCopy()}}
	rs.Spec.Template.Labels = map[string]string{appsv1.DefaultDeploymentUniqueLabelKey: "hash"}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns", UID: "pod", Annotations: map[string]string{kubernetes.WorkloadReleaseAnnotation: release}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", UID: rs.UID, Controller: &control}}}, Spec: template.Spec}
	return kubernetes.CapacityInventory{Complete: true, CheckedAt: time.Now(), Namespaces: []corev1.Namespace{{ObjectMeta: metav1.ObjectMeta{Name: "ns", UID: "namespace"}}}, Deployments: []appsv1.Deployment{dep}, ReplicaSets: []appsv1.ReplicaSet{rs}, Pods: []corev1.Pod{pod}}
}
func TestClusterWorkloadObservationProof(t *testing.T) {
	for _, mode := range []string{"valid", "old-release", "wrong-owner", "wrong-namespace", "forged-label", "changed-template", "duplicate", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			in := workloadObservationFixture()
			switch mode {
			case "old-release":
				in.ReplicaSets[0].Spec.Template.Annotations[kubernetes.WorkloadReleaseAnnotation] = "old"
			case "wrong-owner":
				in.Pods[0].OwnerReferences[0].UID = types.UID("forged")
			case "wrong-namespace":
				in.ReplicaSets[0].Namespace = "other"
			case "forged-label":
				in.Pods[0].OwnerReferences = nil
			case "changed-template":
				in.ReplicaSets[0].Spec.Template.Spec.Containers[0].Image = "other"
			case "duplicate":
				in.Pods = append(in.Pods, in.Pods[0])
			case "unsupported":
				in.Pods[0].Spec.Containers[0].Resources.Requests["nvidia.com/gpu"] = resource.MustParse("1")
			}
			got, complete, err := clusterWorkloadObservations(in)
			if err != nil || len(got) != 1 {
				t.Fatalf("observation: %+v %v", got, err)
			}
			if complete != (mode != "duplicate" && mode != "unsupported") {
				t.Fatalf("completeness %v", complete)
			}
			if mode != "valid" && mode != "duplicate" && mode != "unsupported" && got[0].ReleaseID != "" {
				t.Fatalf("unproven release earned identity: %+v", got[0])
			}
			if mode == "valid" && (got[0].ReleaseID == "" || got[0].NamespaceUID != "namespace") {
				t.Fatal("valid chain omitted")
			}
			if got[0].Requests.CPUMillis != 500 || got[0].Requests.MemoryBytes != 128<<20 || got[0].Requests.Pods != 1 {
				t.Fatalf("requests lost: %+v", got[0])
			}
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"private-image", "SECRET", "never-public-value"} {
				if strings.Contains(string(raw), secret) {
					t.Fatal("raw template exposed")
				}
			}
		})
	}
}

func TestClusterWorkloadObservationCountsScheduledAndPending(t *testing.T) {
	in := workloadObservationFixture()
	in.Pods[0].Spec.NodeName = "node"
	pending := in.Pods[0].DeepCopy()
	pending.UID = "pending"
	pending.Spec.NodeName = ""
	pending.Status.Phase = corev1.PodPending
	in.Pods = append(in.Pods, *pending)
	pods, complete, err := clusterWorkloadObservations(in)
	if err != nil || !complete || len(pods) != 2 {
		t.Fatalf("pending observation: %+v %v", pods, err)
	}
	var observed ClusterWorkloadVector
	for _, pod := range pods {
		if pod.ReleaseID == "" {
			t.Fatal("exact release proof missing")
		}
		observed, err = clusterWorkloadUsage(observed, []clusterWorkloadCoverage{{Maximum: pod.Requests}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if observed.CPUMillis != 1000 || observed.Pods != 2 {
		t.Fatal("pending pod omitted from observed usage", observed)
	}
	used, err := clusterWorkloadUsage(observed, []clusterWorkloadCoverage{{Maximum: ClusterWorkloadVector{CPUMillis: 2000, MemoryBytes: 512 << 20, Pods: 4}, Observed: observed}})
	if err != nil || used.CPUMillis != 2000 || used.Pods != 4 {
		t.Fatalf("pending credit freed reviewed maximum: %+v %v", used, err)
	}
}
