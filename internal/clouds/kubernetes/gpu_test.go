package kubernetes

import (
	"context"
	"encoding/pem"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func gpuNode(name string, labels map[string]string, gpus int64, taints ...corev1.Taint) corev1.Node {
	n := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Spec: corev1.NodeSpec{Taints: taints}}
	n.Status.Allocatable = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}
	if gpus > 0 {
		n.Status.Allocatable["nvidia.com/gpu"] = *resource.NewQuantity(gpus, resource.DecimalSI)
	}
	return n
}

func gpuFake(allowNodes bool, nodes ...corev1.Node) *Client {
	kube := fake.NewClientset()
	for i := range nodes {
		_, _ = kube.CoreV1().Nodes().Create(context.Background(), &nodes[i], metav1.CreateOptions{})
	}
	kube.PrependReactor("create", "selfsubjectaccessreviews", func(action clienttesting.Action) (bool, runtime.Object, error) {
		review := action.(clienttesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		review.Status.Allowed = allowNodes
		return true, review, nil
	})
	return &Client{Config: &Config{Namespace: "selected"}, Kube: kube}
}

func TestGPUInventoryGroupsNodesByKind(t *testing.T) {
	present := corev1.Taint{Key: "nvidia.com/gpu", Value: "present", Effect: corev1.TaintEffectNoSchedule}
	gke := map[string]string{"cloud.google.com/gke-accelerator": "nvidia-l4", "node.kubernetes.io/instance-type": "g2-standard-4"}
	// gke-2 is cordoned and gke-3 is not ready: their state taints must not
	// become tolerations. Only gke-1 is dedicated, so no node's pods get
	// that toleration either.
	cordoned := corev1.Taint{Key: "node.kubernetes.io/unschedulable", Effect: corev1.TaintEffectNoSchedule}
	notReady := corev1.Taint{Key: "node.kubernetes.io/not-ready", Effect: corev1.TaintEffectNoExecute}
	uninitialized := corev1.Taint{Key: "node.cloudprovider.kubernetes.io/uninitialized", Value: "true", Effect: corev1.TaintEffectNoSchedule}
	dedicated := corev1.Taint{Key: "dedicated", Value: "ml", Effect: corev1.TaintEffectNoSchedule}
	inv := gpuInventory([]corev1.Node{
		gpuNode("gke-1", gke, 1, present, dedicated), gpuNode("gke-2", gke, 1, present, cordoned), gpuNode("gke-3", gke, 1, present, notReady, uninitialized),
		gpuNode("eks-1", map[string]string{"node.kubernetes.io/instance-type": "g5.12xlarge"}, 4),
		gpuNode("cpu-1", map[string]string{"node.kubernetes.io/instance-type": "e2-standard-4"}, 0),
	})
	if inv.Status != provider.GPUStatusReady || len(inv.Nodes) != 2 {
		t.Fatalf("inventory: %+v", inv)
	}
	l4 := inv.Nodes[slices.IndexFunc(inv.Nodes, func(g provider.GPUNodeGroup) bool { return g.Kind == "nvidia-l4" })]
	if l4.Nodes != 3 || l4.PerNode != 1 || l4.Total != 3 || l4.NodeSelector["cloud.google.com/gke-accelerator"] != "nvidia-l4" || !l4.HourlyPriceKnown {
		t.Fatalf("L4 group: %+v", l4)
	}
	want := []provider.GPUToleration{{Key: "nvidia.com/gpu", Operator: "Equal", Value: "present", Effect: "NoSchedule"}, {Key: "nvidia.com/gpu", Operator: "Exists", Effect: "NoSchedule"}}
	if !reflect.DeepEqual(l4.Tolerations, want) {
		t.Fatalf("tolerations: %+v", l4.Tolerations)
	}
	a10g := inv.Nodes[slices.IndexFunc(inv.Nodes, func(g provider.GPUNodeGroup) bool { return g.Kind == "nvidia-a10g" })]
	if a10g.PerNode != 4 || a10g.NodeSelector["node.kubernetes.io/instance-type"] != "g5.12xlarge" {
		t.Fatalf("A10G group: %+v", a10g)
	}
	if !reflect.DeepEqual(a10g.Tolerations, []provider.GPUToleration{{Key: "nvidia.com/gpu", Operator: "Exists", Effect: "NoSchedule"}}) {
		t.Fatalf("A10G tolerations: %+v", a10g.Tolerations)
	}
}

// A taint on every node of a group is tolerated; one on only some nodes
// isn't, so the placement doesn't change as nodes come and go.
func TestGPUInventoryToleratesOnlyTaintsOnEveryNode(t *testing.T) {
	labels := map[string]string{"cloud.google.com/gke-accelerator": "nvidia-l4"}
	shared := corev1.Taint{Key: "gpu", Effect: corev1.TaintEffectNoExecute}
	inv := gpuInventory([]corev1.Node{
		gpuNode("a", labels, 1, shared, corev1.Taint{Key: "team", Value: "a", Effect: corev1.TaintEffectNoSchedule}),
		gpuNode("b", labels, 1, shared, corev1.Taint{Key: "team", Value: "b", Effect: corev1.TaintEffectNoSchedule}),
	})
	want := []provider.GPUToleration{{Key: "gpu", Operator: "Exists", Effect: "NoExecute"}, {Key: "nvidia.com/gpu", Operator: "Exists", Effect: "NoSchedule"}}
	if len(inv.Nodes) != 1 || !reflect.DeepEqual(inv.Nodes[0].Tolerations, want) {
		t.Fatalf("tolerations: %+v", inv.Nodes)
	}
}

func TestGPUInventoryStates(t *testing.T) {
	if inv := gpuInventory([]corev1.Node{gpuNode("cpu", nil, 0)}); inv.Status != provider.GPUStatusNone {
		t.Fatalf("no GPU nodes: %+v", inv)
	}
	noPlugin := gpuInventory([]corev1.Node{gpuNode("gpu", map[string]string{"cloud.google.com/gke-accelerator": "nvidia-l4"}, 0)})
	if noPlugin.Status != provider.GPUStatusNoDevicePlugin || !strings.Contains(noPlugin.Message, "device plugin") {
		t.Fatalf("no device plugin: %+v", noPlugin)
	}
}

func TestGPUInventoryUnlabelledNodeCannotPlace(t *testing.T) {
	inv := gpuInventory([]corev1.Node{gpuNode("gpu", nil, 2)})
	if inv.Status != provider.GPUStatusUnknown || !strings.Contains(inv.Message, "no node label says which kind") || len(inv.Nodes) != 1 || inv.Nodes[0].Kind != "" || len(inv.Nodes[0].NodeSelector) != 0 {
		t.Fatalf("unlabelled GPU node: %+v", inv)
	}
}

func TestGPUInventoryNoAllocatableStaysNoDevicePlugin(t *testing.T) {
	inv := gpuInventory([]corev1.Node{gpuNode("gpu", nil, 0, corev1.Taint{Key: "x"}), gpuNode("lbl", map[string]string{"nvidia.com/gpu.product": "Tesla-T4"}, 0)})
	if inv.Status != provider.GPUStatusNoDevicePlugin || !strings.Contains(inv.Message, "nothing advertises") {
		t.Fatalf("%+v", inv)
	}
}

func TestGPUsNodeListPageCapIsUnknown(t *testing.T) {
	c := gpuFake(true)
	pages := 0
	c.Kube.(*fake.Clientset).PrependReactor("list", "nodes", func(clienttesting.Action) (bool, runtime.Object, error) {
		pages++
		return true, &corev1.NodeList{ListMeta: metav1.ListMeta{Continue: "more"}}, nil
	})
	inv, err := c.GPUs(t.Context())
	if err != nil || inv.Status != provider.GPUStatusUnknown || !strings.Contains(inv.Message, "more nodes") || pages != 10 {
		t.Fatalf("%+v %v pages=%d", inv, err, pages)
	}
}

func TestGPUsListsNodes(t *testing.T) {
	c := gpuFake(true, gpuNode("gke-1", map[string]string{"cloud.google.com/gke-accelerator": "nvidia-l4"}, 1))
	inv, err := c.GPUs(t.Context())
	if err != nil || inv.Status != provider.GPUStatusReady || inv.CheckedAt.IsZero() {
		t.Fatalf("%+v %v", inv, err)
	}
}

func TestGPUsWithoutNodeListIsUnlisted(t *testing.T) {
	c := gpuFake(false, gpuNode("gke-1", map[string]string{"cloud.google.com/gke-accelerator": "nvidia-l4"}, 1))
	inv, err := c.GPUs(t.Context())
	if err != nil || inv.Status != provider.GPUStatusUnlisted || !strings.Contains(inv.Message, "ClusterRole") {
		t.Fatalf("%+v %v", inv, err)
	}
	if _, perr := inv.Place(provider.GPURequest{Kind: "nvidia-l4", Count: 1}); perr == nil {
		t.Fatal("an unlisted cluster must not place GPU work")
	}
}

func TestGPUsListErrorIsUnknown(t *testing.T) {
	c := gpuFake(true)
	c.Kube.(*fake.Clientset).PrependReactor("list", "nodes", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("boom")
	})
	inv, err := c.GPUs(t.Context())
	if err != nil || inv.Status != provider.GPUStatusUnknown {
		t.Fatalf("%+v %v", inv, err)
	}
}

func TestCheckReportsGPUDetails(t *testing.T) {
	for _, tc := range []struct {
		denied []string
		want   string
	}{{nil, provider.GPUStatusNone}, {[]string{"nodes:list"}, provider.GPUStatusUnlisted}} {
		s, _, _ := clusterFixture(t, false, tc.denied...)
		ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
		cfg, err := ParseConfig(kubeconfig(t, s.URL, ca), nil)
		if err != nil {
			t.Fatal(err)
		}
		c, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		result, err := c.Check(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if result.Details[provider.GPUDetailStatus] != tc.want || slices.Contains(result.Missing, "nodes:list") {
			t.Fatalf("details %+v missing %v", result.Details, result.Missing)
		}
		if _, ok := provider.ParseGPUInventory(result.Details); !ok {
			t.Fatal("inventory missing from details")
		}
	}
}
