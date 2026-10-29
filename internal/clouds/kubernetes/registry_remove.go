package kubernetes

import (
	"context"
	"errors"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func (c *Client) ManagedRegistryExists(ctx context.Context, org, connection string) (bool, error) {
	name, identity, err := registryIdentity(org, connection, c.Config.Details["namespace_uid"])
	if err != nil {
		return false, err
	}
	d, err := c.Kube.AppsV1().Deployments(c.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, managedOwner(d, identity, c.Config.Details["registry_hostname"])
}

// RemoveManagedRegistry deletes the connection's owned registry resources.
// A caller requesting volume deletion must first persist a verified backup.
func (c *Client) RemoveManagedRegistry(ctx context.Context, org, connection string, deleteData bool) error {
	name, identity, err := registryIdentity(org, connection, c.Config.Details["namespace_uid"])
	if err != nil {
		return err
	}
	host, ns := c.Config.Details["registry_hostname"], c.Config.Namespace
	// Check controllers as well as running pods, including scaled-to-zero users.
	deployments, err := c.Kube.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, d := range deployments.Items {
		if (d.Namespace != ns || d.Name != name) && registryPodUses(d.Spec.Template.Spec, host) {
			return errors.New("a deployment still uses this registry")
		}
	}
	sets, err := c.Kube.AppsV1().StatefulSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, set := range sets.Items {
		if registryPodUses(set.Spec.Template.Spec, host) {
			return errors.New("a stateful workload still uses this registry")
		}
	}
	daemons, err := c.Kube.AppsV1().DaemonSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, daemon := range daemons.Items {
		if registryPodUses(daemon.Spec.Template.Spec, host) {
			return errors.New("a daemon workload still uses this registry")
		}
	}
	schedules, err := c.Kube.BatchV1().CronJobs("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, schedule := range schedules.Items {
		if registryPodUses(schedule.Spec.JobTemplate.Spec.Template.Spec, host) {
			return errors.New("a scheduled job still uses this registry")
		}
	}
	jobs, err := c.Kube.BatchV1().Jobs("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, job := range jobs.Items {
		if job.Status.CompletionTime == nil && registryPodUses(job.Spec.Template.Spec, host) {
			return errors.New("a job still uses this registry")
		}
	}
	pods, err := c.Kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		if registryPodUses(pod.Spec, host) {
			return errors.New("a pod still uses this registry")
		}
	}
	for _, d := range deployments.Items {
		if d.Name != name || d.Namespace != ns {
			continue
		}
		if operationErr := registryRemovalOwner(&d, identity, host); operationErr != nil {
			return operationErr
		}
		if d.DeletionTimestamp == nil {
			err = c.Kube.AppsV1().Deployments(ns).Delete(ctx, name, registryDeleteOptions(&d))
			if err != nil {
				return err
			}
		}
		return ErrRegistryPending
	}
	if operationErr := c.DeleteRouteIngress(ctx, name); operationErr != nil {
		return operationErr
	}
	service, err := c.Kube.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if operationErr := registryRemovalOwner(service, identity, host); operationErr != nil {
			return operationErr
		}
		if operationErr := c.Kube.CoreV1().Services(ns).Delete(ctx, name, registryDeleteOptions(service)); operationErr != nil {
			return operationErr
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	secret, err := c.Kube.CoreV1().Secrets(ns).Get(ctx, name+"-auth", metav1.GetOptions{})
	if err == nil {
		if operationErr := registryRemovalOwner(secret, identity, host); operationErr != nil {
			return operationErr
		}
		if operationErr := c.Kube.CoreV1().Secrets(ns).Delete(ctx, secret.Name, registryDeleteOptions(secret)); operationErr != nil {
			return operationErr
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if deleteData {
		claim, getErr := c.Kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, metav1.GetOptions{})
		if getErr == nil {
			if operationErr := registryRemovalOwner(claim, identity, host); operationErr != nil {
				return operationErr
			}
			if claim.Spec.VolumeName != "" {
				pv, pvErr := c.Kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
				if pvErr != nil {
					return pvErr
				}
				if pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.UID != claim.UID || pv.Spec.ClaimRef.Namespace != ns || pv.Spec.ClaimRef.Name != name {
					return ErrOwned
				}
				if pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimDelete {
					pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
					if _, err = c.Kube.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{}); err != nil {
						return err
					}
				}
			}
			if claim.DeletionTimestamp == nil {
				if operationErr := c.Kube.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, name, registryDeleteOptions(claim)); operationErr != nil {
					return operationErr
				}
			}
			return ErrRegistryPending
		} else if !apierrors.IsNotFound(getErr) {
			return getErr
		}
		volumes, listErr := c.Kube.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
		if listErr != nil {
			return listErr
		}
		for _, pv := range volumes.Items {
			if pv.Spec.ClaimRef != nil && pv.Spec.ClaimRef.Namespace == ns && pv.Spec.ClaimRef.Name == name {
				return ErrRegistryPending
			}
		}
	}
	remaining, err := c.Kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(identity).String()})
	if err != nil {
		return err
	}
	if len(remaining.Items) > 0 {
		return ErrRegistryPending
	}
	return nil
}
func registryPodUses(spec corev1.PodSpec, host string) bool {
	for _, group := range [][]corev1.Container{spec.Containers, spec.InitContainers} {
		for _, container := range group {
			if strings.HasPrefix(container.Image, host+"/") {
				return true
			}
		}
	}
	return false
}

func registryRemovalOwner(object metav1.Object, identity map[string]string, host string) error {
	if object.GetAnnotations()[managedRegistryHost] != host {
		return ErrOwned
	}
	return checkJobOwner(object, identity)
}
