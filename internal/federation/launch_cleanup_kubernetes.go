package federation

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	domain "github.com/xraph/loomplane/pkg/federation"
)

func (s *LaunchRetirementService) cleanupKubernetes(ctx context.Context, d domain.Destination, a *LaunchAttempt, guard func(context.Context) error) error {
	client, err := openInstallationKubernetes(ctx, s.launch.credentials, s.launch.org, d)
	if err != nil {
		return err
	}
	if client.Config.Namespace != a.Namespace {
		return ErrIdentityChanged
	}
	return cleanupLaunchResources(ctx, client.Kube, d, a, guard)
}

// cleanupLaunchResources touches only the saved agent pod and exact launch
// payload. Service accounts and RBAC need separately recorded creation UIDs.
func cleanupLaunchResources(ctx context.Context, client kubernetes.Interface, d domain.Destination, a *LaunchAttempt, guard func(context.Context) error) error {
	if client == nil || a == nil || a.State != LaunchRetiring || guard == nil {
		return ErrInvalid
	}
	if err := guard(ctx); err != nil {
		return err
	}
	ns, err := client.CoreV1().Namespaces().Get(ctx, a.Namespace, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(ns.UID) != d.Identity.NamespaceUID || ns.DeletionTimestamp != nil {
		return ErrIdentityChanged
	}
	// Inspect configuration before pod garbage collection could remove it.
	if _, err = ownedLaunchConfig(ctx, client, a); err != nil {
		return err
	}
	if deleteErr := deleteOwnedPod(ctx, client, a.Namespace, a.ConfigMapName, a.PodUID, guard); deleteErr != nil {
		return deleteErr
	}
	// A different pod UID is a replacement. Preserve it and remove only the
	// configuration whose owner and digest were saved for this attempt.
	cm, err := ownedLaunchConfig(ctx, client, a)
	if err != nil {
		return err
	}
	if cm == nil {
		return guard(ctx)
	}
	if cm.ResourceVersion == "" {
		return ErrUnverified
	}
	if checkErr := guard(ctx); checkErr != nil {
		return checkErr
	}
	maps := client.CoreV1().ConfigMaps(a.Namespace)
	err = maps.Delete(ctx, cm.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: ptr.To(cm.UID), ResourceVersion: ptr.To(cm.ResourceVersion)}})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	_, err = maps.Get(ctx, a.ConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return guard(ctx)
	}
	if err != nil {
		return err
	}
	return ErrInUse
}
func ownedLaunchConfig(ctx context.Context, client kubernetes.Interface, a *LaunchAttempt) (*corev1.ConfigMap, error) {
	cm, err := client.CoreV1().ConfigMaps(a.Namespace).Get(ctx, a.ConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !matchesLaunchConfig(a, cm) {
		return nil, ErrIdentityChanged
	}
	return cm, nil
}

func deleteOwnedPod(ctx context.Context, client kubernetes.Interface, namespace, name, uid string, guard func(context.Context) error) error {
	pods := client.CoreV1().Pods(namespace)
	pod, err := pods.Get(ctx, name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil && string(pod.UID) == uid {
		if pod.ResourceVersion == "" {
			return ErrUnverified
		}
		if checkErr := guard(ctx); checkErr != nil {
			return checkErr
		}
		if pod.DeletionTimestamp == nil {
			err = pods.Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: ptr.To(types.UID(uid)), ResourceVersion: ptr.To(pod.ResourceVersion)}, PropagationPolicy: ptr.To(metav1.DeletePropagationForeground)})
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
		remaining, readErr := pods.Get(ctx, name, metav1.GetOptions{})
		if readErr != nil && !apierrors.IsNotFound(readErr) {
			return readErr
		}
		if readErr == nil && string(remaining.UID) == uid {
			return ErrInUse
		}
	}
	return guard(ctx)
}
