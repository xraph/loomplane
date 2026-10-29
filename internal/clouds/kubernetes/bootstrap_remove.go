package kubernetes

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// RemoveManagedBootstrap removes owned controllers only after their consumers
// are gone. Namespaces remain because they can contain unrelated resources.
func (c *Client) RemoveManagedBootstrap(ctx context.Context) error {
	system, err := c.Kube.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return err
	}
	owner := string(system.UID)
	if owner == "" {
		return ErrOwned
	}
	var objects []*unstructured.Unstructured
	for _, file := range []string{"cert-manager.yaml", "traefik.yaml", "local-path.yaml", "longhorn.yaml", "metallb.yaml"} {
		bundle, e := bootstrapObjects(file, owner)
		if e != nil {
			return e
		}
		for _, want := range bundle {
			old, e := c.bootstrapAPI(want).Get(ctx, want.GetName(), metav1.GetOptions{})
			if apierrors.IsNotFound(e) {
				continue
			}
			if e != nil {
				return e
			}
			if old.GetLabels()[bootstrapOwner] == owner {
				objects = append(objects, old)
			}
		}
	}
	classes, err := c.Kube.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	managedClasses := map[string]bool{}
	for _, class := range classes.Items {
		if class.Labels[bootstrapOwner] == owner {
			managedClasses[class.Name] = true
		}
	}
	claims, err := c.Kube.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, claim := range claims.Items {
		if claim.Spec.StorageClassName != nil && managedClasses[*claim.Spec.StorageClassName] {
			return errors.New("retained or active claims still use managed storage")
		}
	}
	volumes, err := c.Kube.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, volume := range volumes.Items {
		if managedClasses[volume.Spec.StorageClassName] || (volume.Spec.CSI != nil && volume.Spec.CSI.Driver == "driver.longhorn.io") {
			return errors.New("retained or active volumes still use managed storage")
		}
	}
	ingresses, err := c.Kube.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, ingress := range ingresses.Items {
		if ingress.Spec.IngressClassName == nil || *ingress.Spec.IngressClassName == bootstrapIngress || ingress.Annotations["kubernetes.io/ingress.class"] == bootstrapIngress {
			return errors.New("ingresses still depend on the managed controller")
		}
	}
	services, err := c.Kube.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, service := range services.Items {
		if service.Spec.Type == "LoadBalancer" && service.Labels[bootstrapOwner] != owner && service.Annotations["metallb.io/ip-allocated-from-pool"] != "" {
			return errors.New("load balancers still depend on MetalLB")
		}
	}
	var custom []*unstructured.Unstructured
	longhorn := false
	for _, object := range objects {
		if object.GetKind() != "CustomResourceDefinition" {
			continue
		}
		group, _, groupErr := unstructured.NestedString(object.Object, "spec", "group")
		if groupErr != nil {
			return groupErr
		}
		plural, _, pluralErr := unstructured.NestedString(object.Object, "spec", "names", "plural")
		if pluralErr != nil {
			return pluralErr
		}
		versions, _, versionsErr := unstructured.NestedSlice(object.Object, "spec", "versions")
		if versionsErr != nil {
			return versionsErr
		}
		version := ""
		for _, v := range versions {
			item, ok := v.(map[string]any)
			if !ok {
				return ErrInvalid
			}
			if item["storage"] == true {
				var nameOK bool
				version, nameOK = item["name"].(string)
				if !nameOK {
					return ErrInvalid
				}
			}
		}
		if version == "" {
			return ErrInvalid
		}
		gvr := schema.GroupVersionResource{Group: group, Version: version, Resource: plural}
		list, e := c.Dynamic.Resource(gvr).List(ctx, metav1.ListOptions{})
		if apierrors.IsNotFound(e) {
			continue
		}
		if e != nil {
			return e
		}
		if group == "longhorn.io" {
			longhorn = true
			if plural == "volumes" || plural == "replicas" || plural == "engines" {
				if len(list.Items) > 0 {
					return errors.New("longhorn still contains image volumes or replicas")
				}
			}
			continue
		}
		for i := range list.Items {
			item := &list.Items[i]
			if group == "metallb.io" && item.GetLabels()[bootstrapOwner] == owner && item.GetName() == "loomplane-registry" && (plural == "ipaddresspools" || plural == "l2advertisements") {
				custom = append(custom, item)
				continue
			}
			if group == "cert-manager.io" && plural == "issuers" && item.GetNamespace() == c.Config.Namespace && item.GetName() == HTTPIssuerName && item.GetLabels()[LabelRouteShared] == sharedIssuer {
				custom = append(custom, item)
				continue
			}
			return fmt.Errorf("%s still contains resources that depend on the managed controller", object.GetName())
		}
	}
	// No writes precede the dependency checks above.
	if longhorn {
		settings := c.Dynamic.Resource(schema.GroupVersionResource{Group: "longhorn.io", Version: "v1beta2", Resource: "settings"}).Namespace(bootstrapNamespace)
		setting, e := settings.Get(ctx, "deleting-confirmation-flag", metav1.GetOptions{})
		if e != nil {
			return e
		}
		if setting.Object["value"] != "true" {
			setting.Object["value"] = "true"
			if _, e = settings.Update(ctx, setting, metav1.UpdateOptions{}); e != nil {
				return e
			}
		}
		if operationErr := c.runLonghornHook(ctx, owner, "uninstall"); operationErr != nil {
			return operationErr
		}
	}
	for _, object := range custom {
		resource := "issuers"
		if object.GetKind() == "IPAddressPool" {
			resource = "ipaddresspools"
		}
		if object.GetKind() == "L2Advertisement" {
			resource = "l2advertisements"
		}
		gvr := object.GroupVersionKind().GroupVersion().WithResource(resource)
		if err = c.Dynamic.Resource(gvr).Namespace(object.GetNamespace()).Delete(ctx, object.GetName(), registryDeleteOptions(object)); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	if len(custom) > 0 {
		return ErrRegistryPending
	}
	// Remove controllers before their RBAC and definitions. A retry rechecks users.
	for _, kind := range []string{"Deployment", "DaemonSet", "Service", "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration", "CustomResourceDefinition", "IngressClass", "StorageClass", "ConfigMap", "Secret", "ServiceAccount", "RoleBinding", "Role", "ClusterRoleBinding", "ClusterRole", "PriorityClass", "NetworkPolicy"} {
		for _, object := range objects {
			if object.GetKind() != kind {
				continue
			}
			if err = c.bootstrapAPI(object).Delete(ctx, object.GetName(), registryDeleteOptions(object)); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	for _, class := range classes.Items {
		if managedClasses[class.Name] {
			if err = c.Kube.StorageV1().StorageClasses().Delete(ctx, class.Name, registryDeleteOptions(&class)); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	// Longhorn's successful uninstall Job is no longer needed once RBAC is gone.
	jobs, err := c.Kube.BatchV1().Jobs(bootstrapNamespace).List(ctx, metav1.ListOptions{LabelSelector: bootstrapOwner + "=" + owner})
	if err != nil {
		return err
	}
	for _, job := range jobs.Items {
		if strings.HasPrefix(job.Name, "longhorn-") {
			if err = c.Kube.BatchV1().Jobs(bootstrapNamespace).Delete(ctx, job.Name, registryDeleteOptions(&job)); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	if len(objects) > 0 {
		return ErrRegistryPending
	}
	return nil
}

func (c *Client) runLonghornHook(ctx context.Context, owner, kind string) error {
	bundle, err := bootstrapObjects("longhorn-"+kind+".yaml", owner)
	if err != nil {
		return err
	}
	want := bundle[0]
	if kind == "post-upgrade" {
		digest := sha256.Sum256([]byte(want.GetAnnotations()[bootstrapBundle]))
		want.SetName(fmt.Sprintf("%s-%x", want.GetName(), digest[:6]))
	}
	api := c.bootstrapAPI(want)
	current, err := api.Get(ctx, want.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = api.Create(ctx, want, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		return ErrRegistryPending
	}
	if err != nil {
		return err
	}
	if operationErr := bootstrapOwned(current, want); operationErr != nil {
		return operationErr
	}
	succeeded, _, statusErr := unstructured.NestedInt64(current.Object, "status", "succeeded")
	if statusErr != nil {
		return statusErr
	}
	if succeeded > 0 {
		return nil
	}
	failed, _, failureErr := unstructured.NestedInt64(current.Object, "status", "failed")
	if failureErr != nil {
		return failureErr
	}
	if failed > 0 {
		return errors.New("longhorn lifecycle job failed; inspect its logs before retrying")
	}
	return ErrRegistryPending
}
