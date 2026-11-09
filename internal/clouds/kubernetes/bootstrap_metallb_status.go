package kubernetes

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Status objects are controller output, not configuration or service consumers.
// Trace their owner UIDs to the managed controller before allowing its removal.
func (c *Client) ownedMetalLBStatus(ctx context.Context, item *unstructured.Unstructured, owner string) (bool, error) {
	if item.GetNamespace() != bootstrapNamespace {
		return false, nil
	}
	switch item.GetKind() {
	case "ConfigurationState", "ServiceL2Status", "ServiceBGPStatus":
	default:
		return false, nil
	}
	refs := item.GetOwnerReferences()
	if len(refs) != 1 || refs[0].Kind != "Pod" || refs[0].APIVersion != "v1" {
		return false, nil
	}
	pod, err := c.Kube.CoreV1().Pods(bootstrapNamespace).Get(ctx, refs[0].Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, ErrRegistryPending
	}
	if err != nil {
		return false, err
	}
	if pod.UID != refs[0].UID {
		return false, nil
	}
	for _, ref := range pod.OwnerReferences {
		if ref.Controller == nil || !*ref.Controller || ref.APIVersion != "apps/v1" {
			continue
		}
		if ref.Kind == "DaemonSet" && ref.Name == "loomplane-metallb-speaker" {
			controller, e := c.Kube.AppsV1().DaemonSets(bootstrapNamespace).Get(ctx, ref.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				return false, ErrRegistryPending
			}
			if e != nil {
				return false, e
			}
			return controller.UID == ref.UID && controller.Labels[bootstrapOwner] == owner, nil
		}
		if ref.Kind != "ReplicaSet" {
			continue
		}
		rs, e := c.Kube.AppsV1().ReplicaSets(bootstrapNamespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(e) {
			return false, ErrRegistryPending
		}
		if e != nil {
			return false, e
		}
		if rs.UID != ref.UID {
			return false, nil
		}
		for _, parent := range rs.OwnerReferences {
			if parent.Kind != "Deployment" || parent.Name != "loomplane-metallb-controller" || parent.APIVersion != "apps/v1" || parent.Controller == nil || !*parent.Controller {
				continue
			}
			controller, e := c.Kube.AppsV1().Deployments(bootstrapNamespace).Get(ctx, parent.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				return false, ErrRegistryPending
			}
			if e != nil {
				return false, e
			}
			return controller.UID == parent.UID && controller.Labels[bootstrapOwner] == owner, nil
		}
	}
	return false, nil
}
