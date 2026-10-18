package kubernetes

import (
	"context"
	"errors"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func checkRetainedClass(sc *storagev1.StorageClass) error {
	if sc.UID == "" || sc.Provisioner == "" || sc.ReclaimPolicy == nil || *sc.ReclaimPolicy != corev1.PersistentVolumeReclaimRetain || sc.VolumeBindingMode == nil || (*sc.VolumeBindingMode != storagev1.VolumeBindingImmediate && *sc.VolumeBindingMode != storagev1.VolumeBindingWaitForFirstConsumer) {
		return errors.New("the selected storage class must retain filesystem volumes and declare its binding mode")
	}
	return nil
}
func (c *Client) checkPersistentStorage(ctx context.Context, out *Result) error {
	class := c.Config.Details["storage_class"]
	if class == "" {
		return nil
	}
	sc, err := c.Kube.StorageV1().StorageClasses().Get(ctx, class, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if checkErr := checkRetainedClass(sc); checkErr != nil {
		return checkErr
	}
	out.Details["storage_class_uid"] = string(sc.UID)
	out.Details["storage_provisioner"] = sc.Provisioner
	out.Details["storage_binding"] = string(*sc.VolumeBindingMode)
	out.Details["storage_reclaim"] = string(*sc.ReclaimPolicy)
	out.Details["storage_expansion"] = strconv.FormatBool(sc.AllowVolumeExpansion != nil && *sc.AllowVolumeExpansion)
	out.Details["persistent_storage"] = "ready"
	allowed, err := c.allowedIn(ctx, "", "", "persistentvolumes", "get")
	if err != nil {
		return err
	}
	if !allowed {
		out.Missing = append(out.Missing, "persistentvolumes:get")
		out.Details["persistent_storage"] = "unavailable"
	}
	for _, resource := range []struct {
		group, name string
		verbs       []string
	}{
		{"apps", "statefulsets", []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		{"", "persistentvolumeclaims", []string{"get", "list", "create"}},
	} {
		for _, verb := range resource.verbs {
			allowed, checkErr := c.allowed(ctx, resource.group, resource.name, verb)
			if checkErr != nil {
				return checkErr
			}
			if !allowed {
				out.Missing = append(out.Missing, resource.name+":"+verb)
				out.Details["persistent_storage"] = "unavailable"
			}
		}
	}
	return nil
}
