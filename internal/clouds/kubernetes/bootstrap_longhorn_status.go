package kubernetes

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (c *Client) discoverLonghorn(ctx context.Context, missing func(string)) error {
	driver, err := c.Kube.AppsV1().DaemonSets(bootstrapNamespace).Get(ctx, "longhorn-csi-plugin", metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		missing("cluster setup: waiting for the Longhorn CSI driver")
	case err != nil:
		return err
	case driver.Status.ObservedGeneration < driver.Generation || driver.Status.DesiredNumberScheduled < 3 || driver.Status.NumberReady != driver.Status.DesiredNumberScheduled:
		missing("cluster setup: waiting for Longhorn CSI on all storage nodes")
	}
	nodes, err := c.Dynamic.Resource(schema.GroupVersionResource{Group: "longhorn.io", Version: "v1beta2", Resource: "nodes"}).Namespace(bootstrapNamespace).List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) {
		missing("cluster setup: waiting for Longhorn storage nodes")
		return nil
	}
	if err != nil {
		return err
	}
	ready := 0
	for _, node := range nodes.Items {
		allow, _, getErr := unstructured.NestedBool(node.Object, "spec", "allowScheduling")
		if getErr != nil {
			return getErr
		}
		if !allow {
			continue
		}
		conditions, _, getErr := unstructured.NestedSlice(node.Object, "status", "conditions")
		if getErr != nil {
			return getErr
		}
		healthy := true
		for _, name := range []string{"Ready", "Schedulable", "RequiredPackages", "MountPropagation", "KernelModulesLoaded", "Multipathd", "NFSClientInstalled"} {
			found := false
			for _, raw := range conditions {
				condition, ok := raw.(map[string]any)
				if !ok {
					return ErrInvalid
				}
				if condition["type"] != name {
					continue
				}
				found = condition["status"] == "True"
				if !found {
					missing(fmt.Sprintf("cluster setup: Longhorn node %s: %v (%s)", node.GetName(), condition["message"], name))
				}
			}
			if !found {
				healthy = false
			}
		}
		rawDisks, _, getErr := unstructured.NestedFieldNoCopy(node.Object, "status", "diskStatus")
		if getErr != nil {
			return getErr
		}
		if rawDisks == nil {
			continue
		}
		disks, ok := rawDisks.(map[string]any)
		if !ok {
			return ErrInvalid
		}
		diskReady := false
		for _, raw := range disks {
			disk, ok := raw.(map[string]any)
			if !ok {
				return ErrInvalid
			}
			conditions, _, conditionErr := unstructured.NestedSlice(disk, "conditions")
			if conditionErr != nil {
				return conditionErr
			}
			statuses := map[string]bool{}
			for _, raw := range conditions {
				condition, ok := raw.(map[string]any)
				if !ok {
					return ErrInvalid
				}
				kind, ok := condition["type"].(string)
				if !ok {
					return ErrInvalid
				}
				statuses[kind] = condition["status"] == "True"
			}
			if statuses["Ready"] && statuses["Schedulable"] {
				diskReady = true
			}
		}
		if healthy && diskReady {
			ready++
		}
	}
	if ready < 3 {
		missing(fmt.Sprintf("cluster setup: Longhorn has %d of 3 required storage nodes ready; check node prerequisites and disks", ready))
	}
	return nil
}
