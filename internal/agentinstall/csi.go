package agentinstall

import (
	"errors"
	"path"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"

	domain "github.com/xraph/loomplane/pkg/federation"
)

const CSIDriverName = "csi.spiffe.io"
const csiInstallationName = "loomplane-spiffe-csi"

// CSIConfig describes a cluster-admin installation, independent of tenant
// workload permissions. The namespace and SPIRE agent must already exist.
type CSIConfig struct {
	Namespace            string
	DriverImage          string
	RegistrarImage       string
	AgentSocketDirectory string
	KubeletRootDirectory string
}

func DefaultCSIConfig(namespace string) CSIConfig {
	return CSIConfig{Namespace: namespace,
		DriverImage:          "ghcr.io/spiffe/spiffe-csi-driver:0.2.13@sha256:6f1ba5d635ba9bf55ae69ba11d71bf169c8fa3e0139a5063738cbc227c8b5546",
		RegistrarImage:       "registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.15.0@sha256:11f199f6bec47403b03cb49c79a41f445884b213b382582a60710b8c6fdc316a",
		AgentSocketDirectory: "/run/spire/agent-sockets", KubeletRootDirectory: "/var/lib/kubelet"}
}
func safeCSIDirectory(value string) bool {
	return len(value) <= 256 && strings.HasPrefix(value, "/") && value != "/" && path.Clean(value) == value && !strings.ContainsAny(value, "\x00\r\n\t\\")
}

// CSIManifest renders resources for review and explicit cluster-admin creation.
// It neither reads credentials nor grants a workload a SPIFFE identity.
func CSIManifest(cfg CSIConfig) (*corev1.List, error) {
	if cfg.Namespace == "" || len(validation.IsDNS1123Label(cfg.Namespace)) != 0 || !domain.ValidPinnedImage(cfg.DriverImage) || !domain.ValidPinnedImage(cfg.RegistrarImage) || !safeCSIDirectory(cfg.AgentSocketDirectory) || !safeCSIDirectory(cfg.KubeletRootDirectory) || cfg.AgentSocketDirectory == cfg.KubeletRootDirectory || strings.HasPrefix(cfg.AgentSocketDirectory, cfg.KubeletRootDirectory+"/") || strings.HasPrefix(cfg.KubeletRootDirectory, cfg.AgentSocketDirectory+"/") {
		return nil, errors.New("invalid SPIFFE CSI installation configuration")
	}
	metadata := func(name, namespace string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "loomplane", "app.kubernetes.io/name": csiInstallationName}}
	}
	account := &corev1.ServiceAccount{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"}, ObjectMeta: metadata(csiInstallationName, cfg.Namespace), AutomountServiceAccountToken: ptr.To(false)}
	driver := &storagev1.CSIDriver{TypeMeta: metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "CSIDriver"}, ObjectMeta: metadata(CSIDriverName, ""), Spec: storagev1.CSIDriverSpec{AttachRequired: ptr.To(false), PodInfoOnMount: ptr.To(true), FSGroupPolicy: ptr.To(storagev1.NoneFSGroupPolicy), VolumeLifecycleModes: []storagev1.VolumeLifecycleMode{storagev1.VolumeLifecycleEphemeral}}}
	socketPath := "/spiffe-csi/csi.sock"
	pluginPath := path.Join(cfg.KubeletRootDirectory, "plugins", CSIDriverName)
	podsPath := path.Join(cfg.KubeletRootDirectory, "pods")
	host := func(name, p string) corev1.Volume {
		return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: p, Type: ptr.To(corev1.HostPathDirectoryOrCreate)}}}
	}
	labels := map[string]string{"app.kubernetes.io/name": csiInstallationName}
	daemon := &appsv1.DaemonSet{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "DaemonSet"}, ObjectMeta: metadata(csiInstallationName, cfg.Namespace), Spec: appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{ServiceAccountName: account.Name, AutomountServiceAccountToken: ptr.To(false), NodeSelector: map[string]string{"kubernetes.io/os": "linux"}, Containers: []corev1.Container{
		{Name: "driver", Image: cfg.DriverImage, Args: []string{"-workload-api-socket-dir", "/spire-agent-socket", "-csi-socket-path", socketPath}, Env: []corev1.EnvVar{{Name: "MY_NODE_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}}}, SecurityContext: &corev1.SecurityContext{Privileged: ptr.To(true), ReadOnlyRootFilesystem: ptr.To(true)}, VolumeMounts: []corev1.VolumeMount{{Name: "socket", MountPath: "/spire-agent-socket", ReadOnly: true}, {Name: "csi", MountPath: "/spiffe-csi"}, {Name: "pods", MountPath: podsPath, MountPropagation: ptr.To(corev1.MountPropagationBidirectional)}}},
		{Name: "registrar", Image: cfg.RegistrarImage, Args: []string{"-csi-address", socketPath, "-kubelet-registration-path", path.Join(pluginPath, "csi.sock")}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, VolumeMounts: []corev1.VolumeMount{{Name: "csi", MountPath: "/spiffe-csi"}, {Name: "registration", MountPath: "/registration"}}},
	}, Volumes: []corev1.Volume{host("socket", cfg.AgentSocketDirectory), host("csi", pluginPath), host("pods", podsPath), host("registration", path.Join(cfg.KubeletRootDirectory, "plugins_registry"))}}}}}
	return &corev1.List{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "List"}, Items: []runtime.RawExtension{{Object: account}, {Object: driver}, {Object: daemon}}}, nil
}
