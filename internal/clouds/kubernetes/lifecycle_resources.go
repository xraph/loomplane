package kubernetes

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func lifecycleResources(cpu, memory int64) corev1.ResourceRequirements {
	limits := corev1.ResourceList{corev1.ResourceCPU: *resource.NewMilliQuantity(cpu, resource.DecimalSI), corev1.ResourceMemory: *resource.NewQuantity(memory<<20, resource.BinarySI)}
	return corev1.ResourceRequirements{Requests: limits.DeepCopy(), Limits: limits}
}
