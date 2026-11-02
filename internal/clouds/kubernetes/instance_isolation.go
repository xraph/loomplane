package kubernetes

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	kubeclient "k8s.io/client-go/kubernetes"

	"github.com/xraph/loomplane/pkg/provider"
)

const instanceIsolationObjectName = "loomplane-instance-isolation"
const instanceIsolationAnnotation = "loomplane.io/instance-isolation"

// InstanceIsolationState retains exact object identities across response loss.
type InstanceIsolationState struct {
	Digest    string            `json:"digest"`
	Objects   map[string]string `json:"objects"`
	Attempted map[string]bool   `json:"attempted"`
}

func instanceSecurityLabels(p *provider.InstanceIsolationPolicy) map[string]string {
	if p == nil {
		return nil
	}
	return map[string]string{"pod-security.kubernetes.io/enforce": "restricted", "pod-security.kubernetes.io/enforce-version": p.PodSecurityVersion, "pod-security.kubernetes.io/audit": "restricted", "pod-security.kubernetes.io/audit-version": p.PodSecurityVersion, "pod-security.kubernetes.io/warn": "restricted", "pod-security.kubernetes.io/warn-version": p.PodSecurityVersion}
}
func instanceSecurityMatches(ns *corev1.Namespace, p *provider.InstanceIsolationPolicy) bool {
	annotations, err := instanceNamespaceAnnotations(p)
	if err != nil || ns.Annotations[instanceIsolationAnnotation] != annotations[instanceIsolationAnnotation] {
		return false
	}
	for key, value := range instanceSecurityLabels(p) {
		if ns.Labels[key] != value {
			return false
		}
	}
	return true
}
func instanceNetworkPorts(ports []provider.InstanceNetworkPort) []networkingv1.NetworkPolicyPort {
	out := make([]networkingv1.NetworkPolicyPort, 0, len(ports))
	for _, p := range ports {
		protocol := corev1.Protocol(p.Protocol)
		port := intstr.FromInt32(p.Port)
		out = append(out, networkingv1.NetworkPolicyPort{Protocol: &protocol, Port: &port})
	}
	return out
}
func instanceIsolationObjects(in EnrollmentRequest) (*corev1.ResourceQuota, *corev1.LimitRange, *networkingv1.NetworkPolicy, error) {
	p := in.InstancePolicy
	digest, err := p.Digest()
	if err != nil {
		return nil, nil, nil, err
	}
	meta := metav1.ObjectMeta{Name: instanceIsolationObjectName, Namespace: in.Namespace, Labels: map[string]string{enrollmentLabel: in.AttachmentID, projectLabel: in.ProjectID}, Annotations: map[string]string{instanceIsolationAnnotation: digest}}
	cpu := *resource.NewMilliQuantity(p.CPUMillis, resource.DecimalSI)
	memory := *resource.NewQuantity(p.MemoryMiB<<20, resource.BinarySI)
	hard := corev1.ResourceList{corev1.ResourceRequestsCPU: cpu, corev1.ResourceLimitsCPU: cpu.DeepCopy(), corev1.ResourceRequestsMemory: memory, corev1.ResourceLimitsMemory: memory.DeepCopy(), corev1.ResourcePods: *resource.NewQuantity(p.MaxPods, resource.DecimalSI)}
	for name, count := range map[corev1.ResourceName]int64{"count/jobs.batch": p.MaxPods, "count/deployments.apps": p.MaxPods, corev1.ResourceServices: p.MaxPods + 2, corev1.ResourceSecrets: p.MaxPods*4 + 16, corev1.ResourcePersistentVolumeClaims: 0} {
		hard[name] = *resource.NewQuantity(count, resource.DecimalSI)
	}
	if p.EphemeralMiB > 0 {
		amount := *resource.NewQuantity(p.EphemeralMiB<<20, resource.BinarySI)
		hard[corev1.ResourceRequestsEphemeralStorage] = amount
		hard[corev1.ResourceLimitsEphemeralStorage] = amount.DeepCopy()
	}
	quota := &corev1.ResourceQuota{ObjectMeta: *meta.DeepCopy(), Spec: corev1.ResourceQuotaSpec{Hard: hard}}
	limits := &corev1.LimitRange{ObjectMeta: *meta.DeepCopy(), Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{Type: corev1.LimitTypeContainer, Min: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1m"), corev1.ResourceMemory: resource.MustParse("1Mi")}, Max: corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory}, Default: corev1.ResourceList{corev1.ResourceCPU: cpu.DeepCopy(), corev1.ResourceMemory: memory.DeepCopy()}, DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: cpu.DeepCopy(), corev1.ResourceMemory: memory.DeepCopy()}}}}}
	// A namespace is one instance trust boundary. External traffic requires
	// exact resource addresses from the reviewed grants.
	same := networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}}
	policy := &networkingv1.NetworkPolicy{ObjectMeta: *meta.DeepCopy(), Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{same}}}, Egress: []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{same}}}}}
	for _, endpoint := range p.Egress {
		policy.Spec.Egress = append(policy.Spec.Egress, networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: endpoint.CIDR}}}, Ports: instanceNetworkPorts(endpoint.Ports)})
	}
	return quota, limits, policy, nil
}

func establishInstanceIsolation(ctx context.Context, k kubeclient.Interface, in EnrollmentRequest, state *EnrollmentState, checkpoint EnrollmentCheckpoint) error {
	if in.InstancePolicy == nil {
		return nil
	}
	quota, limits, policy, err := instanceIsolationObjects(in)
	if err != nil {
		return err
	}
	digest := quota.Annotations[instanceIsolationAnnotation]
	if state.Isolation == nil {
		state.Isolation = &InstanceIsolationState{Digest: digest, Objects: map[string]string{}, Attempted: map[string]bool{}}
	}
	saved := state.Isolation
	if saved.Digest != digest || saved.Objects == nil || saved.Attempted == nil {
		return ErrInvalid
	}
	if checkErr := rejectForeignInstancePolicies(ctx, k, in.Namespace); checkErr != nil {
		return checkErr
	}
	objects := []struct {
		kind   string
		want   runtime.Object
		get    func() (runtime.Object, error)
		create func() (runtime.Object, error)
	}{
		{"ResourceQuota", quota, func() (runtime.Object, error) {
			return k.CoreV1().ResourceQuotas(in.Namespace).Get(ctx, quota.Name, metav1.GetOptions{})
		}, func() (runtime.Object, error) {
			return k.CoreV1().ResourceQuotas(in.Namespace).Create(ctx, quota, metav1.CreateOptions{})
		}},
		{"LimitRange", limits, func() (runtime.Object, error) {
			return k.CoreV1().LimitRanges(in.Namespace).Get(ctx, limits.Name, metav1.GetOptions{})
		}, func() (runtime.Object, error) {
			return k.CoreV1().LimitRanges(in.Namespace).Create(ctx, limits, metav1.CreateOptions{})
		}},
		{"NetworkPolicy", policy, func() (runtime.Object, error) {
			return k.NetworkingV1().NetworkPolicies(in.Namespace).Get(ctx, policy.Name, metav1.GetOptions{})
		}, func() (runtime.Object, error) {
			return k.NetworkingV1().NetworkPolicies(in.Namespace).Create(ctx, policy, metav1.CreateOptions{})
		}},
	}
	for _, item := range objects {
		got, getErr := item.get()
		if apierrors.IsNotFound(getErr) && saved.Objects[item.kind] == "" {
			if identityErr := VerifyRuntimeIdentity(ctx, k, in.Namespace, state.NamespaceUID, in.ServiceAccountName, state.ServiceAccountUID); identityErr != nil {
				return identityErr
			}
			saved.Attempted[item.kind] = true
			if checkErr := checkpoint(ctx, state); checkErr != nil {
				return checkErr
			}
			got, getErr = item.create()
		}
		if getErr != nil {
			return getErr
		}
		meta, ok := got.(metav1.Object)
		if !ok || meta.GetUID() == "" || meta.GetDeletionTimestamp() != nil || len(meta.GetOwnerReferences()) != 0 || meta.GetLabels()[enrollmentLabel] != in.AttachmentID || meta.GetLabels()[projectLabel] != in.ProjectID || meta.GetAnnotations()[instanceIsolationAnnotation] != digest || !saved.Attempted[item.kind] || saved.Objects[item.kind] != "" && saved.Objects[item.kind] != string(meta.GetUID()) || !sameInstanceIsolationObject(item.want, got) {
			return ErrInvalid
		}
		saved.Objects[item.kind] = string(meta.GetUID())
		if checkErr := checkpoint(ctx, state); checkErr != nil {
			return checkErr
		}
	}
	return nil
}
func sameInstanceIsolationObject(want, got runtime.Object) bool {
	switch w := want.(type) {
	case *corev1.ResourceQuota:
		g, ok := got.(*corev1.ResourceQuota)
		return ok && apiequality.Semantic.DeepEqual(w.Spec, g.Spec)
	case *corev1.LimitRange:
		g, ok := got.(*corev1.LimitRange)
		return ok && apiequality.Semantic.DeepEqual(w.Spec, g.Spec)
	case *networkingv1.NetworkPolicy:
		g, ok := got.(*networkingv1.NetworkPolicy)
		return ok && apiequality.Semantic.DeepEqual(w.Spec, g.Spec)
	}
	return false
}
func rejectForeignInstancePolicies(ctx context.Context, k kubeclient.Interface, namespace string) error {
	policies, err := k.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, policy := range policies.Items {
		if policy.Name != instanceIsolationObjectName {
			return ErrInvalid
		}
	}
	return nil
}

func instanceNamespaceAnnotations(p *provider.InstanceIsolationPolicy) (map[string]string, error) {
	if p == nil {
		return nil, nil
	}
	digest, err := p.Digest()
	if err != nil {
		return nil, err
	}
	return map[string]string{instanceIsolationAnnotation: digest}, nil
}
