package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/version"
)

// UpgradeManagedBootstrap installs only the versions bundled with Loomplane.
// Existing foreign installations and major/minor migrations remain untouched.
func (c *Client) UpgradeManagedBootstrap(ctx context.Context) error {
	system, err := c.Kube.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if system.UID == "" {
		return ErrOwned
	}
	files := []string{"cert-manager.yaml", "traefik.yaml"}
	if driver := c.Config.Details["registry_storage"]; driver == "local-path" || driver == "longhorn" {
		files = append(files, driver+".yaml")
	}
	if c.Config.Details["registry_network"] == "metallb" {
		files = append(files, "metallb.yaml")
	}
	var updates []*unstructured.Unstructured
	for _, file := range files {
		objects, loadErr := bootstrapObjects(file, string(system.UID))
		if loadErr != nil {
			return loadErr
		}
		if file == "traefik.yaml" && c.Config.Details["registry_network"] == "node" {
			node, address, nodeErr := c.nodeIngress(ctx)
			if nodeErr != nil {
				return nodeErr
			}
			if configureErr := configureNodeIngress(objects, node, address); configureErr != nil {
				return configureErr
			}
		}
		for _, want := range objects {
			old, getErr := c.bootstrapAPI(want).Get(ctx, want.GetName(), metav1.GetOptions{})
			if apierrors.IsNotFound(getErr) {
				continue
			}
			if getErr != nil {
				return getErr
			}
			if old.GetLabels()[bootstrapOwner] != string(system.UID) {
				continue
			}
			if old.GetDeletionTimestamp() != nil {
				return ErrOwned
			}
			previous := old.GetAnnotations()[bootstrapBundle]
			target := want.GetAnnotations()[bootstrapBundle]
			if previous == target {
				continue
			}
			if !compatibleBootstrapUpgrade(previous, target) {
				return fmt.Errorf("%w: %s needs a reviewed migration from %s to %s", ErrInvalid, want.GetName(), previous, target)
			}
			gvr := bootstrapResources[want.GetKind()]
			allowed, permissionErr := c.allowedIn(ctx, want.GetNamespace(), gvr.Group, gvr.Resource, "update")
			if permissionErr != nil {
				return permissionErr
			}
			if !allowed {
				return errors.New("controller upgrade needs update permissions")
			}
			if mergeErr := preserveBootstrapRuntime(want, old); mergeErr != nil {
				return mergeErr
			}
			updates = append(updates, want)
		}
	}
	for _, object := range updates {
		if _, err = c.bootstrapAPI(object).Update(ctx, object, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	if operationErr := c.BootstrapManagedBuild(ctx); operationErr != nil {
		return operationErr
	}
	if c.Config.Details["registry_storage"] == "longhorn" {
		return c.runLonghornHook(ctx, string(system.UID), "post-upgrade")
	}
	return nil
}
func compatibleBootstrapUpgrade(previous, target string) bool {
	// Bundle revisions may change without an upstream version change. Only
	// patch upgrades within the same upstream major/minor are automatic.
	split := func(value string) (string, *version.Version) {
		base, _, ok := strings.Cut(value, "-r")
		if !ok {
			return "", nil
		}
		index := strings.LastIndex(base, "-")
		if index < 0 {
			return "", nil
		}
		parsed, err := version.ParseGeneric(strings.TrimPrefix(base[index+1:], "v"))
		if err != nil {
			return "", nil
		}
		return base[:index], parsed
	}
	oldName, old := split(previous)
	name, next := split(target)
	return old != nil && next != nil && oldName == name && old.Major() == next.Major() && old.Minor() == next.Minor() && !next.LessThan(old)
}
func preserveBootstrapRuntime(want, old *unstructured.Unstructured) error {
	annotations := maps.Clone(old.GetAnnotations())
	if annotations == nil {
		annotations = map[string]string{}
	}
	maps.Copy(annotations, want.GetAnnotations())
	want.SetAnnotations(annotations)
	objectLabels := maps.Clone(old.GetLabels())
	if objectLabels == nil {
		objectLabels = map[string]string{}
	}
	maps.Copy(objectLabels, want.GetLabels())
	want.SetLabels(objectLabels)
	want.SetUID(old.GetUID())
	want.SetResourceVersion(old.GetResourceVersion())
	want.SetFinalizers(old.GetFinalizers())
	if status, ok := old.Object["status"]; ok {
		want.Object["status"] = status
	}
	if want.GetKind() == "Service" {
		for _, field := range []string{"clusterIP", "clusterIPs", "ipFamilies", "ipFamilyPolicy", "healthCheckNodePort", "allocateLoadBalancerNodePorts", "loadBalancerClass"} {
			value, found, err := unstructured.NestedFieldCopy(old.Object, "spec", field)
			if err != nil {
				return err
			}
			if found {
				if operationErr := unstructured.SetNestedField(want.Object, value, "spec", field); operationErr != nil {
					return operationErr
				}
			}
		}
		ports, _, err := unstructured.NestedSlice(want.Object, "spec", "ports")
		if err != nil {
			return err
		}
		existing, _, err := unstructured.NestedSlice(old.Object, "spec", "ports")
		if err != nil {
			return err
		}
		for _, p := range ports {
			port, portOK := p.(map[string]any)
			if !portOK {
				return ErrInvalid
			}
			for _, o := range existing {
				prior, priorOK := o.(map[string]any)
				if !priorOK {
					return ErrInvalid
				}
				if prior["name"] == port["name"] {
					if n, ok := prior["nodePort"]; ok {
						port["nodePort"] = n
					}
				}
			}
		}
		if operationErr := unstructured.SetNestedSlice(want.Object, ports, "spec", "ports"); operationErr != nil {
			return operationErr
		}
	}
	if want.GetKind() == "Secret" {
		want.Object["data"] = old.Object["data"]
	}
	if strings.HasSuffix(want.GetKind(), "WebhookConfiguration") {
		hooks, _, err := unstructured.NestedSlice(want.Object, "webhooks")
		if err != nil {
			return err
		}
		existing, _, err := unstructured.NestedSlice(old.Object, "webhooks")
		if err != nil {
			return err
		}
		for _, h := range hooks {
			hook, hookOK := h.(map[string]any)
			if !hookOK {
				return ErrInvalid
			}
			for _, o := range existing {
				prior, priorOK := o.(map[string]any)
				if !priorOK {
					return ErrInvalid
				}
				if prior["name"] == hook["name"] {
					bundle, found, getErr := unstructured.NestedFieldCopy(prior, "clientConfig", "caBundle")
					if getErr != nil {
						return getErr
					}
					if found {
						if operationErr := unstructured.SetNestedField(hook, bundle, "clientConfig", "caBundle"); operationErr != nil {
							return operationErr
						}
					}
				}
			}
		}
		if operationErr := unstructured.SetNestedSlice(want.Object, hooks, "webhooks"); operationErr != nil {
			return operationErr
		}
	}
	return nil
}
