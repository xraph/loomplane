package federation

import (
	"bytes"
	"context"
	"time"

	"github.com/xraph/ctrlplane/id"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"

	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// WorkloadDeployment is a projection of a current, successfully deployed service
// binding. The deployment layer supplies it; management requests cannot choose
// its scope, instance, connection or image.
type WorkloadDeployment struct {
	domain.Scope
	BindingID    string
	InstanceID   string
	ConnectionID string
	ServiceID    string
	Container    string
	Image        string
}

// KubernetesWorkloadProfile retains controller identity from the trusted
// deployment/installation record. A matching name or copied label cannot replace
// its Deployment UID. These pins must be retained before application issuance.
type KubernetesWorkloadProfile struct {
	KubernetesAttestationProfile
	BindingID            string   `json:"binding_id"`
	DeploymentUID        string   `json:"deployment_uid"`
	DeploymentGeneration int64    `json:"deployment_generation"`
	RuntimeRevision      string   `json:"runtime_revision"`
	ImageIndexes         [][]byte `json:"image_indexes,omitempty"`
}

type KubernetesWorkloadVerifier func(context.Context, domain.Destination, WorkloadDeployment, KubernetesWorkloadProfile) (workloadidentity.WorkloadBinding, error)

// VerifyKubernetesWorkload reads ownership and SPIRE evidence through the current
// explicit connection. It creates no registration and grants no service access.
func VerifyKubernetesWorkload(credentials InstallationCredentials, nodes SPIRENodeReader, trustDomain, org string) KubernetesWorkloadVerifier {
	_, scopeErr := workloadidentity.ControlURIForOrganization(trustDomain, org)
	return func(ctx context.Context, d domain.Destination, binding WorkloadDeployment, p KubernetesWorkloadProfile) (workloadidentity.WorkloadBinding, error) {
		if err := ctx.Err(); err != nil {
			return workloadidentity.WorkloadBinding{}, err
		}
		if scopeErr != nil || d.OrgID != org || nodes == nil || !workloadProfileMatches(binding, p, d) {
			return workloadidentity.WorkloadBinding{}, ErrUnverified
		}
		indexes := make([][]byte, len(p.ImageIndexes))
		for i, raw := range p.ImageIndexes {
			indexes[i] = bytes.Clone(raw)
		}
		p.ImageIndexes = indexes
		client, err := openInstallationKubernetes(ctx, credentials, org, d)
		if err != nil {
			return workloadidentity.WorkloadBinding{}, err
		}
		r := &kubernetesWorkloadResolver{attestation: &KubernetesAttestationResolver{client: client, nodes: nodes, profile: p.KubernetesAttestationProfile, domain: trustDomain, now: time.Now}, profile: p, binding: binding}
		return r.Resolve(ctx, d)
	}
}

type kubernetesWorkloadResolver struct {
	attestation *KubernetesAttestationResolver
	profile     KubernetesWorkloadProfile
	binding     WorkloadDeployment
}

func workloadProfileMatches(b WorkloadDeployment, p KubernetesWorkloadProfile, d domain.Destination) bool {
	instance, err := id.ParseWithPrefix(b.InstanceID, id.PrefixInstance)
	return err == nil && instance.String() == b.InstanceID && b.Scope == d.Scope && b.ConnectionID == d.ConnectionID && domain.ValidIdentitySegment(b.BindingID) && b.BindingID == p.BindingID && domain.ValidIdentitySegment(b.ServiceID) && b.Container == p.Container && domain.ValidPinnedImage(b.Image) && validAttestationProfile(p.KubernetesAttestationProfile) && profileMatchesDestination(p.KubernetesAttestationProfile, d) && domain.ValidIdentitySegment(p.DeploymentUID) && p.DeploymentGeneration > 0 && p.RuntimeRevision != "" && len(p.RuntimeRevision) <= 512 && domain.ValidImageIndexes(p.ImageIndexes)
}

func (r *kubernetesWorkloadResolver) Resolve(ctx context.Context, d domain.Destination) (workloadidentity.WorkloadBinding, error) {
	if err := ctx.Err(); err != nil {
		return workloadidentity.WorkloadBinding{}, err
	}
	if r == nil || r.attestation == nil || r.attestation.profile != r.profile.KubernetesAttestationProfile || !workloadProfileMatches(r.binding, r.profile, d) {
		return workloadidentity.WorkloadBinding{}, ErrUnverified
	}
	var imageSelector string
	binding, err := r.attestation.resolve(ctx, d, func(ctx context.Context, pod *corev1.Pod, node *corev1.Node) error {
		var verifyErr error
		imageSelector, verifyErr = r.verifyOwnership(ctx, pod, node)
		return verifyErr
	})
	if err != nil {
		return workloadidentity.WorkloadBinding{}, err
	}
	return workloadidentity.WorkloadBinding{Binding: binding, Workload: domain.WorkloadSelector{Scope: d.Scope, DestinationID: d.ID, ServiceID: r.binding.ServiceID}, ContainerImage: imageSelector}, nil
}

func (r *kubernetesWorkloadResolver) verifyOwnership(ctx context.Context, pod *corev1.Pod, node *corev1.Node) (string, error) {
	p, b := r.profile, r.binding
	imageSelector, imageOK := r.runningImage(pod, node)
	if pod.Name != p.PodName || pod.Namespace != r.attestation.client.Config.Namespace || pod.ResourceVersion == "" || pod.Status.Phase != corev1.PodRunning || !r.templateMatches(pod.ObjectMeta, pod.Spec) || !imageOK {
		return "", ErrUnverified
	}
	owner, ok := workloadController(pod, "ReplicaSet")
	if !ok {
		return "", ErrUnverified
	}
	client, ns := r.attestation.client.Kube, pod.Namespace
	rs, err := client.AppsV1().ReplicaSets(ns).Get(ctx, owner.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if !ownedWorkloadObject(rs, owner.Name, string(owner.UID), ns) || !r.labelsMatch(rs.Labels) || !r.templateMatches(rs.Spec.Template.ObjectMeta, rs.Spec.Template.Spec) || !selectsWorkload(rs.Spec.Selector, pod.Labels) {
		return "", ErrUnverified
	}
	parent, ok := workloadController(rs, "Deployment")
	instance, parseErr := id.ParseWithPrefix(b.InstanceID, id.PrefixInstance)
	if !ok || parseErr != nil || parent.Name != kube.WorkloadName(instance) || string(parent.UID) != p.DeploymentUID {
		return "", ErrUnverified
	}
	deployment, err := client.AppsV1().Deployments(ns).Get(ctx, parent.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if !ownedWorkloadObject(deployment, parent.Name, p.DeploymentUID, ns) || deployment.Generation != p.DeploymentGeneration || deployment.Spec.Paused || deployment.Spec.Replicas == nil || *deployment.Spec.Replicas < 1 || !r.labelsMatch(deployment.Labels) || !r.templateMatches(deployment.Spec.Template.ObjectMeta, deployment.Spec.Template.Spec) || !selectsWorkload(deployment.Spec.Selector, rs.Labels) || !selectsWorkload(deployment.Spec.Selector, pod.Labels) {
		return "", ErrUnverified
	}
	// Kubernetes has no transaction spanning these resources. Refuse an observed
	// change during verification; issuance must also recheck the retained pins.
	currentDeployment, err := client.AppsV1().Deployments(ns).Get(ctx, deployment.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	currentRS, err := client.AppsV1().ReplicaSets(ns).Get(ctx, rs.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	currentPod, err := client.CoreV1().Pods(ns).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if !sameWorkloadVersion(deployment, currentDeployment) || !sameWorkloadVersion(rs, currentRS) || !sameWorkloadVersion(pod, currentPod) {
		return "", ErrUnverified
	}
	return imageSelector, nil
}

func (r *kubernetesWorkloadResolver) labelsMatch(values map[string]string) bool {
	b := r.binding
	return values[owned.LabelOrg] == b.OrgID && values[owned.LabelProject] == b.ProjectID && values[owned.LabelEnvironment] == b.EnvironmentID && values[owned.LabelInstance] == b.InstanceID
}

func (r *kubernetesWorkloadResolver) templateMatches(meta metav1.ObjectMeta, spec corev1.PodSpec) bool {
	sa := spec.ServiceAccountName
	if sa == "" {
		sa = "default"
	}
	if !r.labelsMatch(meta.Labels) || meta.Annotations[owned.LabelRevision] != r.profile.RuntimeRevision || sa != r.profile.ServiceAccount {
		return false
	}
	matches := 0
	for _, container := range spec.Containers {
		if container.Name == r.profile.Container {
			if container.Image != r.binding.Image {
				return false
			}
			matches++
		}
	}
	return matches == 1
}

func (r *kubernetesWorkloadResolver) runningImage(pod *corev1.Pod, node *corev1.Node) (string, bool) {
	matches := 0
	var selected string
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != r.profile.Container {
			continue
		}
		matches++
		if status.State.Running == nil {
			return "", false
		}
		image, ok := domain.ResolveImageSelector(r.binding.Image, status.ImageID, node.Status.NodeInfo.OperatingSystem, node.Status.NodeInfo.Architecture, r.profile.ImageIndexes)
		if !ok {
			return "", false
		}
		selected = image
	}
	return selected, matches == 1 && selected != ""
}

func workloadController(object metav1.Object, kind string) (metav1.OwnerReference, bool) {
	owners := object.GetOwnerReferences()
	if len(owners) != 1 {
		return metav1.OwnerReference{}, false
	}
	owner := owners[0]
	return owner, owner.Controller != nil && *owner.Controller && owner.APIVersion == "apps/v1" && owner.Kind == kind && len(validation.IsDNS1123Subdomain(owner.Name)) == 0 && domain.ValidIdentitySegment(string(owner.UID))
}

func ownedWorkloadObject(object metav1.Object, name, uid, namespace string) bool {
	return object.GetName() == name && string(object.GetUID()) == uid && object.GetNamespace() == namespace && object.GetDeletionTimestamp() == nil && object.GetResourceVersion() != ""
}

func sameWorkloadVersion(before, after metav1.Object) bool {
	return ownedWorkloadObject(after, before.GetName(), string(before.GetUID()), before.GetNamespace()) && after.GetResourceVersion() == before.GetResourceVersion()
}

func selectsWorkload(selector *metav1.LabelSelector, values map[string]string) bool {
	if selector == nil {
		return false
	}
	compiled, err := metav1.LabelSelectorAsSelector(selector)
	return err == nil && compiled.Matches(labels.Set(values))
}
