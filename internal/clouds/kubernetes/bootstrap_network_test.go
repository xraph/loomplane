package kubernetes

import (
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
)

func TestMetalLBClassMigrationWaitsForOldProviderCleanup(t *testing.T) {
	c := bootstrapFixture(t)
	old := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "loomplane-ingress", "namespace": bootstrapNamespace, "uid": "service", "labels": map[string]any{bootstrapOwner: "cluster-uid"}, "annotations": map[string]any{"loomplane.bootstrap.network": "metallb"}, "finalizers": []any{"provider-cleanup"}}, "spec": map[string]any{"type": "LoadBalancer", "clusterIP": "10.96.0.20", "ports": []any{map[string]any{"port": int64(80), "nodePort": int64(30080)}}}}}
	api := c.bootstrapAPI(old)
	if _, err := api.Create(t.Context(), old, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	want := old.DeepCopy()
	if err := configureMetalLBIngress([]*unstructured.Unstructured{want}); err != nil {
		t.Fatal(err)
	}
	if err := c.prepareMetalLBClass(t.Context(), want); !errors.Is(err, ErrRegistryPending) {
		t.Fatal(err)
	}
	current, err := api.Get(t.Context(), old.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	kind, _, err := unstructured.NestedString(current.Object, "spec", "type")
	if err != nil || kind != "ClusterIP" {
		t.Fatal("old provider was not released", err)
	}
	if err = c.prepareMetalLBClass(t.Context(), want); !errors.Is(err, ErrRegistryPending) {
		t.Fatal("finalizer was bypassed", err)
	}
	current.SetFinalizers(nil)
	if _, err = api.Update(t.Context(), current, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.prepareMetalLBClass(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	class, _, err := unstructured.NestedString(want.Object, "spec", "loadBalancerClass")
	if err != nil || class != "loomplane.io/metallb" {
		t.Fatal("class missing", err)
	}
	address, _, err := unstructured.NestedString(want.Object, "spec", "clusterIP")
	if err != nil || address != "10.96.0.20" || len(want.GetFinalizers()) != 0 {
		t.Fatal("runtime allocation or finalizers lost", err)
	}
	current.SetUID("replacement")
	if _, err = api.Update(t.Context(), current, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.prepareMetalLBClass(t.Context(), want); !errors.Is(err, ErrOwned) {
		t.Fatal("replacement service accepted", err)
	}
}

func TestMetalLBBundleScopesBothControllers(t *testing.T) {
	objects, err := bootstrapObjects("metallb.yaml", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	scoped := 0
	for _, object := range objects {
		if object.GetKind() != "Deployment" && object.GetKind() != "DaemonSet" {
			continue
		}
		containers, _, e := unstructured.NestedSlice(object.Object, "spec", "template", "spec", "containers")
		if e != nil {
			t.Fatal(e)
		}
		for _, item := range containers {
			container, ok := item.(map[string]any)
			if !ok {
				t.Fatal("invalid container")
			}
			args, _, e := unstructured.NestedStringSlice(container, "args")
			if e != nil {
				t.Fatal(e)
			}
			for _, arg := range args {
				if arg == "--lb-class=loomplane.io/metallb" {
					scoped++
				}
			}
		}
	}
	if scoped != 2 {
		t.Fatal("both controller and speaker must use the explicit class", scoped)
	}
}

func TestMetalLBStatusRemovalRequiresControllerAncestry(t *testing.T) {
	c := bootstrapFixture(t)
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "loomplane-metallb-speaker", Namespace: bootstrapNamespace, UID: "controller", Labels: map[string]string{bootstrapOwner: "cluster-uid"}}}
	if _, err := c.Kube.AppsV1().DaemonSets(bootstrapNamespace).Create(t.Context(), ds, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "speaker", Namespace: bootstrapNamespace, UID: "pod", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: ds.Name, UID: ds.UID, Controller: ptr.To(true)}}}}
	if _, err := c.Kube.CoreV1().Pods(bootstrapNamespace).Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	status := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "metallb.io/v1beta1", "kind": "ConfigurationState"}}
	status.SetNamespace(bootstrapNamespace)
	status.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: pod.UID}})
	if owned, err := c.ownedMetalLBStatus(t.Context(), status, "cluster-uid"); err != nil || !owned {
		t.Fatal(owned, err)
	}
	if owned, err := c.ownedMetalLBStatus(t.Context(), status, "another-cluster"); err != nil || owned {
		t.Fatal("foreign controller accepted", owned, err)
	}
	status.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: "replaced"}})
	if owned, err := c.ownedMetalLBStatus(t.Context(), status, "cluster-uid"); err != nil || owned {
		t.Fatal("replacement pod accepted", owned, err)
	}
}

func TestLonghornHookWaitsForJobRetries(t *testing.T) {
	c := bootstrapFixture(t)
	if err := c.runLonghornHook(t.Context(), "cluster-uid", "uninstall"); !errors.Is(err, ErrRegistryPending) {
		t.Fatal(err)
	}
	api := c.Dynamic.Resource(bootstrapResources["Job"]).Namespace(bootstrapNamespace)
	job, err := api.Get(t.Context(), "longhorn-uninstall", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	job.Object["status"] = map[string]any{"failed": int64(1), "active": int64(1)}
	if _, err = api.Update(t.Context(), job, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.runLonghornHook(t.Context(), "cluster-uid", "uninstall"); !errors.Is(err, ErrRegistryPending) {
		t.Fatal("retrying job treated as terminal", err)
	}
	job.Object["status"] = map[string]any{"failed": int64(4), "conditions": []any{map[string]any{"type": "Failed", "status": "True"}}}
	if _, err = api.Update(t.Context(), job, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.runLonghornHook(t.Context(), "cluster-uid", "uninstall"); err == nil || errors.Is(err, ErrRegistryPending) {
		t.Fatal("terminal failure not reported", err)
	}
}
