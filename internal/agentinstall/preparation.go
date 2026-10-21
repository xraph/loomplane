package agentinstall

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	domain "github.com/xraph/loomplane/pkg/federation"
)

const (
	PreparationAttemptAnnotation   = "federation.loomplane.io/installation-attempt"
	PreparationNamespaceAnnotation = "federation.loomplane.io/namespace-uid"
)

// PreparationResources adds the saved attempt's ownership markers to the shared
// consumer plan. Publisher preparation also needs recorded RBAC ownership.
func (p *KubernetesPlan) PreparationResources(namespaceUID string) (*corev1.ServiceAccount, *corev1.Pod, error) {
	if p == nil || p.publisher || !domain.ValidIdentitySegment(namespaceUID) {
		return nil, nil, ErrLaunch
	}
	account, pod := p.ServiceAccount(), p.Pod()
	for _, meta := range []*metav1.ObjectMeta{&account.ObjectMeta, &pod.ObjectMeta} {
		meta.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: p.namespace, UID: types.UID(namespaceUID)}}
		meta.Annotations = map[string]string{PreparationAttemptAnnotation: p.name, PreparationNamespaceAnnotation: namespaceUID}
	}
	return account, pod, nil
}
func (p *KubernetesPlan) preparationMetadata(meta metav1.ObjectMeta, namespaceUID string) bool {
	return p != nil && !p.publisher && domain.ValidIdentitySegment(namespaceUID) && meta.Name == p.name && meta.Namespace == p.namespace && meta.UID != "" && meta.DeletionTimestamp == nil && matchesNamespaceOwner(meta.OwnerReferences, p.namespace, namespaceUID) && meta.Annotations[PreparationAttemptAnnotation] == p.name && meta.Annotations[PreparationNamespaceAnnotation] == namespaceUID
}
func (p *KubernetesPlan) MatchesPreparedAccount(account *corev1.ServiceAccount, namespaceUID string) bool {
	return account != nil && p.preparationMetadata(account.ObjectMeta, namespaceUID) && account.AutomountServiceAccountToken != nil && !*account.AutomountServiceAccountToken && len(account.Secrets) == 0 && len(account.ImagePullSecrets) == 0
}
func (p *KubernetesPlan) MatchesPreparedPod(pod *corev1.Pod, namespaceUID string) bool {
	return pod != nil && p.preparationMetadata(pod.ObjectMeta, namespaceUID) && p.matchesSpec(pod.Spec)
}

func matchesNamespaceOwner(owners []metav1.OwnerReference, namespace, uid string) bool {
	return len(owners) == 1 && owners[0].APIVersion == "v1" && owners[0].Kind == "Namespace" && owners[0].Name == namespace && string(owners[0].UID) == uid && !ptr.Deref(owners[0].Controller, false) && !ptr.Deref(owners[0].BlockOwnerDeletion, false)
}
