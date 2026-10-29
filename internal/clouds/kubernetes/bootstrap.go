package kubernetes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/utils/ptr"
)

const bootstrapNamespace = "loomplane-system"
const bootstrapOwner = "loomplane.bootstrap.cluster"
const bootstrapBundle = "loomplane.bootstrap.bundle"
const bootstrapIngress = "loomplane"

//go:embed bootstrap-data/*
var bootstrapData embed.FS

var bootstrapVersions = map[string]string{"longhorn-uninstall.yaml": "longhorn-v1.13.0-r1", "longhorn-post-upgrade.yaml": "longhorn-v1.13.0-r1", "cert-manager.yaml": "cert-manager-v1.21.3-r1", "traefik.yaml": "traefik-42.1.3-r1", "local-path.yaml": "local-path-v0.0.37-r1", "longhorn.yaml": "longhorn-v1.13.0-r1", "metallb.yaml": "metallb-v0.16.1-r1"}

var bootstrapResources = map[string]schema.GroupVersionResource{
	"Job":                            {Group: "batch", Version: "v1", Resource: "jobs"},
	"Namespace":                      {Version: "v1", Resource: "namespaces"},
	"ServiceAccount":                 {Version: "v1", Resource: "serviceaccounts"},
	"Service":                        {Version: "v1", Resource: "services"},
	"Deployment":                     {Group: "apps", Version: "v1", Resource: "deployments"},
	"Role":                           {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"},
	"RoleBinding":                    {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"},
	"ClusterRole":                    {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"},
	"ClusterRoleBinding":             {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"},
	"CustomResourceDefinition":       {Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"},
	"MutatingWebhookConfiguration":   {Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"},
	"ValidatingWebhookConfiguration": {Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"},
	"IngressClass":                   {Group: "networking.k8s.io", Version: "v1", Resource: "ingressclasses"},
	"StorageClass":                   {Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"},
	"ConfigMap":                      {Version: "v1", Resource: "configmaps"},
	"Secret":                         {Version: "v1", Resource: "secrets"},
	"DaemonSet":                      {Group: "apps", Version: "v1", Resource: "daemonsets"},
	"PriorityClass":                  {Group: "scheduling.k8s.io", Version: "v1", Resource: "priorityclasses"},
	"NetworkPolicy":                  {Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"},
	"IPAddressPool":                  {Group: "metallb.io", Version: "v1beta1", Resource: "ipaddresspools"},
	"L2Advertisement":                {Group: "metallb.io", Version: "v1beta1", Resource: "l2advertisements"},
}

// BootstrapManagedBuild installs missing prerequisites only when a connection
// is saved. Check never calls it. Existing installations are not upgraded or
// adopted, and deleting a connection never removes shared cluster resources.
func (c *Client) BootstrapManagedBuild(ctx context.Context) error {
	if c.Config.Details["registry_hostname"] == "" || c.Config.Details["registry_setup"] == "existing" {
		return nil
	}
	_, namespaceErr := c.Kube.CoreV1().Namespaces().Get(ctx, c.Config.Namespace, metav1.GetOptions{})
	if namespaceErr != nil && !apierrors.IsNotFound(namespaceErr) {
		return namespaceErr
	}
	system, err := c.Kube.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if system.UID == "" {
		return ErrOwned
	}
	owner := string(system.UID)
	architecture, err := c.buildArchitecture(ctx)
	if err != nil {
		return err
	}
	storage, storageObjects, err := c.setupStorage(ctx, owner)
	if err != nil {
		return err
	}
	ingress, err := c.buildIngressClass(ctx)
	if err != nil {
		return err
	}
	installIngress := ingress == ""
	if ingress == bootstrapIngress {
		class, getErr := c.Kube.NetworkingV1().IngressClasses().Get(ctx, ingress, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		installIngress = class.Labels[bootstrapOwner] == owner
	}
	if !installIngress && (c.Config.Details["registry_network"] == "node" || c.Config.Details["registry_network"] == "metallb") {
		return errors.New("node and MetalLB setup require a Loomplane-owned ingress class")
	}
	certObjects, err := bootstrapObjects("cert-manager.yaml", owner)
	if err != nil {
		return err
	}
	installCert, err := c.needsCertManager(ctx, certObjects, owner)
	if err != nil {
		return err
	}
	var objects []*unstructured.Unstructured
	if apierrors.IsNotFound(namespaceErr) {
		if strings.HasPrefix(c.Config.Namespace, "kube-") || c.Config.Namespace == bootstrapNamespace {
			return errors.New("select an application namespace for managed builds")
		}
		objects = append(objects, bootstrapNS(c.Config.Namespace, owner))
	}
	if installCert || installIngress || len(storageObjects) > 0 {
		objects = append(objects, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": bootstrapNamespace, "labels": map[string]any{bootstrapOwner: owner}, "annotations": map[string]any{bootstrapBundle: "namespace-v1"}}}})
	}
	objects = append(objects, storageObjects...)
	if installCert {
		objects = append(objects, certObjects...)
	}
	if installIngress {
		bundle, e := bootstrapObjects("traefik.yaml", owner)
		if e != nil {
			return e
		}
		if c.Config.Details["registry_network"] == "node" {
			node, address, nodeErr := c.nodeIngress(ctx)
			if nodeErr != nil {
				return nodeErr
			}
			if configureErr := configureNodeIngress(bundle, node, address); configureErr != nil {
				return configureErr
			}
			c.Config.Details["ingress_address"] = address
			delete(c.Config.Details, "registry_ingress_address")
		}
		for _, object := range bundle {
			annotations := object.GetAnnotations()
			mode := c.Config.Details["registry_network"]
			if mode == "" {
				mode = "load-balancer"
			}
			annotations["loomplane.bootstrap.network"] = mode
			object.SetAnnotations(annotations)
		}
		if c.Config.Details["registry_network"] == "metallb" {
			metallb, loadErr := c.metallbSetup(ctx, owner, bundle)
			if loadErr != nil {
				return loadErr
			}
			objects = append(objects, metallb...)
		}
		objects = append(objects, bundle...)
		ingress = bootstrapIngress
	}
	if storage.ReclaimPolicy == nil || *storage.ReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		original := storage
		storage = storage.DeepCopy()
		storage.ObjectMeta = metav1.ObjectMeta{Name: retainedClassName(string(original.UID)), Labels: map[string]string{bootstrapOwner: owner}, Annotations: map[string]string{bootstrapBundle: "retained-storage-v1", "loomplane.bootstrap.storage-source": string(original.UID)}}
		storage.TypeMeta = metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "StorageClass"}
		storage.ReclaimPolicy = ptr.To(corev1.PersistentVolumeReclaimRetain)
		object, e := runtime.DefaultUnstructuredConverter.ToUnstructured(storage)
		if e != nil {
			return e
		}
		objects = append(objects, &unstructured.Unstructured{Object: object})
	}
	// Reject known permission gaps and ownership conflicts before writing.
	// Admission or connectivity failures can still leave resources for a retry.
	var missing []*unstructured.Unstructured
	for _, object := range objects {
		api := c.bootstrapAPI(object)
		old, e := api.Get(ctx, object.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(e) {
			gvr := bootstrapResources[object.GetKind()]
			allowed, permissionErr := c.allowedIn(ctx, object.GetNamespace(), gvr.Group, gvr.Resource, "create")
			if permissionErr != nil {
				return permissionErr
			}
			if !allowed {
				return fmt.Errorf("cluster setup needs create permission for %s in %q", gvr.Resource, object.GetNamespace())
			}
			missing = append(missing, object)
		} else if e != nil {
			return e
		} else if ownerErr := bootstrapOwned(old, object); ownerErr != nil {
			return ownerErr
		}
	}
	for _, object := range missing {
		api := c.bootstrapAPI(object)
		if e := createBootstrapObject(ctx, api, object); apierrors.IsAlreadyExists(e) {
			old, getErr := api.Get(ctx, object.GetName(), metav1.GetOptions{})
			if getErr != nil {
				return getErr
			}
			if ownerErr := bootstrapOwned(old, object); ownerErr != nil {
				return ownerErr
			}
		} else if e != nil {
			return fmt.Errorf("install %s %s: %w", object.GetKind(), object.GetName(), e)
		}
	}
	c.Config.Details["architecture"] = architecture
	c.Config.Details["storage_class"] = storage.Name
	c.Config.Details["ingress_class"] = ingress
	c.Config.Details["registry_setup"] = "automatic"
	for key, managed := range map[string]bool{"registry_cert_manager": installCert, "registry_ingress": installIngress} {
		c.Config.Details[key] = "existing"
		if managed {
			c.Config.Details[key] = "managed"
		}
	}
	return nil
}

func bootstrapNS(name, owner string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": name, "labels": map[string]any{bootstrapOwner: owner}, "annotations": map[string]any{bootstrapBundle: "namespace-v1"}}}}
}

func bootstrapOwned(old, want *unstructured.Unstructured) error {
	if old.GetDeletionTimestamp() != nil || old.GetLabels()[bootstrapOwner] != want.GetLabels()[bootstrapOwner] || old.GetAnnotations()[bootstrapBundle] != want.GetAnnotations()[bootstrapBundle] {
		return fmt.Errorf("%w: cluster setup will not replace %s %s", ErrOwned, want.GetKind(), want.GetName())
	}
	if desired := want.GetAnnotations()["loomplane.bootstrap.network"]; desired != "" {
		current := old.GetAnnotations()["loomplane.bootstrap.network"]
		if current == "" {
			current = "load-balancer"
		}
		if current != desired {
			return errors.New("ingress network mode changed; remove unused managed ingress before selecting another mode")
		}
	}
	return nil
}

func createBootstrapObject(ctx context.Context, api dynamic.ResourceInterface, object *unstructured.Unstructured) error {
	for {
		_, err := api.Create(ctx, object, metav1.CreateOptions{})
		if object.GetKind() != "IPAddressPool" && object.GetKind() != "L2Advertisement" {
			return err
		}
		if !apierrors.IsInternalError(err) && !apierrors.IsServiceUnavailable(err) && !apierrors.IsNotFound(err) && !apierrors.IsTimeout(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (c *Client) bootstrapAPI(object *unstructured.Unstructured) dynamic.ResourceInterface {
	api := c.Dynamic.Resource(bootstrapResources[object.GetKind()])
	if object.GetNamespace() != "" {
		return api.Namespace(object.GetNamespace())
	}
	return api
}

func bootstrapObjects(file, owner string) ([]*unstructured.Unstructured, error) {
	version, ok := bootstrapVersions[file]
	if !ok {
		return nil, errors.New("unknown cluster setup bundle")
	}
	data, err := bootstrapData.ReadFile("bootstrap-data/" + file)
	if err != nil {
		return nil, err
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var out []*unstructured.Unstructured
	for {
		object := &unstructured.Unstructured{}
		if err = decoder.Decode(&object.Object); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		if len(object.Object) == 0 {
			continue
		}
		gvr, ok := bootstrapResources[object.GetKind()]
		if !ok || object.GetAPIVersion() != gvr.GroupVersion().String() || object.GetName() == "" || (object.GetNamespace() != "" && object.GetNamespace() != bootstrapNamespace) {
			return nil, errors.New("invalid embedded cluster setup resource")
		}
		labels := maps.Clone(object.GetLabels())
		if labels == nil {
			labels = map[string]string{}
		}
		labels[bootstrapOwner] = owner
		delete(labels, "app.kubernetes.io/managed-by")
		object.SetLabels(labels)
		annotations := maps.Clone(object.GetAnnotations())
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[bootstrapBundle] = version
		object.SetAnnotations(annotations)
		out = append(out, object)
	}
	if len(out) == 0 {
		return nil, errors.New("empty cluster setup bundle")
	}
	return out, nil
}

func (c *Client) needsCertManager(ctx context.Context, objects []*unstructured.Unstructured, owner string) (bool, error) {
	found, owned := 0, 0
	for _, object := range objects {
		if object.GetKind() != "CustomResourceDefinition" {
			continue
		}
		old, err := c.bootstrapAPI(object).Get(ctx, object.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		found++
		if old.GetLabels()[bootstrapOwner] == owner {
			owned++
		}
	}
	if found == 0 || found == owned {
		return true, nil
	}
	if found != 6 || owned != 0 {
		return false, errors.New("cert-manager is partially installed; finish or repair its existing installation before automatic setup")
	}
	return false, nil
}

func (c *Client) buildArchitecture(ctx context.Context) (string, error) {
	if architecture := c.Config.Details["architecture"]; architecture != "" {
		return architecture, nil
	}
	nodes, err := c.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}
	available := map[string]bool{}
	for _, node := range nodes.Items {
		if node.Spec.Unschedulable || node.Labels["kubernetes.io/os"] != "linux" || slices.ContainsFunc(node.Spec.Taints, func(taint corev1.Taint) bool {
			return taint.Effect == corev1.TaintEffectNoSchedule || taint.Effect == corev1.TaintEffectNoExecute
		}) {
			continue
		}
		if slices.ContainsFunc(node.Status.Conditions, func(condition corev1.NodeCondition) bool {
			return condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue
		}) {
			available[node.Labels["kubernetes.io/arch"]] = true
		}
	}
	for _, architecture := range []string{"amd64", "arm64"} {
		if available[architecture] {
			return architecture, nil
		}
	}
	return "", errors.New("cluster setup needs a ready, schedulable Linux amd64 or arm64 node")
}

func (c *Client) buildStorage(ctx context.Context) (*storagev1.StorageClass, error) {
	var selected *storagev1.StorageClass
	if name := c.Config.Details["storage_class"]; name != "" {
		class, err := c.Kube.StorageV1().StorageClasses().Get(ctx, name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err) && strings.HasPrefix(name, "lp-registry-retained-"):
			classes, listErr := c.Kube.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
			if listErr != nil {
				return nil, listErr
			}
			for i := range classes.Items {
				if retainedClassName(string(classes.Items[i].UID)) == name {
					selected = &classes.Items[i]
					break
				}
			}
			if selected == nil {
				return nil, errors.New("the retained class and its original storage source are gone; select storage_class again")
			}
		case err != nil:
			return nil, err
		default:
			selected = class
		}
	} else {
		classes, err := c.Kube.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for i := range classes.Items {
			class := &classes.Items[i]
			if class.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" || class.Annotations["storageclass.beta.kubernetes.io/is-default-class"] == "true" {
				if selected != nil {
					return nil, errors.New("multiple default storage classes exist; select storage_class")
				}
				selected = class
			}
		}
		if selected == nil && len(classes.Items) == 1 {
			selected = &classes.Items[0]
		}
	}
	if selected == nil {
		return nil, errors.New("select a storage_class backed by an installed storage provisioner; Loomplane cannot infer a storage driver")
	}
	if selected.UID == "" || selected.Provisioner == "" || selected.Provisioner == "kubernetes.io/no-provisioner" || selected.VolumeBindingMode == nil || (*selected.VolumeBindingMode != storagev1.VolumeBindingImmediate && *selected.VolumeBindingMode != storagev1.VolumeBindingWaitForFirstConsumer) {
		return nil, errors.New("cluster setup needs a dynamic storage class with a declared binding mode")
	}
	return selected, nil
}

func retainedClassName(uid string) string {
	sum := sha256.Sum256([]byte(uid))
	return fmt.Sprintf("lp-registry-retained-%x", sum[:8])
}

func (c *Client) buildIngressClass(ctx context.Context) (string, error) {
	if name := c.Config.Details["ingress_class"]; name != "" {
		_, err := c.Kube.NetworkingV1().IngressClasses().Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) && name == bootstrapIngress && c.Config.Details["registry_ingress"] == "managed" {
			return "", nil
		}
		return name, err
	}
	classes, err := c.Kube.NetworkingV1().IngressClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}
	for _, class := range classes.Items {
		if class.Name == bootstrapIngress {
			return class.Name, nil
		}
	}
	var chosen string
	for _, class := range classes.Items {
		if class.Annotations["ingressclass.kubernetes.io/is-default-class"] == "true" {
			if chosen != "" {
				return "", errors.New("multiple default ingress classes exist; select ingress_class")
			}
			chosen = class.Name
		}
	}
	if chosen != "" {
		return chosen, nil
	}
	if len(classes.Items) == 1 {
		return classes.Items[0].Name, nil
	}
	if len(classes.Items) > 1 {
		return "", errors.New("multiple ingress classes exist; select ingress_class")
	}
	return "", nil
}
