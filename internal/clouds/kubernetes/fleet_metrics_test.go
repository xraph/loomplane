package kubernetes

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/xraph/loomplane/internal/clouds/owned"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func fleetMetricFixture(t *testing.T) (*Client, FleetMetricTarget, time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	iid := id.New(id.PrefixInstance)
	receipt := ControllerReceipt{Scope: domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "environment"}, ConnectionID: "connection", InstanceID: iid.String(), PlanID: "plan", RuntimeRef: "runtime-ref", ServiceID: "api", Container: "api", Image: "registry.example/api@sha256:" + strings.Repeat("a", 64), Namespace: "apps", NamespaceUID: "namespace-uid", ClusterCA: strings.Repeat("b", 64), ServiceAccount: "default", DeploymentUID: "deployment-uid", DeploymentGeneration: 1, RuntimeRevision: "revision"}
	labels := objectLabels(iid, map[string]string{owned.LabelOrg: "org", owned.LabelProject: "project", owned.LabelEnvironment: "environment"})
	controller := true
	replicas := int32(1)
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: WorkloadName(iid), Namespace: "apps", UID: "deployment-uid", Generation: 1, ResourceVersion: "1", Labels: labels}, Spec: appsv1.DeploymentSpec{Replicas: &replicas}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, ReadyReplicas: 1}}
	set := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "replicas", Namespace: "apps", UID: "replicas-uid", Labels: labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: dep.Name, UID: dep.UID, Controller: &controller}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-pod", Namespace: "apps", UID: "pod-uid", ResourceVersion: "1", Labels: labels, CreationTimestamp: metav1.NewTime(now.Add(-time.Hour)), OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: set.Name, UID: set.UID, Controller: &controller}}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: receipt.Image, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "api", ContainerID: "containerd://synthetic-container", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-time.Hour))}}}}}}
	dep.Spec.Template = corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: map[string]string{owned.LabelRevision: receipt.RuntimeRevision}}, Spec: pod.Spec}
	k := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps", UID: "namespace-uid"}}, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "runtime", Namespace: "apps", UID: "sa-uid"}}, dep, set, pod)
	metric := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "metrics.k8s.io/v1beta1", "kind": "PodMetrics", "metadata": map[string]any{"name": "api-pod", "namespace": "apps"}, "timestamp": now.Add(-10 * time.Second).Format(time.RFC3339), "window": "10s", "containers": []any{map[string]any{"name": "api", "usage": map[string]any{"cpu": "125m", "memory": "64Mi"}}}}}
	dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	if err := dynamic.Tracker().Create(fleetPodMetricsResource, metric, "apps"); err != nil {
		t.Fatal(err)
	}
	return &Client{Config: &Config{Namespace: "apps", Details: map[string]string{"cluster_ca": receipt.ClusterCA}}, Kube: k, Dynamic: dynamic}, FleetMetricTarget{Controller: receipt, ServiceAccount: "runtime", ServiceAccountUID: "sa-uid"}, now
}

func TestFleetRuntimeMetricsObservedUnits(t *testing.T) {
	c, target, now := fleetMetricFixture(t)
	got, err := ReadFleetControllerMetrics(t.Context(), c, target, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.DesiredReplicas == nil || *got.DesiredReplicas != 1 || got.CurrentReplicas == nil || *got.CurrentReplicas != 1 || got.ReadyReplicas == nil || *got.ReadyReplicas != 1 {
		t.Fatalf("replicas: %+v", got)
	}
	if got.CPUUsageMillicores == nil || *got.CPUUsageMillicores != 125 || got.CPURequestMillicores == nil || *got.CPURequestMillicores != 500 || got.MemoryBytes == nil || *got.MemoryBytes != 64*1024*1024 || !got.MetricsAt.Equal(now.Add(-10*time.Second)) {
		t.Fatalf("units/freshness: %+v", got)
	}
	for _, a := range c.Kube.(*fake.Clientset).Actions() {
		if a.GetVerb() != "get" && a.GetVerb() != "list" {
			t.Fatalf("observer wrote: %v", a)
		}
	}
}
func TestFleetRuntimeMetricsMissingKeepsReplicas(t *testing.T) {
	c, target, now := fleetMetricFixture(t)
	c.Dynamic = nil
	got, err := ReadFleetControllerMetrics(t.Context(), c, target, now)
	if err != nil || got.ReadyReplicas == nil || *got.ReadyReplicas != 1 || got.CPUUsageMillicores != nil || got.MemoryBytes != nil || !got.Partial {
		t.Fatalf("missing metrics: %+v %v", got, err)
	}
}
func TestFleetRuntimeMetricsIdentityDenial(t *testing.T) {
	for _, which := range []string{"namespace", "cluster", "service-account", "controller"} {
		t.Run(which, func(t *testing.T) {
			c, target, now := fleetMetricFixture(t)
			switch which {
			case "namespace":
				target.Controller.NamespaceUID = "other"
			case "cluster":
				target.Controller.ClusterCA = strings.Repeat("c", 64)
			case "service-account":
				target.ServiceAccountUID = "other"
			case "controller":
				target.Controller.DeploymentUID = "other"
			}
			if _, err := ReadFleetControllerMetrics(t.Context(), c, target, now); !errors.Is(err, ErrFleetMetricIdentity) {
				t.Fatalf("identity accepted: %v", err)
			}
			for _, a := range c.Dynamic.(*dynamicfake.FakeDynamicClient).Actions() {
				t.Fatalf("identity failure reached metrics: %v", a)
			}
		})
	}
}
func TestFleetRuntimeMetricsRejectsPodReplacement(t *testing.T) {
	c, target, now := fleetMetricFixture(t)
	count := 0
	c.Kube.(*fake.Clientset).PrependReactor("get", "pods", func(_ ktesting.Action) (bool, runtime.Object, error) {
		count++
		p, err := c.Kube.(*fake.Clientset).Tracker().Get(corev1.SchemeGroupVersion.WithResource("pods"), "apps", "api-pod")
		if err != nil {
			return true, nil, err
		}
		pod := p.(*corev1.Pod).DeepCopy()
		pod.UID = types.UID("replacement")
		return true, pod, nil
	})
	got, err := ReadFleetControllerMetrics(t.Context(), c, target, now)
	if err != nil || count == 0 || got.CPUUsageMillicores != nil || got.MemoryBytes != nil || !got.Partial {
		t.Fatalf("replaced pod metric accepted: %+v %v", got, err)
	}
}
func TestFleetRuntimeMetricsRejectsStaleAndPartialSamples(t *testing.T) {
	for _, which := range []string{"stale", "future", "before-start", "missing-container", "wrong-uid", "wrong-owner"} {
		t.Run(which, func(t *testing.T) {
			c, target, now := fleetMetricFixture(t)
			if which == "wrong-owner" {
				c.Kube.(*fake.Clientset).PrependReactor("get", "replicasets", func(_ ktesting.Action) (bool, runtime.Object, error) {
					return true, &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "replicas", Namespace: "apps", UID: "different"}}, nil
				})
			} else {
				c.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("get", "pods", func(_ ktesting.Action) (bool, runtime.Object, error) {
					object, err := c.Dynamic.(*dynamicfake.FakeDynamicClient).Tracker().Get(fleetPodMetricsResource, "apps", "api-pod")
					if err != nil {
						return true, nil, err
					}
					m := object.(*unstructured.Unstructured).DeepCopy()
					switch which {
					case "stale":
						m.Object["timestamp"] = now.Add(-2 * time.Minute).Format(time.RFC3339)
					case "future":
						m.Object["timestamp"] = now.Add(time.Minute).Format(time.RFC3339)
					case "before-start":
						m.Object["window"] = "2h"
					case "missing-container":
						m.Object["containers"] = []any{}
					case "wrong-uid":
						m.SetUID("foreign")
					}
					return true, m, nil
				})
			}
			got, err := ReadFleetControllerMetrics(t.Context(), c, target, now)
			if err != nil || got.CPUUsageMillicores != nil || got.MemoryBytes != nil || !got.Partial {
				t.Fatalf("invalid metric accepted: %+v %v", got, err)
			}
		})
	}
}

type fleetObservationHTTP func(*http.Request) (*http.Response, error)

func (f fleetObservationHTTP) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestFleetRuntimeMetricsTransportScopeAndBounds(t *testing.T) {
	calls := 0
	next := fleetObservationHTTP(func(_ *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	transport := newFleetObservationTransport(next, "apps", "runtime")
	for _, raw := range []string{"https://cluster/api/v1/pods", "https://cluster/api/v1/namespaces/other/pods", "https://cluster/api/v1/namespaces/apps/secrets", "https://cluster/api/v1/namespaces/apps/pods?watch=true", "https://cluster/apis/metrics.k8s.io/v1beta1/namespaces/other/pods/name"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		if rejected, roundErr := transport.RoundTrip(req); roundErr == nil {
			rejected.Body.Close()
			t.Fatalf("scope escape: %s", raw)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://cluster/api/v1/namespaces/apps/serviceaccounts/runtime/token", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rejected, roundErr := transport.RoundTrip(req); roundErr == nil {
		rejected.Body.Close()
		t.Fatal("token mutation permitted")
	}
	if calls != 0 {
		t.Fatal("rejected request reached transport")
	}
	req, err = http.NewRequestWithContext(t.Context(), http.MethodGet, "https://cluster/api/v1/namespaces/apps/pods?limit=65", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if calls != 1 {
		t.Fatal("valid read missing")
	}
	large := newFleetObservationTransport(fleetObservationHTTP(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", (2<<20)+1)))}, nil
	}), "apps", "runtime")
	if rejected, roundErr := large.RoundTrip(req); roundErr == nil {
		rejected.Body.Close()
		t.Fatal("oversized response accepted")
	}
}

func TestFleetRuntimeMetricsControllerTemplateDrift(t *testing.T) {
	for _, which := range []string{"image", "service-account", "revision", "replacement-during-read"} {
		t.Run(which, func(t *testing.T) {
			client, target, now := fleetMetricFixture(t)
			k := client.Kube.(*fake.Clientset)
			iid, err := id.ParseWithPrefix(target.Controller.InstanceID, id.PrefixInstance)
			if err != nil {
				t.Fatal(err)
			}
			dep, err := k.AppsV1().Deployments(target.Controller.Namespace).Get(t.Context(), WorkloadName(iid), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			switch which {
			case "image":
				dep.Spec.Template.Spec.Containers[0].Image = "registry.example/changed@sha256:" + strings.Repeat("c", 64)
			case "service-account":
				dep.Spec.Template.Spec.ServiceAccountName = "changed"
			case "revision":
				dep.Spec.Template.Annotations[owned.LabelRevision] = "changed"
			}
			if which != "replacement-during-read" {
				if _, err = k.AppsV1().Deployments(dep.Namespace).Update(t.Context(), dep, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			} else {
				reads := 0
				k.PrependReactor("get", "deployments", func(_ ktesting.Action) (bool, runtime.Object, error) {
					reads++
					changed := dep.DeepCopy()
					if reads > 1 {
						changed.Generation++
						changed.Spec.Template.Spec.Containers[0].Image = "changed"
					}
					return true, changed, nil
				})
			}
			if _, err = ReadFleetControllerMetrics(t.Context(), client, target, now); !errors.Is(err, ErrFleetMetricIdentity) {
				t.Fatalf("changed controller accepted: %v", err)
			}
		})
	}
}

func TestFleetRuntimeMetricsEmptyController(t *testing.T) {
	c, target, now := fleetMetricFixture(t)
	k := c.Kube.(*fake.Clientset)
	iid, err := id.ParseWithPrefix(target.Controller.InstanceID, id.PrefixInstance)
	if err != nil {
		t.Fatal(err)
	}
	dep, err := k.AppsV1().Deployments(target.Controller.Namespace).Get(t.Context(), WorkloadName(iid), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	zero := int32(0)
	dep.Spec.Replicas = &zero
	dep.Status.Replicas = 0
	dep.Status.ReadyReplicas = 0
	if _, err = k.AppsV1().Deployments(dep.Namespace).Update(t.Context(), dep, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = k.CoreV1().Pods(dep.Namespace).Delete(t.Context(), "api-pod", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFleetControllerMetrics(t.Context(), c, target, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUUsageMillicores == nil || *got.CPUUsageMillicores != 0 || got.CPURequestMillicores == nil || *got.CPURequestMillicores != 0 || got.IdentityBasis != "empty_controller" || got.Partial {
		t.Fatalf("empty controller %+v", got)
	}
}
