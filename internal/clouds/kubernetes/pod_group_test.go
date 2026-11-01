package kubernetes

import (
	"context"
	"encoding/json"
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
	"github.com/xraph/loomplane/pkg/provider"
)

func podGroupFixture(t *testing.T) (cpprovider.ServiceSpec, map[string]string) {
	t.Helper()
	group := provider.PodGroup{Sidecars: []provider.Sidecar{{Name: "helper", Image: "registry.example.test/helper@sha256:" + strings.Repeat("a", 64), Architecture: "arm64", CPUMillis: 250, MemoryMiB: 128, Readiness: provider.ContainerReadiness{Port: 9001}, Environment: []string{"SELECTED"}, Mounts: []provider.ScratchMount{{Volume: "scratch", Path: "/scratch"}}}}, Scratch: []provider.ScratchVolume{{Name: "scratch", SizeMiB: 64}}, Mounts: []provider.ScratchMount{{Volume: "scratch", Path: "/scratch"}}, SurgeReplicas: 1}
	raw, err := json.Marshal(group)
	if err != nil {
		t.Fatal(err)
	}
	spec := cpprovider.ServiceSpec{Name: "main", Image: "registry.example.test/main@sha256:" + strings.Repeat("b", 64), Env: map[string]string{"SELECTED": "visible", "PRIVATE": "hidden"}, Ports: []cpprovider.PortSpec{{Container: 8080}}}
	spec.Resources.CPUMillis = 500
	spec.Resources.MemoryMB = 256
	return spec, map[string]string{owned.LabelPodGroup: string(raw), owned.LabelWorkloadCapability: provider.PodGroupCapabilityRevision, owned.LabelArchitecture: "arm64", owned.LabelHealth: "/ready"}
}
func TestPodGroupScratchRecreated(t *testing.T) {
	spec, labels := podGroupFixture(t)
	render := func() *corev1.PodTemplateSpec {
		p, _, err := runtimePod(spec, labels, owned.RuntimeValues{}, &corev1.Secret{}, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	first, second := render(), render()
	if len(first.Spec.Volumes) != 1 || first.Spec.Volumes[0].EmptyDir == nil || first.Spec.Volumes[0].EmptyDir.SizeLimit.Value() != 64<<20 {
		t.Fatal("scratch must use bounded pod-local storage")
	}
	first.Spec.Volumes[0].EmptyDir.SizeLimit.Set(1)
	if second.Spec.Volumes[0].EmptyDir.SizeLimit.Value() != 64<<20 {
		t.Fatal("replacement inherited mutable scratch state")
	}
	var cpu, memory, scratch int64
	for _, c := range second.Spec.Containers {
		cpu += c.Resources.Requests.Cpu().MilliValue()
		memory += c.Resources.Requests.Memory().Value()
		scratch += c.Resources.Requests.StorageEphemeral().Value()
	}
	if cpu != 750 || memory != 384<<20 || scratch != 64<<20 {
		t.Fatalf("unreserved requests %d/%d/%d", cpu, memory, scratch)
	}
	helper := second.Spec.Containers[1]
	if len(helper.Env) != 1 || helper.Env[0].Name != "SELECTED" || len(helper.VolumeMounts) != 1 {
		t.Fatal("sidecar inherited undeclared settings")
	}
	if second.Spec.AutomountServiceAccountToken == nil || *second.Spec.AutomountServiceAccountToken {
		t.Fatal("pod group inherited API token")
	}
}
func TestPodGroupReadiness(t *testing.T) {
	spec, labels := podGroupFixture(t)
	pod, _, err := runtimePod(spec, labels, owned.RuntimeValues{}, &corev1.Secret{}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pod.Spec.Containers) != 2 || pod.Spec.Containers[0].ReadinessProbe == nil || pod.Spec.Containers[1].ReadinessProbe.TCPSocket.Port.IntValue() != 9001 {
		t.Fatal("required container has no probe")
	}
	replicas := int32(1)
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: "selected", UID: "uid", Generation: 1}, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Template: *pod}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 1, UnavailableReplicas: 1}}
	// The Deployment controller keeps a pod unavailable when its helper probe fails,
	// even if the application's probe succeeds.
	client := &Client{Config: &Config{Namespace: "selected"}, Kube: fake.NewClientset(deployment)}
	runtime := &Runtime{}
	if err = runtime.waitReady(t.Context(), client, deployment, 10*time.Millisecond); err == nil {
		t.Fatal("unavailable sidecar accepted")
	}
	deployment.Status.AvailableReplicas = 1
	deployment.Status.UnavailableReplicas = 0
	if !deploymentReady(deployment) {
		t.Fatal("all containers ready but rollout rejected")
	}
}
func TestPodGroupRuntimeRejectsUnreviewedMetadata(t *testing.T) {
	for _, mutate := range []func(map[string]string){func(m map[string]string) { m[owned.LabelWorkloadCapability] = "unknown" }, func(m map[string]string) { m[owned.LabelArchitecture] = "amd64" }, func(m map[string]string) { m[owned.LabelPodGroup] = `{"host_network":true}` }} {
		spec, labels := podGroupFixture(t)
		mutate(labels)
		if _, _, err := runtimePod(spec, labels, owned.RuntimeValues{}, &corev1.Secret{}, nil, ""); err == nil {
			t.Fatal("invalid runtime metadata accepted")
		}
	}
}

func TestPodGroupPreflightRejectsBeforeMutation(t *testing.T) {
	spec, labels := podGroupFixture(t)
	labels[owned.LabelWorkloadCapability] = "unknown"
	kube := fake.NewClientset()
	runtime := &Runtime{Session: func(context.Context, map[string]string) (*Client, error) {
		return &Client{Config: &Config{Namespace: "selected"}, Kube: kube}, nil
	}}
	if _, err := runtime.Apply(t.Context(), id.New(id.PrefixInstance), spec, labels); err == nil {
		t.Fatal("unreviewed pod group accepted")
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() != "get" && action.GetVerb() != "list" {
			t.Fatal("invalid pod group caused mutation", action.GetVerb(), action.GetResource())
		}
	}
}
func TestPodGroupStrategyReservesExactSurge(t *testing.T) {
	_, labels := podGroupFixture(t)
	deployment := &appsv1.Deployment{}
	if err := podGroupStrategy(deployment, labels); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Strategy.RollingUpdate.MaxSurge.IntValue() != 1 || deployment.Spec.Strategy.RollingUpdate.MaxUnavailable.IntValue() != 0 {
		t.Fatal("unreviewed surge policy")
	}
	group, err := reviewedPodGroup(labels)
	if err != nil {
		t.Fatal(err)
	}
	group.SurgeReplicas = 0
	raw, _ := json.Marshal(group)
	labels[owned.LabelPodGroup] = string(raw)
	if err = podGroupStrategy(deployment, labels); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Strategy.RollingUpdate.MaxSurge.IntValue() != 0 || deployment.Spec.Strategy.RollingUpdate.MaxUnavailable.IntValue() != 1 {
		t.Fatal("zero surge cannot make progress")
	}
}
