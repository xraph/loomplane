package kubernetes

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	cpprovider "github.com/xraph/ctrlplane/provider"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/pkg/provider"
)

func reviewedPodGroup(labels map[string]string) (*provider.PodGroup, error) {
	raw := labels[owned.LabelPodGroup]
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 128<<10 || labels[owned.LabelWorkloadCapability] != provider.PodGroupCapabilityRevision {
		return nil, provider.ErrWorkloadSpec
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var group *provider.PodGroup
	if err := dec.Decode(&group); err != nil || group == nil {
		return nil, provider.ErrWorkloadSpec
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, provider.ErrWorkloadSpec
	}
	if err := provider.ValidatePodGroup(group); err != nil {
		return nil, err
	}
	if err := provider.CheckPodGroupArchitecture(group, labels[owned.LabelArchitecture]); err != nil {
		return nil, err
	}
	return group, nil
}

func addPodGroup(template *corev1.PodTemplateSpec, labels map[string]string) error {
	group, err := reviewedPodGroup(labels)
	if err != nil || group == nil {
		return err
	}
	if len(template.Spec.Containers) != 1 || template.Spec.ServiceAccountName != "" {
		return provider.ErrWorkloadSpec
	}
	main := &template.Spec.Containers[0]
	_, _, scratch, err := provider.PodGroupResources(main.Resources.Requests.Cpu().MilliValue(), main.Resources.Requests.Memory().Value()>>20, group)
	if err != nil {
		return err
	}
	for _, mount := range group.Mounts {
		for _, existing := range main.VolumeMounts {
			if mount.Path == existing.MountPath || strings.HasPrefix(mount.Path, existing.MountPath+"/") || strings.HasPrefix(existing.MountPath, mount.Path+"/") {
				return provider.ErrWorkloadSpec
			}
		}
		main.VolumeMounts = append(main.VolumeMounts, corev1.VolumeMount{Name: mount.Volume, MountPath: mount.Path, ReadOnly: mount.ReadOnly})
	}
	for _, volume := range group.Scratch {
		template.Spec.Volumes = append(template.Spec.Volumes, corev1.Volume{Name: volume.Name, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: resource.NewQuantity(volume.SizeMiB<<20, resource.BinarySI)}}})
	}
	if scratch > 0 {
		amount := *resource.NewQuantity(scratch<<20, resource.BinarySI)
		main.Resources.Requests[corev1.ResourceEphemeralStorage] = amount
		main.Resources.Limits[corev1.ResourceEphemeralStorage] = amount.DeepCopy()
	}
	selectedEnv := map[string]corev1.EnvVar{}
	for _, value := range main.Env {
		selectedEnv[value.Name] = value
	}
	for _, sidecar := range group.Sidecars {
		if sidecar.Name == main.Name {
			return provider.ErrWorkloadSpec
		}
		limits := corev1.ResourceList{corev1.ResourceCPU: *resource.NewMilliQuantity(sidecar.CPUMillis, resource.DecimalSI), corev1.ResourceMemory: *resource.NewQuantity(sidecar.MemoryMiB<<20, resource.BinarySI)}
		container := corev1.Container{Name: sidecar.Name, Image: sidecar.Image, ImagePullPolicy: corev1.PullIfNotPresent, Command: sidecar.Command, Args: sidecar.Args, Resources: corev1.ResourceRequirements{Requests: limits.DeepCopy(), Limits: limits}, ReadinessProbe: &corev1.Probe{PeriodSeconds: 2, TimeoutSeconds: 2, FailureThreshold: 10}}
		if sidecar.Readiness.Path != "" {
			container.ReadinessProbe.HTTPGet = &corev1.HTTPGetAction{Path: sidecar.Readiness.Path, Port: intstr.FromInt(sidecar.Readiness.Port)}
		} else {
			container.ReadinessProbe.TCPSocket = &corev1.TCPSocketAction{Port: intstr.FromInt(sidecar.Readiness.Port)}
		}
		for _, key := range sidecar.Environment {
			value, ok := selectedEnv[key]
			if !ok {
				return provider.ErrWorkloadSpec
			}
			container.Env = append(container.Env, *value.DeepCopy())
		}
		for _, mount := range sidecar.Mounts {
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: mount.Volume, MountPath: mount.Path, ReadOnly: mount.ReadOnly})
		}
		template.Spec.Containers = append(template.Spec.Containers, container)
	}
	automount := false
	template.Spec.AutomountServiceAccountToken = &automount
	return nil
}

func podGroupStrategy(deployment *appsv1.Deployment, labels map[string]string) error {
	group, err := reviewedPodGroup(labels)
	if err != nil || group == nil {
		return err
	}
	surge, unavailable := intstr.FromInt(group.SurgeReplicas), intstr.FromInt(0)
	if group.SurgeReplicas == 0 {
		unavailable = intstr.FromInt(1)
	}
	deployment.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType, RollingUpdate: &appsv1.RollingUpdateDeployment{MaxSurge: &surge, MaxUnavailable: &unavailable}}
	return nil
}

func podGroupControllerMatches(template corev1.PodTemplateSpec, mainName, mainImage string, labels map[string]string) bool {
	group, err := reviewedPodGroup(labels)
	if err != nil {
		return false
	}
	expected := 1
	if group != nil {
		expected += len(group.Sidecars)
	}
	if len(template.Spec.Containers) != expected || template.Spec.Containers[0].Name != mainName || template.Spec.Containers[0].Image != mainImage {
		return false
	}
	if group != nil {
		for i, sidecar := range group.Sidecars {
			container := template.Spec.Containers[i+1]
			if container.Name != sidecar.Name || container.Image != sidecar.Image || container.ReadinessProbe == nil || container.Resources.Requests.Cpu().MilliValue() != sidecar.CPUMillis || container.Resources.Requests.Memory().Value() != sidecar.MemoryMiB<<20 {
				return false
			}
		}
	}
	return true
}
func podGroupDigest(labels map[string]string) string {
	group, err := reviewedPodGroup(labels)
	if err != nil || group == nil {
		return ""
	}
	raw, _ := json.Marshal(group) //nolint:errcheck // the contract contains only JSON values
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}

func preflightPodGroup(spec cpprovider.ServiceSpec, labels map[string]string, values owned.RuntimeValues) error {
	if labels[owned.LabelPodGroup] == "" {
		return nil
	}
	if len(spec.Volumes) > 0 || labels[owned.LabelKubernetesRules] != "" || labels[owned.LabelStateful] == "true" {
		return provider.ErrWorkloadSpec
	}
	_, _, err := runtimePod(spec, labels, values, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "preflight-runtime"}}, nil, "")
	return err
}
