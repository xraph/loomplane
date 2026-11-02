// Package agentinstall builds and delivers pod-bound agent launch configuration.
// Its inputs come from the trusted installation and enrollment controllers, never
// directly from an agent or a management request body.
package agentinstall

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/agent"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// ContainerName and ConfigKey are fixed across attestation and launch delivery.
const (
	ContainerName = "agent"
	ConfigKey     = "agent.json"
	configPath    = "/etc/loomplane-agent/agent.json"
	statePath     = "/var/lib/loomplane-agent/private/agent.db"
	workloadAPI   = "unix:///run/spire/agent.sock"
)

var ErrLaunch = errors.New("agent launch does not match the enrolled installation")

// KubernetesPlan describes one process attempt. The controller must retain the
// attempt ID and created resource UIDs for recovery and conditional cleanup.
// A new process requires a new plan, pod and enrollment, never a container retry.
type KubernetesPlan struct {
	namespace, name, image string
	publisher              bool
}

func NewKubernetesPlan(namespace, attemptID, image string, publisher bool) (*KubernetesPlan, error) {
	id, err := uuid.Parse(attemptID)
	if err != nil || id.String() != attemptID || id == uuid.Nil || len(validation.IsDNS1123Label(namespace)) != 0 || !domain.ValidAgentImage(image) {
		return nil, ErrLaunch
	}
	return &KubernetesPlan{namespace: namespace, name: "loomplane-agent-" + attemptID, image: image, publisher: publisher}, nil
}

// ServiceAccount returns an attempt-specific account. Consumers need no binding.
func (p *KubernetesPlan) ServiceAccount() *corev1.ServiceAccount {
	return &corev1.ServiceAccount{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"}, ObjectMeta: p.metadata(), AutomountServiceAccountToken: ptr.To(false)}
}

// ObserverRBAC grants only namespace-local EndpointSlice observation. Tokens are
// mounted explicitly into exporting pods; the account never enables auto-mount.
func (p *KubernetesPlan) ObserverRBAC() (*rbacv1.Role, *rbacv1.RoleBinding) {
	if !p.publisher {
		return nil, nil
	}
	role := &rbacv1.Role{TypeMeta: metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"}, ObjectMeta: p.metadata(), Rules: []rbacv1.PolicyRule{{APIGroups: []string{"discovery.k8s.io"}, Resources: []string{"endpointslices"}, Verbs: []string{"list", "watch"}}}}
	binding := &rbacv1.RoleBinding{TypeMeta: metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "RoleBinding"}, ObjectMeta: p.metadata(), RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: p.name}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: p.name, Namespace: p.namespace}}}
	return role, binding
}
func (p *KubernetesPlan) metadata() metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: p.name, Namespace: p.namespace, Labels: map[string]string{"app.kubernetes.io/name": "loomplane-agent", "app.kubernetes.io/managed-by": "loomplane"}}
}

// Pod waits for its absent ConfigMap after scheduling. Delivery then checks the
// assigned UID and node before publishing configuration. No init container or
// shell copies data, and no host directory is mounted into the workload.
func (p *KubernetesPlan) Pod() *corev1.Pod {
	pod := &corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: p.metadata(), Spec: corev1.PodSpec{
		ServiceAccountName: p.name, AutomountServiceAccountToken: ptr.To(false), RestartPolicy: corev1.RestartPolicyNever, EnableServiceLinks: ptr.To(false), DNSPolicy: corev1.DNSClusterFirst,
		SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(10001)), RunAsGroup: ptr.To(int64(10001)), FSGroup: ptr.To(int64(10001)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		Containers: []corev1.Container{{Name: ContainerName, Image: p.image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/usr/local/bin/loomplane-agent"}, Args: []string{"--config", configPath},
			TerminationMessagePath: "/dev/termination-log", TerminationMessagePolicy: corev1.TerminationMessageReadFile,
			SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")}},
			VolumeMounts:    []corev1.VolumeMount{{Name: "launch", MountPath: configPath, SubPath: ConfigKey, ReadOnly: true}, {Name: "state", MountPath: "/var/lib/loomplane-agent"}, {Name: "workload-api", MountPath: "/run/spire", ReadOnly: true}},
		}},
		Volumes: []corev1.Volume{
			{Name: "launch", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: p.name}, Items: []corev1.KeyToPath{{Key: ConfigKey, Path: ConfigKey}}, DefaultMode: ptr.To(int32(0o444)), Optional: ptr.To(false)}}},
			{Name: "state", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr.To(resource.MustParse("64Mi"))}}},
			{Name: "workload-api", VolumeSource: corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{Driver: CSIDriverName, ReadOnly: ptr.To(true)}}},
		},
	}}
	if p.publisher {
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "kubernetes-api", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount", ReadOnly: true})
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: "kubernetes-api", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{DefaultMode: ptr.To(int32(0o444)), Sources: []corev1.VolumeProjection{
			{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token", ExpirationSeconds: ptr.To(int64(3600))}},
			{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"}, Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}}},
			{DownwardAPI: &corev1.DownwardAPIProjection{Items: []corev1.DownwardAPIVolumeFile{{Path: "namespace", FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.namespace"}}}}},
		}}}})
	}
	return pod
}

// LaunchSettings comes from the control plane's public listener and signing-key
// configuration. Private keys and management credentials have no fields here.
type LaunchSettings struct {
	ControlEndpoint string
	TrustGeneration uint64
	SigningKeys     []access.SnapshotKey
	Targets         []runtimecatalog.KubernetesTarget
}

// Deliver creates, never updates, the attempt's immutable public configuration.
// The caller must load a ready enrollment from durable storage. guard must check
// current management authorization, installation generation and enrollment state.
// An error after the Create request has an uncertain outcome: inspect the exact
// attempt against its saved intent. Never retry enrollment to resolve a timeout.
func (p *KubernetesPlan) Deliver(ctx context.Context, client kubernetes.Interface, record workloadidentity.EnrollmentRecord, settings LaunchSettings, guard func(context.Context) error) (*corev1.ConfigMap, error) {
	if p == nil || client == nil || guard == nil {
		return nil, ErrLaunch
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := guard(ctx); err != nil {
		return nil, err
	}
	cm, err := p.DesiredConfigMap(record, settings)
	if err != nil {
		return nil, err
	}
	ns, err := client.CoreV1().Namespaces().Get(ctx, p.namespace, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if ns.DeletionTimestamp != nil || string(ns.UID) != record.Binding.NamespaceUID {
		return nil, ErrLaunch
	}
	pod, err := client.CoreV1().Pods(p.namespace).Get(ctx, p.name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if !p.matches(pod, record.Binding.PodUID, record.Binding.NamespaceUID) {
		return nil, ErrLaunch
	}
	// Remote reads may outlive authorization or the five-minute bootstrap window.
	if err := guard(ctx); err != nil {
		return nil, err
	}
	if _, err := p.launch(record, settings, time.Now()); err != nil {
		return nil, ErrLaunch
	}
	return client.CoreV1().ConfigMaps(p.namespace).Create(ctx, cm, metav1.CreateOptions{})
}

func (p *KubernetesPlan) launch(r workloadidentity.EnrollmentRecord, s LaunchSettings, now time.Time) (agent.LaunchConfig, error) {
	b := r.Binding
	d := b.Destination
	if b.Method != "kubernetes" || b.Namespace != p.namespace || b.ServiceAccount != p.name || b.Container != ContainerName || !domain.ValidIdentitySegment(b.PodUID) || b.NamespaceUID == "" || d.Identity.Provider != "kubernetes" || d.Identity.NamespaceUID != b.NamespaceUID || d.Quarantined || d.Scope != b.Agent.Scope || d.ID != b.Agent.DestinationID || d.Generation != r.Registration.DestinationGeneration || b.Agent != r.Registration.Agent || p.publisher != (len(r.Registration.Exports) > 0) || r.CreatedAt.IsZero() || r.CreatedAt.After(now.Add(domain.MaxClockSkew)) || !r.ExpiresAt.After(r.CreatedAt) || r.ExpiresAt.Sub(r.CreatedAt) > workloadidentity.EnrollmentLifetime {
		return agent.LaunchConfig{}, ErrLaunch
	}
	cfg := agent.LaunchConfig{Version: 1, ControlEndpoint: s.ControlEndpoint, WorkloadAPI: workloadAPI, StatePath: statePath, EnrollmentID: r.ID, EnrollmentExpiresAt: r.ExpiresAt, Registration: r.Registration, Namespace: p.namespace, Targets: s.Targets, TrustGeneration: s.TrustGeneration, SigningKeys: s.SigningKeys}
	if err := cfg.Validate(now); err != nil {
		return agent.LaunchConfig{}, ErrLaunch
	}
	return cfg, nil
}

func (p *KubernetesPlan) matches(pod *corev1.Pod, uid, namespaceUID string) bool {
	if pod == nil || pod.Name != p.name || pod.Namespace != p.namespace || string(pod.UID) != uid || pod.DeletionTimestamp != nil || (len(pod.OwnerReferences) != 0 && !matchesNamespaceOwner(pod.OwnerReferences, p.namespace, namespaceUID)) || pod.Status.Phase != corev1.PodPending || pod.Spec.NodeName == "" || len(pod.Spec.InitContainers) != 0 || len(pod.Spec.EphemeralContainers) != 0 {
		return false
	}
	scheduled := false
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionTrue {
			scheduled = true
		}
	}
	if !scheduled {
		return false
	}
	for _, s := range pod.Status.ContainerStatuses {
		if s.RestartCount != 0 || s.State.Running != nil || s.State.Terminated != nil || s.LastTerminationState.Terminated != nil {
			return false
		}
	}
	return p.matchesSpec(pod.Spec)
}

func (p *KubernetesPlan) matchesSpec(got corev1.PodSpec) bool {
	want := p.Pod().Spec
	return len(got.InitContainers) == 0 && len(got.EphemeralContainers) == 0 && !got.HostNetwork && !got.HostPID && !got.HostIPC && !ptr.Deref(got.ShareProcessNamespace, false) && got.RestartPolicy == want.RestartPolicy && got.ServiceAccountName == want.ServiceAccountName && got.AutomountServiceAccountToken != nil && !*got.AutomountServiceAccountToken && got.EnableServiceLinks != nil && !*got.EnableServiceLinks && equality.Semantic.DeepEqual(got.SecurityContext, want.SecurityContext) && equality.Semantic.DeepEqual(got.Containers, want.Containers) && equality.Semantic.DeepEqual(got.Volumes, want.Volumes)
}
