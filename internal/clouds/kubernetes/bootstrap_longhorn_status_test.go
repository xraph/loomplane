package kubernetes

import (
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestLonghornStaticStorageClassIsManaged(t *testing.T) {
	objects, err := bootstrapObjects("longhorn.yaml", "cluster-owner")
	if err != nil {
		t.Fatal(err)
	}
	class := objects[0]
	if class.GetKind() != "StorageClass" || class.GetName() != "lp-longhorn-static" || class.GetLabels()[bootstrapOwner] != "cluster-owner" || class.Object["reclaimPolicy"] != "Retain" {
		t.Fatal("static storage must be owned and retained before controllers start", class)
	}
	for _, object := range objects {
		if object.GetKind() == "ConfigMap" && object.GetName() == "longhorn-default-setting" {
			settings, _, getErr := unstructured.NestedString(object.Object, "data", "default-setting.yaml")
			if getErr != nil || !strings.Contains(settings, `default-longhorn-static-storage-class: "lp-longhorn-static"`) {
				t.Fatal("Longhorn would create an unmanaged static class", settings, getErr)
			}
			return
		}
	}
	t.Fatal("missing Longhorn defaults")
}

func TestLonghornReadinessRequiresCSIAndHealthyStorageNodes(t *testing.T) {
	c := bootstrapFixture(t)
	resource := schema.GroupVersionResource{Group: "longhorn.io", Version: "v1beta2", Resource: "nodes"}
	objects := []runtime.Object{}
	for i := 0; i < 3; i++ {
		conditions := make([]any, 0, 7)
		for _, name := range []string{"Ready", "Schedulable", "RequiredPackages", "MountPropagation", "KernelModulesLoaded", "Multipathd", "NFSClientInstalled"} {
			conditions = append(conditions, map[string]any{"type": name, "status": "True"})
		}
		objects = append(objects, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "longhorn.io/v1beta2", "kind": "Node", "metadata": map[string]any{"name": fmt.Sprintf("node-%d", i), "namespace": bootstrapNamespace}, "spec": map[string]any{"allowScheduling": true}, "status": map[string]any{"conditions": conditions, "diskStatus": map[string]any{"disk": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}, map[string]any{"type": "Schedulable", "status": "True"}}}}}}})
	}
	c.Dynamic = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{resource: "NodeList"}, objects...)
	var missing []string
	check := func() {
		t.Helper()
		missing = nil
		if err := c.discoverLonghorn(t.Context(), func(s string) { missing = append(missing, s) }); err != nil {
			t.Fatal(err)
		}
	}
	check()
	if len(missing) != 1 || !strings.Contains(missing[0], "CSI") {
		t.Fatal("missing CSI marked ready", missing)
	}
	driver := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "longhorn-csi-plugin", Namespace: bootstrapNamespace, Generation: 2}, Status: appsv1.DaemonSetStatus{ObservedGeneration: 2, DesiredNumberScheduled: 3, NumberReady: 3}}
	if _, err := c.Kube.AppsV1().DaemonSets(bootstrapNamespace).Create(t.Context(), driver, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	check()
	if len(missing) != 0 {
		t.Fatal(missing)
	}
	api := c.Dynamic.Resource(resource).Namespace(bootstrapNamespace)
	node, err := api.Get(t.Context(), "node-0", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = unstructured.SetNestedSlice(node.Object, []any{map[string]any{"type": "RequiredPackages", "status": "False", "message": "Missing packages: [open-iscsi]"}}, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
	if _, err = api.Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	check()
	if len(missing) < 2 || !strings.Contains(strings.Join(missing, " "), "open-iscsi") {
		t.Fatal("host prerequisite failure hidden", missing)
	}
	if err = unstructured.SetNestedField(node.Object, nil, "status", "diskStatus"); err != nil {
		t.Fatal(err)
	}
	if _, err = api.Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	check()
	if len(missing) == 0 {
		t.Fatal("uninitialized disks marked ready")
	}
}
