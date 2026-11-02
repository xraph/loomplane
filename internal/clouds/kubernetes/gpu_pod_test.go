package kubernetes

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

const l4Placement = `{"kind":"nvidia-l4","count":2,"node_selector":{"cloud.google.com/gke-accelerator":"nvidia-l4"},"tolerations":[{"key":"nvidia.com/gpu","operator":"Exists","effect":"NoSchedule"}]}`

func gpuServiceSpec() cpprovider.ServiceSpec {
	return cpprovider.ServiceSpec{Name: "server", Image: "img", Resources: cpprovider.ResourceSpec{CPUMillis: 2000, MemoryMB: 8192}}
}

func TestRuntimePodRequestsGPUs(t *testing.T) {
	labels := map[string]string{owned.LabelArchitecture: "amd64", owned.LabelShmMB: "2048", owned.LabelGPU: l4Placement}
	tpl, _, err := runtimePod(gpuServiceSpec(), labels, owned.RuntimeValues{}, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	c := tpl.Spec.Containers[0]
	if q := c.Resources.Limits["nvidia.com/gpu"]; q.Value() != 2 {
		t.Fatalf("gpu limit: %v", c.Resources.Limits)
	}
	if q := c.Resources.Requests["nvidia.com/gpu"]; q.Value() != 2 {
		t.Fatalf("gpu request: %v", c.Resources.Requests)
	}
	if tpl.Spec.NodeSelector["cloud.google.com/gke-accelerator"] != "nvidia-l4" || tpl.Spec.NodeSelector["kubernetes.io/arch"] != "amd64" {
		t.Fatalf("selector: %v", tpl.Spec.NodeSelector)
	}
	if len(tpl.Spec.Tolerations) != 1 || tpl.Spec.Tolerations[0].Key != "nvidia.com/gpu" || tpl.Spec.Tolerations[0].Operator != corev1.TolerationOpExists || tpl.Spec.Tolerations[0].Effect != corev1.TaintEffectNoSchedule {
		t.Fatalf("tolerations: %+v", tpl.Spec.Tolerations)
	}
	i := slices.IndexFunc(tpl.Spec.Volumes, func(v corev1.Volume) bool { return v.Name == "dshm" })
	if i < 0 || tpl.Spec.Volumes[i].EmptyDir.Medium != corev1.StorageMediumMemory || tpl.Spec.Volumes[i].EmptyDir.SizeLimit.String() != "2Gi" {
		t.Fatalf("shm volume: %+v", tpl.Spec.Volumes)
	}
	if !slices.ContainsFunc(c.VolumeMounts, func(m corev1.VolumeMount) bool { return m.Name == "dshm" && m.MountPath == "/dev/shm" }) {
		t.Fatalf("shm mount: %+v", c.VolumeMounts)
	}
}

func TestRuntimePodGPUWithoutCPUOrMemoryLimits(t *testing.T) {
	spec := cpprovider.ServiceSpec{Name: "server", Image: "img"}
	tpl, _, err := runtimePod(spec, map[string]string{owned.LabelGPU: `{"kind":"nvidia-l4","count":1}`}, owned.RuntimeValues{}, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	r := tpl.Spec.Containers[0].Resources
	if q := r.Limits["nvidia.com/gpu"]; q.Value() != 1 || len(r.Limits) != 1 {
		t.Fatalf("limits: %v", r.Limits)
	}
	if q := r.Requests["nvidia.com/gpu"]; q.Value() != 1 {
		t.Fatalf("requests: %v", r.Requests)
	}
	if tpl.Spec.NodeSelector != nil {
		t.Fatalf("selector: %v", tpl.Spec.NodeSelector)
	}
}

func TestRuntimePodRefusesConflictingArchitectureAndBadLabels(t *testing.T) {
	conflict := `{"kind":"nvidia-l4","count":1,"node_selector":{"kubernetes.io/arch":"arm64"}}`
	for name, labels := range map[string]map[string]string{
		"architecture": {owned.LabelArchitecture: "amd64", owned.LabelGPU: conflict},
		"placement":    {owned.LabelGPU: `{`},
		"count":        {owned.LabelGPU: `{"kind":"nvidia-l4","count":0}`},
		"shm":          {owned.LabelShmMB: "lots"},
	} {
		if _, _, err := runtimePod(gpuServiceSpec(), labels, owned.RuntimeValues{}, nil, nil, ""); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	same := map[string]string{owned.LabelArchitecture: "arm64", owned.LabelGPU: conflict}
	if _, _, err := runtimePod(gpuServiceSpec(), same, owned.RuntimeValues{}, nil, nil, ""); err != nil {
		t.Fatalf("matching architecture refused: %v", err)
	}
}

func TestRuntimePodWithoutGPUHasNoGPUFields(t *testing.T) {
	labels := map[string]string{owned.LabelArchitecture: "amd64", owned.LabelRevision: "r1"}
	tpl, _, err := runtimePod(gpuServiceSpec(), labels, owned.RuntimeValues{}, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tpl.Annotations) != 1 || tpl.Annotations[owned.LabelRevision] != "r1" || len(tpl.Labels) != 0 {
		t.Fatalf("metadata: %+v", tpl.ObjectMeta)
	}
	if len(tpl.Spec.Tolerations) != 0 || len(tpl.Spec.Volumes) != 0 || len(tpl.Spec.NodeSelector) != 1 || tpl.Spec.NodeSelector["kubernetes.io/arch"] != "amd64" {
		t.Fatalf("spec: %+v", tpl.Spec)
	}
	c := tpl.Spec.Containers[0]
	if len(c.VolumeMounts) != 0 || len(c.Resources.Limits) != 2 || len(c.Resources.Requests) != 2 {
		t.Fatalf("container: %+v", c)
	}
	if _, ok := c.Resources.Limits["nvidia.com/gpu"]; ok {
		t.Fatal("gpu limit on a CPU pod")
	}
	bare, _, err := runtimePod(cpprovider.ServiceSpec{Name: "server", Image: "img"}, nil, owned.RuntimeValues{}, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if bare.Spec.NodeSelector != nil || bare.Spec.Volumes != nil || bare.Spec.Tolerations != nil || bare.Spec.Containers[0].Resources.Limits != nil {
		t.Fatalf("bare pod: %+v", bare.Spec)
	}
}

func gpuRuntime(kube *fake.Clientset) *Runtime {
	client := &Client{Config: &Config{Namespace: "selected"}, Kube: kube}
	return &Runtime{Wait: 50 * time.Millisecond, Session: func(context.Context, map[string]string) (*Client, error) { return client, nil }}
}

func gpuWorkloadLabels() map[string]string {
	return map[string]string{owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelGPU: l4Placement}
}

func TestGPUDeploymentRecreates(t *testing.T) {
	kube := fake.NewClientset()
	runtime := gpuRuntime(kube)
	runtime.WaitReady = func(context.Context, *Client, string) error { return nil }
	apply := func(labels map[string]string) *appsv1.Deployment {
		t.Helper()
		iid := id.New(id.PrefixInstance)
		if _, err := runtime.Apply(t.Context(), iid, gpuServiceSpec(), labels); err != nil {
			t.Fatal(err)
		}
		d, err := kube.AppsV1().Deployments("selected").Get(t.Context(), WorkloadName(iid), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if d := apply(gpuWorkloadLabels()); d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("strategy: %+v", d.Spec.Strategy)
	}
	cpu := gpuWorkloadLabels()
	delete(cpu, owned.LabelGPU)
	if d := apply(cpu); d.Spec.Strategy.Type != "" {
		t.Fatalf("a CPU deployment changed strategy: %+v", d.Spec.Strategy)
	}
}

func unschedulablePod(name string, labels map[string]string, message string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "selected", Labels: labels}, Status: corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable, Message: message}}}}
}

func TestWaitReportsUnschedulableGPU(t *testing.T) {
	labels := gpuWorkloadLabels()
	iid := id.New(id.PrefixInstance)
	kube := fake.NewClientset(
		unschedulablePod("other", map[string]string{owned.LabelInstance: "inst_other"}, "0/3 nodes are available: 3 Insufficient nvidia.com/gpu."),
		unschedulablePod("cpu", objectLabels(iid, labels), "0/3 nodes are available: 3 Insufficient cpu."),
	)
	_, err := gpuRuntime(kube).Apply(t.Context(), iid, gpuServiceSpec(), labels)
	if err == nil || strings.Contains(err.Error(), "no node has") {
		t.Fatalf("a GPU error from foreign or non-GPU pods: %v", err)
	}
	if err = kube.CoreV1().Pods("selected").Delete(t.Context(), "cpu", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	pod := unschedulablePod("gpu", objectLabels(iid, labels), "0/3 nodes are available: 3 Insufficient nvidia.com/gpu.")
	if _, err = kube.CoreV1().Pods("selected").Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err = gpuRuntime(kube).Apply(t.Context(), iid, gpuServiceSpec(), labels)
	if err == nil || !strings.Contains(err.Error(), "no node has 2 free NVIDIA L4 GPUs") || !strings.Contains(err.Error(), "Insufficient nvidia.com/gpu") {
		t.Fatalf("error: %v", err)
	}
}

func TestPersistentWaitReportsUnschedulableGPU(t *testing.T) {
	r, c, labels, sp := persistentFixture()
	r.WaitReady, r.Wait = nil, 50*time.Millisecond
	labels[owned.LabelGPU] = l4Placement
	iid := id.New(id.PrefixInstance)
	podLabels := objectLabels(iid, labels)
	podLabels[owned.LabelConnection] = labels[owned.LabelConnection]
	if _, err := c.Kube.CoreV1().Pods("selected").Create(t.Context(), unschedulablePod("gpu", podLabels, "0/3 nodes are available: 3 Insufficient nvidia.com/gpu."), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err := r.Apply(t.Context(), iid, sp, labels)
	if err == nil || !strings.Contains(err.Error(), "no node has 2 free NVIDIA L4 GPUs") {
		t.Fatalf("error: %v", err)
	}
}

func TestStatefulSetWithDataAndWeightsGetsTwoClaims(t *testing.T) {
	r, c, labels, sp := persistentFixture()
	labels[owned.LabelGPU] = l4Placement
	labels[owned.LabelShmMB] = "1024"
	sp.Volumes = append(sp.Volumes, cpprovider.VolumeSpec{Name: "weights", Type: "persistent", MountPath: "/models", SizeMB: 51200})
	iid := id.New(id.PrefixInstance)
	if _, err := r.Apply(t.Context(), iid, sp, labels); err != nil {
		t.Fatal(err)
	}
	claims, err := c.Kube.CoreV1().PersistentVolumeClaims("selected").List(t.Context(), metav1.ListOptions{})
	if err != nil || len(claims.Items) != 2 {
		t.Fatalf("claims: %+v %v", claims, err)
	}
	roles := make([]string, 0, len(claims.Items))
	for _, claim := range claims.Items {
		roles = append(roles, claim.Labels[owned.LabelVolumeRole])
	}
	slices.Sort(roles)
	if !slices.Equal(roles, []string{"data", "weights"}) {
		t.Fatalf("claim roles: %v", roles)
	}
	set, err := c.Kube.AppsV1().StatefulSets("selected").Get(t.Context(), WorkloadName(iid), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(set.Spec.Template.Spec.Volumes))
	for _, v := range set.Spec.Template.Spec.Volumes {
		names = append(names, v.Name)
	}
	if !slices.Contains(names, "data") || !slices.Contains(names, "weights") || !slices.Contains(names, "dshm") {
		t.Fatalf("volumes: %v", names)
	}
}
