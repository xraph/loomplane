package kubernetes

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestBareClusterCreatesNamespaceStorageAndNodeIngress(t *testing.T) {
	c := bootstrapFixture(t)
	if err := c.Kube.CoreV1().Namespaces().Delete(t.Context(), "apps", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.Kube.StorageV1().StorageClasses().Delete(t.Context(), "default", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	node, err := c.Kube.CoreV1().Nodes().Get(t.Context(), "worker", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Labels["kubernetes.io/hostname"] = "worker"
	node.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "192.0.2.10"}}
	if _, err = c.Kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.Config.Details["registry_storage"] = "local-path"
	c.Config.Details["registry_network"] = "node"
	if err = c.BootstrapManagedBuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c.Config.Details["storage_class"] != "lp-local-retained" || c.Config.Details["ingress_address"] != "192.0.2.10" {
		t.Fatal(c.Config.Details)
	}
	ns, err := c.Dynamic.Resource(bootstrapResources["Namespace"]).Get(t.Context(), "apps", metav1.GetOptions{})
	if err != nil || ns.GetLabels()[bootstrapOwner] != "cluster-uid" {
		t.Fatal("namespace was not created with ownership", err)
	}
	ingress, err := c.Dynamic.Resource(bootstrapResources["Deployment"]).Namespace(bootstrapNamespace).Get(t.Context(), "loomplane-ingress", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ingress.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"hostPort":443`) || !strings.Contains(string(raw), `ingressendpoint.ip=192.0.2.10`) || strings.Contains(string(raw), `ingressendpoint.publishedservice`) {
		t.Fatal("node ingress was not configured")
	}
}

func TestAdditionalBundlesAreValidAndPinned(t *testing.T) {
	for _, name := range []string{"local-path.yaml", "longhorn.yaml", "metallb.yaml"} {
		objects, err := bootstrapObjects(name, "cluster")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, object := range objects {
			if object.GetKind() == "Job" {
				t.Fatal("Helm lifecycle hooks must not run during setup")
			}
			for _, field := range []string{"containers", "initContainers"} {
				containers, _, getErr := unstructured.NestedSlice(object.Object, "spec", "template", "spec", field)
				if getErr != nil {
					t.Fatal(getErr)
				}
				for _, container := range containers {
					image, _ := container.(map[string]any)["image"].(string)
					if image != "" && !strings.Contains(image, "@sha256:") {
						t.Fatalf("unpinned image %s", image)
					}
				}
			}
		}
	}
}
func TestInvalidBareClusterSettingsMakeNoWrites(t *testing.T) {
	c := bootstrapFixture(t)
	c.Config.Details["registry_network"] = "metallb"
	c.Config.Details["load_balancer_addresses"] = ""
	if err := c.BootstrapManagedBuild(t.Context()); err == nil || bootstrapWrites(c) != 0 {
		t.Fatal("invalid pool wrote resources", err)
	}
	for _, pool := range []string{"", "127.0.0.1/32", "0.0.0.0/0", "192.0.2.20-192.0.2.10"} {
		if _, err := loadBalancerRanges(pool); err == nil {
			t.Fatal("accepted pool", pool)
		}
	}
}
func TestBareClusterForeignStorageCollisionPreventsNamespaceCreation(t *testing.T) {
	c := bootstrapFixture(t)
	c.Config.Details["registry_storage"] = "local-path"
	c.Dynamic = dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), &unstructured.Unstructured{Object: map[string]any{"apiVersion": "storage.k8s.io/v1", "kind": "StorageClass", "metadata": map[string]any{"name": "lp-local-retained"}}})
	if err := c.BootstrapManagedBuild(t.Context()); err == nil || bootstrapWrites(c) != 0 {
		t.Fatal("foreign class was adopted", err)
	}
}

func TestLoadBalancerPoolsRejectOverlap(t *testing.T) {
	for _, tc := range []struct {
		a, b    string
		overlap bool
	}{{"192.0.2.10-192.0.2.20", "192.0.2.0/24", true}, {"192.0.2.10-192.0.2.20", "192.0.2.20-192.0.2.30", true}, {"192.0.2.10-192.0.2.20", "192.0.2.21-192.0.2.30", false}, {"fd00::/64", "fd00::1-fd00::20", true}} {
		if poolsOverlap(tc.a, tc.b) != tc.overlap {
			t.Fatal(tc)
		}
	}
}

func TestReplicatedStorageRequiresThreeNodesBeforeWriting(t *testing.T) {
	c := bootstrapFixture(t)
	c.Config.Details["registry_storage"] = "longhorn"
	c.Kube.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: "v1.36.0"}
	if err := c.BootstrapManagedBuild(t.Context()); err == nil || bootstrapWrites(c) != 0 {
		t.Fatal("single-node replicated storage accepted", err)
	}
	node, err := c.Kube.CoreV1().Nodes().Get(t.Context(), "worker", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"worker-2", "worker-3"} {
		additionalNode := node.DeepCopy()
		additionalNode.Name = name
		if _, err = c.Kube.CoreV1().Nodes().Create(t.Context(), additionalNode, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err = c.BootstrapManagedBuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	class, err := c.Dynamic.Resource(bootstrapResources["StorageClass"]).Get(t.Context(), "lp-longhorn-retained", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	replicas, _, err := unstructured.NestedString(class.Object, "parameters", "numberOfReplicas")
	if err != nil || replicas != "3" {
		t.Fatal("replica policy missing", err)
	}
	policy, _, err := unstructured.NestedString(class.Object, "reclaimPolicy")
	if err != nil || policy != "Retain" {
		t.Fatal("data is not retained", err)
	}
}

func TestMetalLBPoolProtectsAllocatedNodeAddresses(t *testing.T) {
	c := bootstrapFixture(t)
	c.Config.Details["registry_network"] = "metallb"
	c.Config.Details["load_balancer_addresses"] = "192.0.2.10-192.0.2.20"
	node, err := c.Kube.CoreV1().Nodes().Get(t.Context(), "worker", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "192.0.2.10"}}
	if _, err = c.Kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.BootstrapManagedBuild(t.Context()); err == nil || bootstrapWrites(c) != 0 {
		t.Fatal("pool overlapped node address", err)
	}
	c.Config.Details["load_balancer_addresses"] = "192.0.2.30-192.0.2.40"
	if err = c.BootstrapManagedBuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	pool, err := c.Dynamic.Resource(bootstrapResources["IPAddressPool"]).Namespace(bootstrapNamespace).Get(t.Context(), "loomplane-registry", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	auto, _, err := unstructured.NestedBool(pool.Object, "spec", "autoAssign")
	if err != nil || auto {
		t.Fatal("registry pool exposed to unrelated services", err)
	}
}
