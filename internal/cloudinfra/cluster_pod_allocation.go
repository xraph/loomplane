package cloudinfra

import corev1 "k8s.io/api/core/v1"

func podAllocationsCurrent(p *corev1.Pod) bool {
	for _, cond := range p.Status.Conditions {
		if (cond.Type == corev1.PodResizeInProgress || cond.Type == corev1.PodResizePending) && cond.Status != corev1.ConditionFalse {
			return false
		}
	}
	if p.Status.Resize != "" || p.Spec.Resources != nil {
		return false
	}
	if p.Status.AllocatedResources != nil || p.Status.Resources != nil {
		// Some kubelets also report the effective allocation at pod level for
		// a single ordinary container with no pod-level request.
		if len(p.Spec.Containers) != 1 || len(p.Spec.InitContainers) != 0 || len(p.Spec.Overhead) != 0 || len(p.Status.ContainerStatuses) != 1 || p.Status.Resources == nil {
			return false
		}
		req := p.Spec.Containers[0].Resources.Requests
		if !podRuntimeWithinRequests(req, p.Status.Resources.Requests) || !sameCPUAndMemory(req, p.Status.AllocatedResources) || (len(req) > 0 && (p.Status.ContainerStatuses[0].AllocatedResources == nil || p.Status.ContainerStatuses[0].Resources == nil)) {
			return false
		}
	}
	return containerAllocationsCurrent(p.Spec.Containers, p.Status.ContainerStatuses) && containerAllocationsCurrent(p.Spec.InitContainers, p.Status.InitContainerStatuses)
}

func containerAllocationsCurrent(containers []corev1.Container, statuses []corev1.ContainerStatus) bool {
	seen := map[string]bool{}
	for _, status := range statuses {
		if status.AllocatedResources == nil && status.Resources == nil {
			continue
		}
		if status.Name == "" || seen[status.Name] {
			return false
		}
		seen[status.Name] = true
		var found bool
		for _, c := range containers {
			if c.Name != status.Name {
				continue
			}
			if found {
				return false
			}
			found = true
			if len(c.Resources.Requests) == 0 && len(status.AllocatedResources) == 0 && (status.Resources == nil || len(status.Resources.Requests) == 0) {
				continue
			}
			if status.AllocatedResources == nil || status.Resources == nil || !sameCPUAndMemory(c.Resources.Requests, status.AllocatedResources) || !sameCPUAndMemory(c.Resources.Requests, status.Resources.Requests) {
				return false
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sameCPUAndMemory(a, b corev1.ResourceList) bool {
	if len(a) != len(b) {
		return false
	}
	for name, req := range a {
		allocated, ok := b[name]
		if name != corev1.ResourceCPU && name != corev1.ResourceMemory || !ok || allocated.Cmp(req) != 0 {
			return false
		}
	}
	return true
}

// PodRequests uses container requests when the pod declares no pod-level
// resources. A kubelet may round down the pod cgroup CPU reading, even to zero.
// Charge the full declared allocation; never use this reading for credit.
func podRuntimeWithinRequests(req, current corev1.ResourceList) bool {
	for k, v := range current {
		bound, ok := req[k]
		if !ok || v.Sign() < 0 || (k != corev1.ResourceCPU && k != corev1.ResourceMemory) || v.Cmp(bound) > 0 {
			return false
		}
	}
	if mem, ok := req[corev1.ResourceMemory]; ok {
		v, ok := current[corev1.ResourceMemory]
		return ok && v.Equal(mem)
	}
	return true
}
