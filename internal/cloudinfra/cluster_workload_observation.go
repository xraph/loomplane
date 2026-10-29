package cloudinfra

import (
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
)

// ClusterObservedWorkload retains request and owner identities, never a template.
// Unmatched pods still consume requests. Only a complete owner chain earns credit.
type ClusterObservedWorkload struct {
	PodUID               string                `json:"pod_uid"`
	NamespaceUID         string                `json:"namespace_uid,omitempty"`
	DeploymentUID        string                `json:"deployment_uid,omitempty"`
	DeploymentGeneration int64                 `json:"deployment_generation,omitempty"`
	ReplicaSetUID        string                `json:"replica_set_uid,omitempty"`
	ReleaseID            string                `json:"release_id,omitempty"`
	Requests             ClusterWorkloadVector `json:"requests"`
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
		for _, status := range pod.Status.ContainerStatuses {
			if status.AllocatedResources != nil {
				complete = false
			}
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
		w := ClusterObservedWorkload{PodUID: string(pod.UID), Requests: v}
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
				if !reflect.DeepEqual(*template, *currentTemplate) {
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
