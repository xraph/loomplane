package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	cpprovider "github.com/xraph/ctrlplane/provider"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/pkg/provider"
)

// runtimePod keeps workload environment, ports, resource limits and readiness
// identical across Deployment and StatefulSet runtimes. A workload with its
// own ServiceAccount runs as it and gets its token; others keep the
// namespace default, and a catalog component doesn't mount its token.
func runtimePod(spec cpprovider.ServiceSpec, labels map[string]string, values owned.RuntimeValues, runtimeSecret *corev1.Secret, pulls []corev1.LocalObjectReference, serviceAccount string) (*corev1.PodTemplateSpec, []corev1.ServicePort, error) {
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
	switch protocol, health := labels[owned.LabelHealthProtocol], labels[owned.LabelHealth]; {
	case protocol != "" && protocol != "redis":
		return nil, nil, fmt.Errorf("unsupported readiness protocol %q", protocol)
	case protocol == "redis" && len(ports) > 0:
		// The kubelet has no RESP probe; an open port is the closest it gets.
		port, err := healthPort(spec, labels)
		if err != nil {
			return nil, nil, err
		}
		container.ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(port)}}, PeriodSeconds: 2, TimeoutSeconds: 2, FailureThreshold: 10}
	case health != "" && len(ports) > 0:
		port, err := healthPort(spec, labels)
		if err != nil {
			return nil, nil, err
		}
		container.ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: health, Port: intstr.FromInt(port)}}, PeriodSeconds: 2, TimeoutSeconds: 2, FailureThreshold: 10}
	}
	var volumes []corev1.Volume
	if len(values.Files) > 0 {
		fallback := int32(owned.DefaultFileMode)
		source := &corev1.SecretVolumeSource{SecretName: runtimeSecret.Name, DefaultMode: &fallback}
		for i, p := range slices.Sorted(maps.Keys(values.Files)) {
			mode := int32(values.FileMode(p)) //nolint:gosec // permission bits, at most 0o777
			source.Items = append(source.Items, corev1.KeyToPath{Key: fileKey(i), Path: fileKey(i), Mode: &mode})
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: runtimeFilesVolume, MountPath: p, SubPath: fileKey(i), ReadOnly: true})
		}
		volumes = []corev1.Volume{{Name: runtimeFilesVolume, VolumeSource: corev1.VolumeSource{Secret: source}}}
	}
	placement, err := gpuPlacement(labels)
	if err != nil {
		return nil, nil, err
	}
	shm, err := shmSize(labels)
	if err != nil {
		return nil, nil, err
	}
	if shm != nil {
		volumes = append(volumes, corev1.Volume{Name: shmVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: shm}}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: shmVolume, MountPath: "/dev/shm"})
	}
	if placement != nil {
		if container.Resources.Limits == nil {
			container.Resources = corev1.ResourceRequirements{Requests: corev1.ResourceList{}, Limits: corev1.ResourceList{}}
		}
		count := *resource.NewQuantity(int64(placement.Count), resource.DecimalSI)
		container.Resources.Limits[gpuResource] = count
		container.Resources.Requests[gpuResource] = count.DeepCopy()
	}
	template := &corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{owned.LabelRevision: labels[owned.LabelRevision]}}, Spec: corev1.PodSpec{Containers: []corev1.Container{container}, ImagePullSecrets: pulls, Volumes: volumes}}
	if architecture := labels[owned.LabelArchitecture]; architecture != "" {
		template.Spec.NodeSelector = map[string]string{"kubernetes.io/arch": architecture}
	}
	if placement != nil {
		// The wait reads the request back from the template to word its error.
		template.Annotations[owned.LabelGPU] = labels[owned.LabelGPU]
		if template.Spec.NodeSelector == nil && len(placement.NodeSelector) > 0 {
			template.Spec.NodeSelector = map[string]string{}
		}
		for key, value := range placement.NodeSelector {
			if key == "kubernetes.io/arch" && template.Spec.NodeSelector[key] != "" && template.Spec.NodeSelector[key] != value {
				return nil, nil, fmt.Errorf("the GPU placement selects architecture %q but the workload runs on %q", value, template.Spec.NodeSelector[key])
			}
			template.Spec.NodeSelector[key] = value
		}
		for _, t := range placement.Tolerations {
			template.Spec.Tolerations = append(template.Spec.Tolerations, corev1.Toleration{Key: t.Key, Operator: corev1.TolerationOperator(t.Operator), Value: t.Value, Effect: corev1.TaintEffect(t.Effect)})
		}
	}
	switch {
	case serviceAccount != "":
		automount := true
		template.Spec.ServiceAccountName, template.Spec.AutomountServiceAccountToken = serviceAccount, &automount
	case labels[owned.LabelCatalogComponent] != "":
		// A catalog component or job without Kubernetes rules has no use
		// for the namespace default account's token.
		automount := false
		template.Spec.AutomountServiceAccountToken = &automount
	}
	if raw := labels[owned.LabelFSGroup]; raw != "" {
		group, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || group < 1 {
			return nil, nil, errors.New("invalid volume group")
		}
		// OnRootMismatch changes ownership once, not on every start.
		policy := corev1.FSGroupChangeOnRootMismatch
		template.Spec.SecurityContext = &corev1.PodSecurityContext{FSGroup: &group, FSGroupChangePolicy: &policy}
	}
	return template, servicePorts, nil
}

const shmVolume = "dshm"

// gpuPlacement decodes the reviewed placement a workload's labels carry, or
// nil when it asks for no GPU.
func gpuPlacement(labels map[string]string) (*provider.GPUPlacement, error) {
	raw := labels[owned.LabelGPU]
	if raw == "" {
		return nil, nil
	}
	var placement provider.GPUPlacement
	if err := json.Unmarshal([]byte(raw), &placement); err != nil || placement.Count < 1 {
		return nil, errors.New("invalid GPU placement")
	}
	return &placement, nil
}

// shmSize is the /dev/shm size the workload asked for, or nil.
func shmSize(labels map[string]string) (*resource.Quantity, error) {
	raw := labels[owned.LabelShmMB]
	if raw == "" {
		return nil, nil
	}
	mb, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || mb < 1 {
		return nil, errors.New("invalid shared memory size")
	}
	return resource.NewQuantity(mb<<20, resource.BinarySI), nil
}

// unschedulableGPU is the error for a pod of the workload that no node has
// room for, or nil. The template's annotation says what was asked for.
func unschedulableGPU(ctx context.Context, c *Client, template corev1.PodTemplateSpec, selector *metav1.LabelSelector) error {
	placement, err := gpuPlacement(map[string]string{owned.LabelGPU: template.Annotations[owned.LabelGPU]})
	if err != nil || placement == nil || selector == nil {
		return nil
	}
	pods, err := c.Kube.CoreV1().Pods(c.Config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: metav1.FormatLabelSelector(selector)})
	if err != nil {
		return nil //nolint:nilerr // the wait carries on and times out with its own error
	}
	title := placement.Kind
	if kind, ok := provider.LookupAcceleratorKind(placement.Kind); ok {
		title = kind.Title
	}
	for _, pod := range pods.Items {
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse && condition.Reason == corev1.PodReasonUnschedulable && strings.Contains(condition.Message, gpuResource) {
				return fmt.Errorf("no node has %d free %s GPUs: %s", placement.Count, title, condition.Message)
			}
		}
	}
	return nil
}

// runtimeFilesVolume is the volume a workload's rendered files are
// mounted from, one subPath each.
const runtimeFilesVolume = "runtime-files"

// fileKey is the runtime Secret key of the i-th rendered file in path
// order.
func fileKey(i int) string { return "file-" + strconv.Itoa(i) }

// runtimeSecretData is what a workload's runtime Secret holds: its secret
// variables by name, and its rendered files as file-0, file-1 and on in
// path order. A catalog component's variable names have no "-"; one
// that clashes anyway is refused before anything is written.
func runtimeSecretData(values owned.RuntimeValues) (map[string][]byte, error) {
	data := map[string][]byte{}
	for key, value := range values.Env {
		data[key] = []byte(value)
	}
	for i, p := range slices.Sorted(maps.Keys(values.Files)) {
		if _, clash := data[fileKey(i)]; clash {
			return nil, fmt.Errorf("the variable %s clashes with a rendered file's key", fileKey(i))
		}
		data[fileKey(i)] = values.Files[p]
	}
	return data, nil
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
