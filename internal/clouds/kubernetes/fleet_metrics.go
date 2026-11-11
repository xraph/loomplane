package kubernetes

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"path"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"k8s.io/client-go/rest"

	"github.com/xraph/ctrlplane/id"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

var ErrFleetMetricIdentity = errors.New("fleet metric identity does not match")
var ErrFleetMetricUnavailable = errors.New("fleet runtime metric is unavailable")
var fleetPodMetricsResource = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}

// FleetMetricTarget contains only persisted provider and runtime identity pins.
type FleetMetricTarget struct {
	RuntimeLabels                     map[string]string
	Controller                        ControllerReceipt
	ServiceAccount, ServiceAccountUID string
}

// FleetControllerMetrics separates live replica status from optional pod usage.
// These observations grant no scaling or workload mutation authority.
type FleetControllerMetrics struct {
	Component                                             string
	ReplicasAt, MetricsAt                                 time.Time
	DesiredReplicas, CurrentReplicas, ReadyReplicas       *int64
	CPUUsageMillicores, CPURequestMillicores, MemoryBytes *float64
	IdentityBasis                                         string
	Partial                                               bool
	Reason                                                string
}

// ReadFleetControllerMetrics reads one recorded controller, never discovers a new
// owner. Native pod metrics are name/window-correlated, not UID-attested evidence.
func ReadFleetControllerMetrics(ctx context.Context, c *Client, target FleetMetricTarget, now time.Time) (FleetControllerMetrics, error) {
	out := FleetControllerMetrics{Component: target.Controller.ServiceID, Partial: true, Reason: "source_unavailable"}
	r := target.Controller
	if c == nil || c.Kube == nil || c.Config == nil || !r.Valid() || now.IsZero() || c.Config.Namespace != r.Namespace || c.Config.Details["cluster_ca"] != r.ClusterCA || target.ServiceAccount == "" || target.ServiceAccountUID == "" {
		return out, ErrFleetMetricIdentity
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	verify := func() error {
		if err := VerifyRuntimeIdentity(ctx, c.Kube, r.Namespace, r.NamespaceUID, target.ServiceAccount, target.ServiceAccountUID); err != nil {
			return ErrFleetMetricIdentity
		}
		return nil
	}
	if err := verify(); err != nil {
		return out, err
	}
	iid, err := id.ParseWithPrefix(r.InstanceID, id.PrefixInstance)
	if err != nil {
		return out, ErrFleetMetricIdentity
	}
	dep, err := c.Kube.AppsV1().Deployments(r.Namespace).Get(ctx, WorkloadName(iid), metav1.GetOptions{})
	if err != nil {
		return out, ErrFleetMetricUnavailable
	}
	scope := map[string]string{owned.LabelOrg: r.OrgID, owned.LabelProject: r.ProjectID, owned.LabelEnvironment: r.EnvironmentID}
	if dep.Namespace != r.Namespace || string(dep.UID) != r.DeploymentUID || dep.DeletionTimestamp != nil || dep.Generation < r.DeploymentGeneration || checkOwner(dep, iid, scope) != nil {
		return out, ErrFleetMetricIdentity
	}
	if !fleetControllerTemplateMatches(dep, r, target.RuntimeLabels) {
		return out, ErrFleetMetricIdentity
	}
	verifyIdentity := verify
	verify = func() error {
		current, getErr := c.Kube.AppsV1().Deployments(r.Namespace).Get(ctx, dep.Name, metav1.GetOptions{})
		if getErr != nil || current.UID != dep.UID || current.Generation != dep.Generation || current.DeletionTimestamp != nil || !reflect.DeepEqual(current.Spec, dep.Spec) || !reflect.DeepEqual(current.Labels, dep.Labels) {
			return ErrFleetMetricIdentity
		}
		return verifyIdentity()
	}

	desired := int64(1)
	if dep.Spec.Replicas != nil {
		desired = int64(*dep.Spec.Replicas)
	}
	if desired < 0 {
		return out, ErrFleetMetricIdentity
	}
	out.DesiredReplicas = &desired
	out.ReplicasAt = now.UTC()
	if dep.Status.ObservedGeneration < dep.Generation || dep.Status.Replicas < 0 || dep.Status.ReadyReplicas < 0 || dep.Status.ReadyReplicas > dep.Status.Replicas {
		return out, verify()
	}
	current, ready := int64(dep.Status.Replicas), int64(dep.Status.ReadyReplicas)
	out.CurrentReplicas = &current
	out.ReadyReplicas = &ready
	selector := labels.SelectorFromSet(objectLabels(iid, scope)).String()
	pods, err := c.Kube.CoreV1().Pods(r.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 65})
	if err != nil || pods.Continue != "" || len(pods.Items) > 64 {
		return out, verify()
	}
	if len(pods.Items) != int(current) {
		return out, verify()
	}
	if current == 0 {
		zero := float64(0)
		out.CPUUsageMillicores = &zero
		out.CPURequestMillicores = &zero
		out.MemoryBytes = &zero
		out.MetricsAt = now.UTC()
		out.IdentityBasis = "empty_controller"
		out.Partial = false
		out.Reason = ""
		return out, verify()
	}
	if c.Dynamic == nil {
		return out, verify()
	}
	sets := make(map[string]*appsv1.ReplicaSet)
	var cpu, memory, requests float64
	oldest := now
	for n := range pods.Items {
		pod := &pods.Items[n]
		if pod.Namespace != r.Namespace || pod.UID == "" || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || checkOwner(pod, iid, scope) != nil {
			return out, verify()
		}
		parent := metav1.GetControllerOf(pod)
		if parent == nil || parent.Kind != "ReplicaSet" || parent.APIVersion != "apps/v1" || parent.UID == "" {
			return out, verify()
		}
		set := sets[parent.Name]
		if set == nil {
			set, err = c.Kube.AppsV1().ReplicaSets(r.Namespace).Get(ctx, parent.Name, metav1.GetOptions{})
			if err != nil {
				return out, verify()
			}
			sets[parent.Name] = set
		}
		owner := metav1.GetControllerOf(set)
		if set.Namespace != r.Namespace || set.UID != parent.UID || set.DeletionTimestamp != nil || checkOwner(set, iid, scope) != nil || owner == nil || owner.APIVersion != "apps/v1" || owner.Kind != "Deployment" || owner.UID != dep.UID || owner.Name != dep.Name {
			return out, verify()
		}
		metric, readErr := c.Dynamic.Resource(fleetPodMetricsResource).Namespace(r.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if readErr != nil {
			return out, verify()
		}
		use, mem, req, stamp, ok := fleetPodUsage(pod, metric, now)
		if !ok {
			return out, verify()
		}
		after, getErr := c.Kube.CoreV1().Pods(r.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if getErr != nil || after.Namespace != pod.Namespace || after.UID != pod.UID || after.DeletionTimestamp != nil || !reflect.DeepEqual(after.Spec, pod.Spec) || !reflect.DeepEqual(after.Status.ContainerStatuses, pod.Status.ContainerStatuses) || !reflect.DeepEqual(after.Status.InitContainerStatuses, pod.Status.InitContainerStatuses) {
			return out, verify()
		}
		cpu += use
		memory += mem
		requests += req
		if !fleetMetricNumber(cpu) || !fleetMetricNumber(memory) || !fleetMetricNumber(requests) {
			return out, verify()
		}
		if stamp.Before(oldest) {
			oldest = stamp
		}
	}
	if checkErr := verify(); checkErr != nil {
		return FleetControllerMetrics{}, checkErr
	}
	out.CPUUsageMillicores = &cpu
	out.MemoryBytes = &memory
	out.CPURequestMillicores = &requests
	out.MetricsAt = oldest.UTC()
	out.IdentityBasis = "pod_name_window"
	out.Partial = false
	out.Reason = ""
	return out, nil
}

func fleetPodUsage(pod *corev1.Pod, metric *unstructured.Unstructured, now time.Time) (cpu, memory, requests float64, stamp time.Time, ok bool) {
	if metric == nil || metric.GetName() != pod.Name || metric.GetNamespace() != pod.Namespace || (metric.GetUID() != "" && metric.GetUID() != pod.UID) || len(pod.Spec.EphemeralContainers) > 0 {
		return
	}
	timestamp, found, err := unstructured.NestedString(metric.Object, "timestamp")
	if err != nil || !found {
		return
	}
	stamp, err = time.Parse(time.RFC3339Nano, timestamp)
	if err != nil || stamp.After(now) || now.Sub(stamp) > time.Minute {
		return
	}
	window, found, err := unstructured.NestedString(metric.Object, "window")
	if err != nil || !found {
		return
	}
	duration, err := time.ParseDuration(window)
	if err != nil || duration <= 0 || duration > time.Minute {
		return
	}
	start := stamp.Add(-duration)
	if pod.CreationTimestamp.IsZero() || !start.After(pod.CreationTimestamp.Time) {
		return
	}
	expected := make(map[string]corev1.Container, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	for _, container := range pod.Spec.Containers {
		expected[container.Name] = container
	}
	for _, container := range pod.Spec.InitContainers {
		if container.RestartPolicy != nil && *container.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			expected[container.Name] = container
			continue
		}
		completed := false
		for _, status := range pod.Status.InitContainerStatuses {
			if status.Name == container.Name && status.State.Terminated != nil && status.State.Terminated.ExitCode == 0 {
				completed = true
			}
		}
		if !completed {
			return
		}
	}
	statuses := make(map[string]corev1.ContainerStatus, len(expected))
	for _, status := range pod.Status.ContainerStatuses {
		statuses[status.Name] = status
	}
	for _, status := range pod.Status.InitContainerStatuses {
		if _, exists := expected[status.Name]; exists {
			statuses[status.Name] = status
		}
	}
	for name, container := range expected {
		status, exists := statuses[name]
		if !exists || status.ContainerID == "" || status.State.Running == nil || status.State.Running.StartedAt.IsZero() || !start.After(status.State.Running.StartedAt.Time) {
			return
		}
		requested := container.Resources.Requests.Cpu().AsApproximateFloat64() * 1000
		if !fleetMetricNumber(requested) {
			return
		}
		requests += requested
	}
	containers, found, err := unstructured.NestedSlice(metric.Object, "containers")
	if err != nil || !found || len(containers) != len(expected) || len(containers) == 0 {
		return
	}
	seen := make(map[string]bool, len(containers))
	for _, item := range containers {
		row, isMap := item.(map[string]any)
		if !isMap {
			return
		}
		name, found, nameErr := unstructured.NestedString(row, "name")
		if nameErr != nil || !found || seen[name] {
			return
		}
		if _, exists := expected[name]; !exists {
			return
		}
		seen[name] = true
		rawCPU, found, cpuErr := unstructured.NestedString(row, "usage", "cpu")
		if cpuErr != nil || !found {
			return
		}
		rawMemory, found, memoryErr := unstructured.NestedString(row, "usage", "memory")
		if memoryErr != nil || !found {
			return
		}
		if len(rawCPU) > 64 || len(rawMemory) > 64 {
			return
		}
		usage, parseErr := resource.ParseQuantity(rawCPU)
		if parseErr != nil {
			return
		}
		mem, parseErr := resource.ParseQuantity(rawMemory)
		if parseErr != nil {
			return
		}
		use := usage.AsApproximateFloat64() * 1000
		memoryBytes := mem.AsApproximateFloat64()
		if !fleetMetricNumber(use) || !fleetMetricNumber(memoryBytes) {
			return
		}
		cpu += use
		memory += memoryBytes
	}
	ok = fleetMetricNumber(cpu) && fleetMetricNumber(memory) && fleetMetricNumber(requests)
	return
}
func fleetMetricNumber(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0) && value <= float64(math.MaxInt64)
}

// NewFleetObservationClient preserves the installed TLS/auth transport while
// limiting every request to bounded GETs inside the pinned namespace.
func NewFleetObservationClient(cfg *Config, serviceAccount string) (*Client, error) {
	if cfg == nil || cfg.REST == nil || cfg.Namespace == "" || serviceAccount == "" {
		return nil, ErrFleetMetricIdentity
	}
	copyConfig := *cfg
	copyConfig.REST = rest.CopyConfig(cfg.REST)
	previous := copyConfig.REST.WrapTransport
	copyConfig.REST.WrapTransport = func(next http.RoundTripper) http.RoundTripper {
		if previous != nil {
			next = previous(next)
		}
		return newFleetObservationTransport(next, cfg.Namespace, serviceAccount)
	}
	copyConfig.ManagedWorkload = true
	copyConfig.ManagedProfile = ManagedProfileJournal
	return New(&copyConfig)
}

type fleetObservationTransport struct {
	next                      http.RoundTripper
	namespace, serviceAccount string
	remaining                 atomic.Int64
}

func newFleetObservationTransport(next http.RoundTripper, namespace, serviceAccount string) *fleetObservationTransport {
	t := &fleetObservationTransport{next: next, namespace: namespace, serviceAccount: serviceAccount}
	t.remaining.Store(8 << 20)
	return t
}
func (t *fleetObservationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || req.URL == nil || path.Clean(req.URL.Path) != req.URL.Path || strings.Contains(req.URL.Path, "\\") || !t.allowedPath(req.URL.Path) {
		return nil, ErrFleetMetricIdentity
	}
	for key, values := range req.URL.Query() {
		if key == "timeout" {
			if len(values) != 1 {
				return nil, ErrFleetMetricIdentity
			}
			d, err := time.ParseDuration(values[0])
			if err != nil || d <= 0 || d > 20*time.Second {
				return nil, ErrFleetMetricIdentity
			}
			continue
		}
		if key != "labelSelector" && key != "limit" {
			return nil, ErrFleetMetricIdentity
		}
	}
	budget := min(t.remaining.Load(), int64(2<<20))
	if budget <= 0 {
		return nil, ErrFleetMetricUnavailable
	}
	response, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, ErrFleetMetricUnavailable
	}
	if response == nil || response.Body == nil {
		return nil, ErrFleetMetricUnavailable
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, budget+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || int64(len(body)) > budget || t.remaining.Add(-int64(len(body))) < 0 {
		return nil, ErrFleetMetricUnavailable
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	return response, nil
}
func (t *fleetObservationTransport) allowedPath(requestPath string) bool {
	ns := "/api/v1/namespaces/" + t.namespace
	if requestPath == ns || requestPath == ns+"/serviceaccounts/"+t.serviceAccount {
		return true
	}
	for _, prefix := range []string{ns + "/pods", "/apis/apps/v1/namespaces/" + t.namespace + "/deployments", "/apis/apps/v1/namespaces/" + t.namespace + "/replicasets", "/apis/metrics.k8s.io/v1beta1/namespaces/" + t.namespace + "/pods"} {
		if requestPath == prefix {
			return true
		}
		if strings.HasPrefix(requestPath, prefix+"/") {
			name := strings.TrimPrefix(requestPath, prefix+"/")
			return name != "" && !strings.Contains(name, "/")
		}
	}
	return false
}

func fleetControllerTemplateMatches(dep *appsv1.Deployment, r ControllerReceipt, runtimeLabels map[string]string) bool {
	account := dep.Spec.Template.Spec.ServiceAccountName
	if account == "" {
		account = "default"
	}
	return account == r.ServiceAccount && dep.Spec.Template.Annotations[owned.LabelRevision] == r.RuntimeRevision && dep.Spec.Template.Annotations[WorkloadReleaseAnnotation] == r.ReleaseID && podGroupDigest(runtimeLabels) == r.PodGroupDigest && podGroupControllerMatches(dep.Spec.Template, r.Container, r.Image, runtimeLabels)
}
