package kubernetes

import corev1 "k8s.io/api/core/v1"

// restrictInstancePod matches the namespace's restricted admission policy.
// Images must provide a non-root user; an incompatible image fails admission.
func restrictInstancePod(pod *corev1.PodSpec) {
	nonRoot, escalation, automount := true, false, false
	group := int64(65532)
	pod.AutomountServiceAccountToken = &automount
	pod.SecurityContext = &corev1.PodSecurityContext{RunAsNonRoot: &nonRoot, FSGroup: &group, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
	for i := range pod.Containers {
		pod.Containers[i].SecurityContext = &corev1.SecurityContext{AllowPrivilegeEscalation: &escalation, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
	}
}
