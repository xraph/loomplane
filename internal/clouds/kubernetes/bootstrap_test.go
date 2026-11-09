package kubernetes

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
)

func bootstrapFixture(t *testing.T) *Client {
	t.Helper()
	kube := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps", UID: "apps-uid"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "cluster-uid"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", Labels: map[string]string{"kubernetes.io/os": "linux", "kubernetes.io/arch": "arm64"}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "storage-uid", Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}}, Provisioner: "example.csi", Parameters: map[string]string{"type": "ssd"}, MountOptions: []string{"discard"}, ReclaimPolicy: ptr.To(corev1.PersistentVolumeReclaimDelete), VolumeBindingMode: ptr.To(storagev1.VolumeBindingWaitForFirstConsumer), AllowVolumeExpansion: ptr.To(true)},
	)
	kube.PrependReactor("create", "selfsubjectaccessreviews", func(kubetesting.Action) (bool, runtime.Object, error) {
		return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	return &Client{Config: &Config{Namespace: "apps", Details: map[string]string{"registry_hostname": "images.example.com", "registry_setup": "automatic"}}, Kube: kube, Dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())}
}

func bootstrapWrites(c *Client) int {
	count := 0
	for _, action := range c.Dynamic.(*dynamicfake.FakeDynamicClient).Actions() {
		if action.GetVerb() == "create" || action.GetVerb() == "update" || action.GetVerb() == "patch" || action.GetVerb() == "delete" {
			count++
		}
	}
	return count
}

func TestBootstrapInstallsPinnedControllersAndRetainedClone(t *testing.T) {
	c := bootstrapFixture(t)
	before, err := c.Kube.StorageV1().StorageClasses().Get(t.Context(), "default", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.BootstrapManagedBuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c.Config.Details["architecture"] != "arm64" || c.Config.Details["ingress_class"] != "loomplane" {
		t.Fatalf("automatic discovery: %v", c.Config.Details)
	}
	clone, err := c.Dynamic.Resource(bootstrapResources["StorageClass"]).Get(t.Context(), c.Config.Details["storage_class"], metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if clone.Object["reclaimPolicy"] != "Retain" || clone.GetAnnotations()["storageclass.kubernetes.io/is-default-class"] != "" || clone.GetAnnotations()["loomplane.bootstrap.storage-source"] != "storage-uid" {
		t.Fatalf("unsafe clone: %v", clone.Object)
	}
	parameters, _, err := unstructured.NestedStringMap(clone.Object, "parameters")
	if err != nil || parameters["type"] != "ssd" {
		t.Fatal("lost driver parameters")
	}
	after, err := c.Kube.StorageV1().StorageClasses().Get(t.Context(), "default", metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("changed existing default storage class")
	}
	writes := bootstrapWrites(c)
	if writes < 40 {
		t.Fatalf("missing controller resources: %d", writes)
	}
	// Repeating the same save must reuse the original objects, not overwrite them.
	// The typed discovery client has no clone or ingress class, modelling their
	// deletion after saving. The original source UID still identifies storage.
	if err = c.BootstrapManagedBuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	if bootstrapWrites(c) != writes {
		t.Fatal("retry mutated an existing resource")
	}
	for kind, name := range map[string]string{"StorageClass": c.Config.Details["storage_class"], "IngressClass": bootstrapIngress} {
		if deleteErr := c.Dynamic.Resource(bootstrapResources[kind]).Delete(t.Context(), name, metav1.DeleteOptions{}); deleteErr != nil {
			t.Fatal(deleteErr)
		}
	}
	writes = bootstrapWrites(c)
	if err = c.BootstrapManagedBuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	if bootstrapWrites(c) != writes+2 {
		t.Fatal("deleted owned classes were not repaired")
	}
	for _, file := range []string{"cert-manager.yaml", "traefik.yaml"} {
		objects, err := bootstrapObjects(file, "cluster-uid")
		if err != nil {
			t.Fatal(err)
		}
		for _, object := range objects {
			if object.GetKind() == "Deployment" {
				containers, _, err := unstructured.NestedSlice(object.Object, "spec", "template", "spec", "containers")
				if err != nil {
					t.Fatal(err)
				}
				for _, value := range containers {
					container := value.(map[string]any)
					if !strings.Contains(container["image"].(string), "@sha256:") {
						t.Fatal("controller image is mutable")
					}
				}
			}
		}
	}
}

func TestBootstrapPreflightDoesNotTouchForeignResources(t *testing.T) {
	for _, failure := range []string{"collision", "permission", "partial-cert-manager", "no-storage", "ambiguous-ingress"} {
		t.Run(failure, func(t *testing.T) {
			c := bootstrapFixture(t)
			switch failure {
			case "collision":
				object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": map[string]any{"name": "loomplane-ingress-loomplane-system"}}}
				if _, err := c.bootstrapAPI(object).Create(t.Context(), object, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "permission":
				c.Kube.(*fake.Clientset).PrependReactor("create", "selfsubjectaccessreviews", func(a kubetesting.Action) (bool, runtime.Object, error) {
					review := a.(kubetesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
					return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: review.Spec.ResourceAttributes.Resource != "storageclasses"}}, nil
				})
			case "partial-cert-manager":
				object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition", "metadata": map[string]any{"name": "certificates.cert-manager.io"}}}
				if _, err := c.bootstrapAPI(object).Create(t.Context(), object, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "no-storage":
				if err := c.Kube.StorageV1().StorageClasses().Delete(t.Context(), "default", metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			case "ambiguous-ingress":
				for _, name := range []string{"first", "second"} {
					if _, err := c.Kube.NetworkingV1().IngressClasses().Create(t.Context(), &networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: name}}, metav1.CreateOptions{}); err != nil {
						t.Fatal(err)
					}
				}
			}
			writes := bootstrapWrites(c)
			if err := c.BootstrapManagedBuild(t.Context()); err == nil {
				t.Fatal("unsafe bootstrap accepted")
			}
			if bootstrapWrites(c) != writes {
				t.Fatal("preflight left partial infrastructure")
			}
		})
	}
}

func TestBootstrapReusesExternalControllers(t *testing.T) {
	c := bootstrapFixture(t)
	objects, err := bootstrapObjects("cert-manager.yaml", "external")
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		if object.GetKind() == "CustomResourceDefinition" {
			object.SetLabels(nil)
			if _, err = c.bootstrapAPI(object).Create(t.Context(), object, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = c.Kube.NetworkingV1().IngressClasses().Create(t.Context(), &networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "existing"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.Dynamic.(*dynamicfake.FakeDynamicClient).ClearActions()
	if err = c.BootstrapManagedBuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	if bootstrapWrites(c) != 1 || c.Config.Details["ingress_class"] != "existing" {
		t.Fatal("external controllers were changed")
	}
}

func TestManagedSetupWaitsForAddressAndChecksAreReadOnly(t *testing.T) {
	c := bootstrapFixture(t)
	c.Config.Details["ingress_class"] = "loomplane"
	c.Config.Details["storage_class"] = "retained"
	c.Config.Details["architecture"] = "arm64"
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "loomplane-ingress", Namespace: bootstrapNamespace, Labels: map[string]string{bootstrapOwner: "cluster-uid"}}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer}}
	if _, err := c.Kube.CoreV1().Services(bootstrapNamespace).Create(t.Context(), service, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Kube.AppsV1().Deployments(bootstrapNamespace).Create(t.Context(), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "controller", Namespace: bootstrapNamespace, Labels: map[string]string{bootstrapOwner: "cluster-uid"}}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	kube := c.Kube.(*fake.Clientset)
	kube.ClearActions()
	out := &Result{Details: map[string]string{}}
	for key, value := range c.Config.Details {
		out.Details[key] = value
	}
	if err := c.discoverManagedSetup(t.Context(), out); err != nil {
		t.Fatal(err)
	}
	if out.Details["registry_setup_status"] != "waiting" || len(out.Missing) != 2 {
		t.Fatalf("pending infrastructure marked ready: %+v", out)
	}
	for _, a := range kube.Actions() {
		if a.GetVerb() != "get" && a.GetVerb() != "list" {
			t.Fatal("health check mutated cluster")
		}
	}
	if err := kube.AppsV1().Deployments(bootstrapNamespace).Delete(t.Context(), "controller", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	service.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "203.0.113.10"}}
	if _, err := kube.CoreV1().Services(bootstrapNamespace).UpdateStatus(t.Context(), service, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	out.Missing = nil
	if err := c.discoverManagedSetup(t.Context(), out); err != nil {
		t.Fatal(err)
	}
	if out.Details["ingress_address"] != "203.0.113.10" || len(out.Missing) != 0 {
		t.Fatalf("address not discovered: %+v", out)
	}
	out.Details["registry_ingress"] = "managed"
	service.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "203.0.113.11"}}
	if _, err := kube.CoreV1().Services(bootstrapNamespace).UpdateStatus(t.Context(), service, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.discoverManagedSetup(t.Context(), out); err != nil {
		t.Fatal(err)
	}
	if out.Details["ingress_address"] != "203.0.113.11" || out.Details["registry_setup_status"] != "waiting" || len(out.Missing) != 1 || !strings.Contains(out.Missing[0], "is missing") {
		t.Fatalf("missed controller deletion or address change: %+v", out)
	}
}

func TestBootstrapExistingModeDoesNothing(t *testing.T) {
	c := bootstrapFixture(t)
	c.Config.Details["registry_setup"] = "existing"
	c.Kube.(*fake.Clientset).PrependReactor("*", "*", func(kubetesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("unexpected cluster request")
	})
	if err := c.BootstrapManagedBuild(t.Context()); err != nil {
		t.Fatal(err)
	}
}
