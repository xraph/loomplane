package kubernetes

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"reflect"
	"time"

	"github.com/xraph/ctrlplane/id"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	klabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/xraph/loomplane/internal/clouds/owned"
	nativepg "github.com/xraph/loomplane/internal/clouds/postgres"
)

var ErrIsolation = errors.New("the cluster did not verify private recovery network isolation")

const proofAnnotation = "loomplane.recovery.proof"

func recoveryPolicyName(iid id.ID) string { return WorkloadName(iid) + "-isolation" }
func recoveryIdentity(iid id.ID, labels map[string]string) map[string]string {
	identity := objectLabels(iid, labels)
	identity[owned.LabelConnection] = labels[owned.LabelConnection]
	identity[nativepg.LabelDatabase] = labels[nativepg.LabelDatabase]
	identity[nativepg.LabelRecovery] = labels[nativepg.LabelRecovery]
	identity[recoveryTargetLabel] = "true"
	return identity
}
func denyRecoverySpec(identity map[string]string) networkingv1.NetworkPolicySpec {
	return networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: maps.Clone(identity)}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}}
}
func checkRecoveryPolicy(policy *networkingv1.NetworkPolicy, iid id.ID, labels map[string]string) error {
	if err := checkOwner(policy, iid, labels); err != nil {
		return err
	}
	if policy.DeletionTimestamp != nil || policy.Labels[nativepg.LabelRecovery] != labels[nativepg.LabelRecovery] || !reflect.DeepEqual(policy.Spec, denyRecoverySpec(recoveryIdentity(iid, labels))) {
		return ErrIsolation
	}
	return nil
}
func (r *Runtime) ensureRecoveryPolicy(ctx context.Context, c *Client, iid id.ID, labels, identity map[string]string) error {
	policies := c.Kube.NetworkingV1().NetworkPolicies(c.Config.Namespace)
	current, err := policies.Get(ctx, recoveryPolicyName(iid), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		current, err = policies.Create(ctx, &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: recoveryPolicyName(iid), Namespace: c.Config.Namespace, Labels: maps.Clone(identity)}, Spec: denyRecoverySpec(identity)}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			current, err = policies.Get(ctx, recoveryPolicyName(iid), metav1.GetOptions{})
		}
	}
	if err != nil {
		return err
	}
	return checkRecoveryPolicy(current, iid, labels)
}
func noOtherRecoveryAllows(ctx context.Context, c *Client, own *networkingv1.NetworkPolicy, pod *corev1.Pod) error {
	all, err := c.Kube.NetworkingV1().NetworkPolicies(c.Config.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, policy := range all.Items {
		if policy.UID == own.UID && policy.Name == own.Name {
			continue
		}
		selector, selectErr := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
		if selectErr != nil {
			return selectErr
		}
		if selector.Matches(klabels.Set(pod.Labels)) && (len(policy.Spec.Ingress) > 0 || len(policy.Spec.Egress) > 0) {
			return errors.New("another network policy allows traffic to or from the recovery copy")
		}
	}
	return nil
}
func probeLabels(s *postgresPod) map[string]string {
	labels := maps.Clone(s.parent.Labels)
	labels[owned.LabelInstance] = "probe-" + s.parent.Labels[owned.LabelInstance]
	labels[recoveryTargetLabel] = "false"
	labels[recoveryProbeLabel] = "true"
	return labels
}
func readyPod(pod *corev1.Pod) bool {
	if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
func waitProbe(ctx context.Context, c *Client, pod *corev1.Pod) (*corev1.Pod, error) {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		current, err := c.Kube.CoreV1().Pods(c.Config.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if current.UID != pod.UID || !reflect.DeepEqual(current.Labels, pod.Labels) || current.DeletionTimestamp != nil {
			return nil, ErrOwned
		}
		if readyPod(current) && net.ParseIP(current.Status.PodIP) != nil {
			return current, nil
		}
		if current.Status.Phase == corev1.PodFailed || time.Now().After(deadline) {
			return nil, errors.New("recovery network probe did not become ready")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
func probeReady(ctx context.Context, c *Client, pod *corev1.Pod, host string) error {
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() {
		return ErrIsolation
	}
	return execPod(ctx, c, pod, []string{"pg_isready", "-t", "2", "-h", host, "-p", "5432"}, nil, io.Discard)
}
func noResponse(err error) bool {
	var exit interface{ ExitStatus() int }
	return errors.As(err, &exit) && exit.ExitStatus() == 2
}

// verifyRecoveryIsolation tests positive connectivity, then both denied directions,
// while the copy is still empty. No backup bytes are read before this succeeds.
func (r *Runtime) verifyRecoveryIsolation(ctx context.Context, s *postgresPod) (err error) {
	iid, err := id.Parse(s.parent.Labels[owned.LabelInstance])
	if err != nil {
		return err
	}
	c, ns := s.client, s.client.Config.Namespace
	policies := c.Kube.NetworkingV1().NetworkPolicies(ns)
	policy, err := policies.Get(ctx, recoveryPolicyName(iid), metav1.GetOptions{})
	if err != nil {
		return err
	}
	if checkErr := checkRecoveryPolicy(policy, iid, s.labels); checkErr != nil {
		return checkErr
	}
	if checkErr := noOtherRecoveryAllows(ctx, c, policy, s.pod); checkErr != nil {
		return checkErr
	}
	if policy.Annotations[proofAnnotation] != "" {
		return errors.New("another recovery isolation check is active")
	}
	token := rand.Text()
	proofIdentity := probeLabels(s)
	automount := false
	probe := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.parent.Name + "-check", Namespace: ns, Labels: proofIdentity}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &automount, NodeName: s.pod.Spec.NodeName, RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "probe", Image: s.pod.Spec.Containers[0].Image, ImagePullPolicy: corev1.PullIfNotPresent, Env: []corev1.EnvVar{podIdentityEnv(), {Name: "POSTGRES_PASSWORD", Value: rand.Text() + rand.Text()}, {Name: "POSTGRES_DB", Value: "isolation_probe"}}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(5432)}}, PeriodSeconds: 1}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("256Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")}}}}}}
	probe, err = c.Kube.CoreV1().Pods(ns).Create(ctx, probe, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	policyName, policyUID, probeName, probeUID := policy.Name, policy.UID, probe.Name, probe.UID
	var leased bool
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if leased {
			current, getErr := policies.Get(cleanup, policyName, metav1.GetOptions{})
			if getErr == nil && current.UID == policyUID && current.Annotations[proofAnnotation] == token {
				current.Spec = denyRecoverySpec(recoveryIdentity(iid, s.labels))
				delete(current.Annotations, proofAnnotation)
				_, getErr = policies.Update(cleanup, current, metav1.UpdateOptions{})
			} else if getErr == nil && (current.UID != policyUID || current.Annotations[proofAnnotation] != "") {
				getErr = ErrOwned
			}
			err = errors.Join(err, getErr)
		}
		err = errors.Join(err, c.Kube.CoreV1().Pods(ns).Delete(cleanup, probeName, deleteUID(probeUID)))
		err = errors.Join(err, waitProbeRemoved(cleanup, c, probeName, probeUID))
	}()
	ready, readyErr := waitProbe(ctx, c, probe)
	if readyErr != nil {
		return readyErr
	}
	probe = ready
	peer := networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{MatchLabels: proofIdentity}}
	policy.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{peer}}}
	policy.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{peer}}}
	if policy.Annotations == nil {
		policy.Annotations = map[string]string{}
	}
	policy.Annotations[proofAnnotation] = token
	leased = true
	policy, err = policies.Update(ctx, policy, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	// Allow time for the CNI's policy controller, checking both positive paths.
	allowed := false
	for attempt := 0; attempt < 8; attempt++ {
		if probeReady(ctx, c, probe, s.pod.Status.PodIP) == nil && probeReady(ctx, c, s.pod, probe.Status.PodIP) == nil {
			allowed = true
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if !allowed {
		return fmt.Errorf("%w: the positive connectivity control failed", ErrIsolation)
	}
	policy.Spec = denyRecoverySpec(recoveryIdentity(iid, s.labels))
	policy, err = policies.Update(ctx, policy, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	denied := false
	for attempt := 0; attempt < 8; attempt++ {
		ingress := probeReady(ctx, c, probe, s.pod.Status.PodIP)
		egress := probeReady(ctx, c, s.pod, probe.Status.PodIP)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if noResponse(ingress) && noResponse(egress) {
			denied = true
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if !denied {
		return fmt.Errorf("%w: ingress or egress remained reachable", ErrIsolation)
	}
	if checkErr := probeReady(ctx, c, probe, "127.0.0.1"); checkErr != nil {
		return checkErr
	}
	if checkErr := probeReady(ctx, c, s.pod, "127.0.0.1"); checkErr != nil {
		return checkErr
	}
	current, err := policies.Get(ctx, policy.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if current.UID != policy.UID {
		return ErrOwned
	}
	if checkErr := checkRecoveryPolicy(current, iid, s.labels); checkErr != nil {
		return checkErr
	}
	return noOtherRecoveryAllows(ctx, c, current, s.pod)
}

func waitProbeRemoved(ctx context.Context, c *Client, name string, uid types.UID) error {
	for {
		pod, err := c.Kube.CoreV1().Pods(c.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if pod.UID != uid {
			return ErrOwned
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
