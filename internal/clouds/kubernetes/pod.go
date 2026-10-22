package kubernetes

import (
	"errors"
	"maps"
	"slices"
	"strconv"

	cpprovider "github.com/xraph/ctrlplane/provider"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

// runtimePod keeps workload environment, ports, resource limits and readiness
// identical across Deployment and StatefulSet runtimes.
func runtimePod(spec cpprovider.ServiceSpec, labels map[string]string, values owned.RuntimeValues, runtimeSecret *corev1.Secret, pulls []corev1.LocalObjectReference) (*corev1.PodTemplateSpec, []corev1.ServicePort, error) {
	env := []corev1.EnvVar{}
	for _, key := range slices.Sorted(maps.Keys(spec.Env)) {
		if _, secret := values.Env[key]; !secret {
			env = append(env, corev1.EnvVar{Name: key, Value: spec.Env[key]})
		}
	}
	for _, key := range slices.Sorted(maps.Keys(values.Env)) {
		env = append(env, corev1.EnvVar{Name: key, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: runtimeSecret.Name}, Key: key}}})
	}
	ports := []corev1.ContainerPort{}
	servicePorts := []corev1.ServicePort{}
	for index, port := range spec.Ports {
		if port.Container < 1 || port.Container > 65535 {
			return nil, nil, errors.New("invalid Kubernetes container port")
		}
		portName := "http-" + strconv.Itoa(index)
		ports = append(ports, corev1.ContainerPort{Name: portName, ContainerPort: int32(port.Container)})
		servicePorts = append(servicePorts, corev1.ServicePort{Name: portName, Port: int32(port.Container), TargetPort: intstr.FromInt(port.Container)})
	}
	container := corev1.Container{Name: spec.Name, Image: spec.Image, ImagePullPolicy: corev1.PullIfNotPresent, Env: env, Ports: ports, Command: spec.Command, Args: spec.Args}
	if spec.Resources.CPUMillis > 0 || spec.Resources.MemoryMB > 0 {
		limits := corev1.ResourceList{}
		if spec.Resources.CPUMillis > 0 {
			limits[corev1.ResourceCPU] = *resource.NewMilliQuantity(int64(spec.Resources.CPUMillis), resource.DecimalSI)
		}
		if spec.Resources.MemoryMB > 0 {
			limits[corev1.ResourceMemory] = *resource.NewQuantity(int64(spec.Resources.MemoryMB)*(1<<20), resource.BinarySI)
		}
		container.Resources = corev1.ResourceRequirements{Requests: limits.DeepCopy(), Limits: limits}
	}
	if health := labels[owned.LabelHealth]; health != "" && len(ports) > 0 {
		port, err := healthPort(spec, labels)
		if err != nil {
			return nil, nil, err
		}
		container.ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: health, Port: intstr.FromInt(port)}}, PeriodSeconds: 2, TimeoutSeconds: 2, FailureThreshold: 10}
	}
	template := &corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{owned.LabelRevision: labels[owned.LabelRevision]}}, Spec: corev1.PodSpec{Containers: []corev1.Container{container}, ImagePullSecrets: pulls}}
	if architecture := labels[owned.LabelArchitecture]; architecture != "" {
		template.Spec.NodeSelector = map[string]string{"kubernetes.io/arch": architecture}
	}
	return template, servicePorts, nil
}

// healthPort is the container port readiness probes: the one LabelHealthPort
// names, which the workload must declare, otherwise its first port.
func healthPort(spec cpprovider.ServiceSpec, labels map[string]string) (int, error) {
	declared := labels[owned.LabelHealthPort]
	if declared == "" {
		return spec.Ports[0].Container, nil
	}
	for _, port := range spec.Ports {
		if strconv.Itoa(port.Container) == declared {
			return port.Container, nil
		}
	}
	return 0, errors.New("the readiness port isn't one of the workload's ports")
}
