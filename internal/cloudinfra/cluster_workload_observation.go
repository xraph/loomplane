package cloudinfra

import (
	"reflect"
	"regexp"

	apiequality "k8s.io/apimachinery/pkg/api/equality"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
)

// ClusterObservedWorkload retains request and owner identities, never a template.
// Unmatched pods still consume requests. Only a complete owner chain earns credit.
type ClusterObservedWorkload struct {
	Running                bool `json:"running,omitempty"`
	Pending                bool `json:"pending,omitempty"`
	schedulerArchitecture  string
	SchedulerShortage      bool                  `json:"scheduler_shortage,omitempty"`
	PodUID                 string                `json:"pod_uid"`
	NamespaceUID           string                `json:"namespace_uid,omitempty"`
	DeploymentUID          string                `json:"deployment_uid,omitempty"`
	ReplicaProofGeneration int64                 `json:"replica_proof_generation,omitempty"`
	DeploymentGeneration   int64                 `json:"deployment_generation,omitempty"`
	ReplicaSetUID          string                `json:"replica_set_uid,omitempty"`
	ReleaseID              string                `json:"release_id,omitempty"`
	Requests               ClusterWorkloadVector `json:"requests"`
}

func clusterWorkloadObservations(in kubernetes.CapacityInventory) ([]ClusterObservedWorkload, bool, error) {
	out := []ClusterObservedWorkload{}
	complete := true
	seen := map[string]bool{}
	for _, pod := range in.Pods {
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		if pod.UID == "" || seen[string(pod.UID)] {
			complete = false
			continue
		}
		seen[string(pod.UID)] = true
		requests := corev1.ResourceList{}
		if pod.Spec.Resources != nil {
			complete = false
		}
		if !podAllocationsCurrent(&pod) {
			complete = false
		}
		for _, c := range pod.Spec.Containers {
			addClusterRequests(requests, c.Resources.Requests)
		}
		for _, c := range pod.Spec.InitContainers {
			if c.RestartPolicy != nil {
				complete = false
			}
			for name, q := range c.Resources.Requests {
				if current := requests[name]; q.Cmp(current) > 0 {
					requests[name] = q.DeepCopy()
				}
			}
		}
		addClusterRequests(requests, pod.Spec.Overhead)
		metrics, err := clusterResourceMetrics(requests)
		if err != nil {
			return nil, false, err
		}
		v := ClusterWorkloadVector{Pods: 1}
		if metrics.CPUMillis != nil {
			v.CPUMillis = *metrics.CPUMillis
		}
		if metrics.MemoryBytes != nil {
			v.MemoryBytes = *metrics.MemoryBytes
		}
		if metrics.EphemeralStorageBytes != nil {
			v.EphemeralStorageBytes = *metrics.EphemeralStorageBytes
		}
		// Extended resources need a reviewed scheduler contract before admission.
		if len(metrics.Extended) > 0 {
			complete = false
		}
		w := ClusterObservedWorkload{PodUID: string(pod.UID), Requests: v, Running: pod.Status.Phase == corev1.PodRunning && pod.Spec.NodeName != "", Pending: pod.Status.Phase == corev1.PodPending}
		if w.Pending && pod.DeletionTimestamp == nil && pod.Spec.NodeName == "" && pod.Status.NominatedNodeName == "" && supportedShortagePod(pod.Spec) {
			for _, c := range pod.Status.Conditions {
				if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable && schedulerInsufficient.MatchString(c.Message) {
					w.SchedulerShortage = true
					w.schedulerArchitecture = pod.Spec.NodeSelector[corev1.LabelArchStable]
				}
			}
		}
		for _, ns := range in.Namespaces {
			if ns.Name == pod.Namespace && ns.DeletionTimestamp == nil {
				w.NamespaceUID = string(ns.UID)
			}
		}
		rsOwner := workloadController(pod.OwnerReferences, "ReplicaSet")
		for _, rs := range in.ReplicaSets {
			if rs.UID == "" || string(rs.UID) != rsOwner || rs.Namespace != pod.Namespace || rs.DeletionTimestamp != nil {
				continue
			}
			depOwner := workloadController(rs.OwnerReferences, "Deployment")
			for _, dep := range in.Deployments {
				release := dep.Spec.Template.Annotations[kubernetes.WorkloadReleaseAnnotation]
				if dep.UID == "" || string(dep.UID) != depOwner || dep.Namespace != pod.Namespace || dep.DeletionTimestamp != nil || release == "" || release != rs.Spec.Template.Annotations[kubernetes.WorkloadReleaseAnnotation] || release != pod.Annotations[kubernetes.WorkloadReleaseAnnotation] {
					continue
				}
				template := rs.Spec.Template.DeepCopy()
				delete(template.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
				if len(template.Labels) == 0 {
					template.Labels = nil
				}
				currentTemplate := dep.Spec.Template.DeepCopy()
				if len(currentTemplate.Labels) == 0 {
					currentTemplate.Labels = nil
				}
				if !reflect.DeepEqual(*template, *currentTemplate) || !podHasReviewedRequests(pod.Spec, currentTemplate.Spec) {
					continue
				}
				w.DeploymentUID, w.DeploymentGeneration, w.ReplicaSetUID, w.ReleaseID = string(dep.UID), dep.Generation, string(rs.UID), release
			}
		}
		out = append(out, w)
	}
	return out, complete, nil
}

func workloadController(refs []metav1.OwnerReference, kind string) string {
	var found string
	for _, ref := range refs {
		if ref.Controller == nil || !*ref.Controller {
			continue
		}
		if found != "" || ref.Kind != kind || ref.APIVersion != "apps/v1" || ref.UID == "" {
			return ""
		}
		found = string(ref.UID)
	}
	return found
}

var schedulerInsufficient = regexp.MustCompile(`^0/[1-9][0-9]* nodes are available: [1-9][0-9]* Insufficient (cpu|memory)(, [1-9][0-9]* Insufficient (cpu|memory))*\.`)

func podHasReviewedRequests(pod, template corev1.PodSpec) bool {
	if len(pod.Containers) != len(template.Containers) || len(pod.InitContainers) != len(template.InitContainers) || !apiequality.Semantic.DeepEqual(pod.NodeSelector, template.NodeSelector) || !apiequality.Semantic.DeepEqual(pod.Affinity, template.Affinity) || !apiequality.Semantic.DeepEqual(pod.TopologySpreadConstraints, template.TopologySpreadConstraints) || !apiequality.Semantic.DeepEqual(pod.RuntimeClassName, template.RuntimeClassName) || !apiequality.Semantic.DeepEqual(pod.Overhead, template.Overhead) || len(pod.SchedulingGates) != 0 || len(pod.ResourceClaims) != 0 || pod.SchedulerName != template.SchedulerName || pod.PriorityClassName != template.PriorityClassName {
		return false
	}
	same := func(a, b []corev1.Container) bool {
		for i, c := range a {
			if c.Name != b[i].Name || c.Image != b[i].Image || !apiequality.Semantic.DeepEqual(c.Resources, b[i].Resources) {
				return false
			}
		}
		return true
	}
	return same(pod.Containers, template.Containers) && same(pod.InitContainers, template.InitContainers)
}

// The stock scheduler's insufficient-resource reason qualifies only for the
// placement choices the workload planner reviewed. Admission webhooks can add
// constraints to both a template and the resulting pods.
func supportedShortagePod(p corev1.PodSpec) bool {
	if p.SchedulerName != "" && p.SchedulerName != corev1.DefaultSchedulerName || p.Affinity != nil || len(p.TopologySpreadConstraints) > 0 || p.PriorityClassName != "" || p.Priority != nil && *p.Priority != 0 || p.RuntimeClassName != nil || len(p.SchedulingGates) > 0 || len(p.ResourceClaims) > 0 || p.Resources != nil || len(p.Overhead) > 0 || p.HostNetwork {
		return false
	}
	for k := range p.NodeSelector {
		if k != corev1.LabelArchStable {
			return false
		}
	}
	for _, tol := range p.Tolerations {
		if tol.Effect != corev1.TaintEffectNoExecute || tol.Operator != corev1.TolerationOpExists || tol.Key != corev1.TaintNodeNotReady && tol.Key != corev1.TaintNodeUnreachable {
			return false
		}
	}
	for _, v := range p.Volumes {
		if v.PersistentVolumeClaim != nil || v.Ephemeral != nil || v.HostPath != nil || v.CSI != nil {
			return false
		}
	}
	for _, c := range p.Containers {
		for _, port := range c.Ports {
			if port.HostPort != 0 {
				return false
			}
		}
	}
	return len(p.InitContainers) == 0
}
