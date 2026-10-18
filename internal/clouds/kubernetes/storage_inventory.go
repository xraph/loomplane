package kubernetes

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	klabels "k8s.io/apimachinery/pkg/labels"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

// StorageVolumes reports owned PVCs, including claims retained after removal.
func (c *Client) StorageVolumes(ctx context.Context, org, connection string) ([]owned.StoredVolume, error) {
	ns, err := c.Kube.CoreV1().Namespaces().Get(ctx, c.Config.Namespace, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if c.Config.Details["namespace_uid"] == "" || string(ns.UID) != c.Config.Details["namespace_uid"] {
		return nil, errors.New("the checked Kubernetes namespace identity changed")
	}
	claims, err := c.Kube.CoreV1().PersistentVolumeClaims(c.Config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: klabels.Set{owned.LabelOrg: org, owned.LabelConnection: connection}.String()})
	if err != nil {
		return nil, err
	}
	pods, err := c.Kube.CoreV1().Pods(c.Config.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	attached := map[string]bool{}
	for _, pod := range pods.Items {
		for _, mount := range pod.Spec.Volumes {
			if mount.PersistentVolumeClaim != nil {
				attached[mount.PersistentVolumeClaim.ClaimName] = true
			}
		}
	}
	volumes := make([]owned.StoredVolume, 0, len(claims.Items))
	for _, claim := range claims.Items {
		volume, ok := owned.InventoryVolume(claim.Name, org, connection, claim.Labels)
		if !ok {
			continue
		}
		volume.Provider = "kubernetes"
		if claim.Spec.StorageClassName != nil {
			volume.StorageClass = *claim.Spec.StorageClassName
		}
		if size, exists := claim.Status.Capacity[corev1.ResourceStorage]; exists {
			volume.CapacityBytes = size.Value()
		}
		switch {
		case claim.DeletionTimestamp != nil:
			volume.Status = "deleting"
		case claim.Status.Phase == corev1.ClaimLost:
			volume.Status = "lost"
		case claim.Status.Phase != corev1.ClaimBound:
			volume.Status = "pending"
		case attached[claim.Name]:
			volume.Status = "in-use"
		}
		volumes = append(volumes, volume)
	}
	return volumes, nil
}
