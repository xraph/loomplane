//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/google/uuid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestClusterWorkloadObserverRealOwnerChain(t *testing.T) {
	f := newEnrollmentServiceFixture(t)
	name := "workload-" + uuid.NewString()[:8]
	ns, err := f.bootstrap.Kube.CoreV1().Namespaces().Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.bootstrap.Kube.CoreV1().Namespaces().Delete(t.Context(), name, metav1.DeleteOptions{}) })
	count := int32(1)
	release := kube.WorkloadReleaseID("runtime", "release")
	dep, err := f.bootstrap.Kube.AppsV1().Deployments(name).Create(t.Context(), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app"}, Spec: appsv1.DeploymentSpec{Replicas: &count, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}, Annotations: map[string]string{kube.WorkloadReleaseAnnotation: release}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.k8s.io/pause:3.10", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("8Mi")}}}}}}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		inventory, readErr := kube.ReadCapacityInventory(t.Context(), f.bootstrap.Kube)
		if readErr != nil {
			t.Fatal(readErr)
		}
		ref := provider.ClusterRef{ID: "cluster", AccountID: "account", Incarnation: "birth"}
		snapshot, mergeErr := cloudinfra.MergeClusterCapacity(provider.ProviderClusterCapacity{Provider: "digitalocean", Ref: ref, CheckedAt: inventory.CheckedAt, Complete: true}, cloudinfra.KubernetesClusterCapacity{Provider: "digitalocean", Ref: ref, Inventory: inventory})
		if mergeErr != nil {
			t.Fatal(mergeErr)
		}
		for _, w := range snapshot.Workloads {
			if w.DeploymentUID != string(dep.UID) {
				continue
			}
			if w.NamespaceUID != string(ns.UID) || w.ReplicaSetUID == "" || w.ReleaseID != release || w.Requests.CPUMillis != 10 {
				t.Fatalf("invalid safe proof: %+v", w)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("Deployment controller did not produce a provable ReplicaSet and pod")
}
