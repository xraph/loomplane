package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/utils/ptr"
)

func registrySetupOptions(settings, details map[string]string) error {
	for key, choices := range map[string][]string{
		"registry_storage":     {"existing", "local-path", "longhorn"},
		"registry_network":     {"load-balancer", "node", "metallb"},
		"registry_dns":         {"automatic", "manual"},
		"registry_maintenance": {"automatic", "manual"},
	} {
		if value := strings.TrimSpace(settings[key]); value != "" {
			if !slices.Contains(choices, value) {
				return fmt.Errorf("%w: unsupported %s", ErrInvalid, key)
			}
			details[key] = value
		}
	}
	for _, key := range []string{"ingress_node", "load_balancer_addresses"} {
		if value := strings.TrimSpace(settings[key]); value != "" {
			details[key] = value
		}
	}
	if details["ingress_node"] != "" && len(validation.IsDNS1123Subdomain(details["ingress_node"])) != 0 {
		return fmt.Errorf("%w: ingress_node must name a node", ErrInvalid)
	}
	if details["registry_network"] == "metallb" {
		if _, err := loadBalancerRanges(details["load_balancer_addresses"]); err != nil {
			return err
		}
	}
	return nil
}

func loadBalancerRanges(value string) ([]string, error) {
	if value == "" {
		return nil, fmt.Errorf("%w: MetalLB needs an unused load_balancer_addresses pool on the node network", ErrInvalid)
	}
	var result []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if prefix, err := netip.ParsePrefix(item); err == nil {
			if !prefix.Addr().IsGlobalUnicast() || prefix.Bits() == 0 {
				return nil, ErrInvalid
			}
		} else if start, end, ok := strings.Cut(item, "-"); ok {
			a, e1 := netip.ParseAddr(start)
			b, e2 := netip.ParseAddr(end)
			if e1 != nil || e2 != nil || !a.IsGlobalUnicast() || !b.IsGlobalUnicast() || a.BitLen() != b.BitLen() || a.Compare(b) > 0 {
				return nil, ErrInvalid
			}
		} else {
			return nil, fmt.Errorf("%w: use CIDRs or IP ranges for load_balancer_addresses", ErrInvalid)
		}
		result = append(result, item)
	}
	return result, nil
}

func (c *Client) setupStorage(ctx context.Context, owner string) (*storagev1.StorageClass, []*unstructured.Unstructured, error) {
	driver := c.Config.Details["registry_storage"]
	if driver != "local-path" && driver != "longhorn" {
		s, e := c.buildStorage(ctx)
		return s, nil, e
	}
	name := "lp-local-retained"
	if driver == "longhorn" {
		name = "lp-longhorn-retained"
	}
	if configured := c.Config.Details["storage_class"]; configured != "" && configured != name {
		return nil, nil, errors.New("storage_class conflicts with the selected managed storage driver")
	}
	if driver == "longhorn" {
		info, err := c.Kube.Discovery().ServerVersion()
		if err != nil {
			return nil, nil, err
		}
		current, err := version.ParseGeneric(info.GitVersion)
		if err != nil || current.LessThan(version.MustParseGeneric("1.34.0")) {
			return nil, nil, errors.New("bundled Longhorn requires Kubernetes 1.34 or later")
		}
		nodes, err := c.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, nil, err
		}
		ready := 0
		for _, n := range nodes.Items {
			if setupNodeReady(n) {
				ready++
			}
		}
		if ready < 3 {
			return nil, nil, errors.New("replicated Longhorn storage needs at least three ready schedulable Linux nodes")
		}
	}
	objects, err := bootstrapObjects(driver+".yaml", owner)
	if err != nil {
		return nil, nil, err
	}
	class := &storagev1.StorageClass{TypeMeta: metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "StorageClass"}, ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{bootstrapOwner: owner}, Annotations: map[string]string{bootstrapBundle: bootstrapVersions[driver+".yaml"]}}, ReclaimPolicy: ptr.To(corev1.PersistentVolumeReclaimRetain), VolumeBindingMode: ptr.To(storagev1.VolumeBindingWaitForFirstConsumer)}
	if driver == "local-path" {
		class.Provisioner = "loomplane.io/local-path"
	} else {
		class.Provisioner = "driver.longhorn.io"
		class.AllowVolumeExpansion = ptr.To(true)
		class.Parameters = map[string]string{"numberOfReplicas": "3", "staleReplicaTimeout": "30", "fsType": "ext4"}
	}
	// The local-path bundle already includes this class.
	if driver == "longhorn" {
		object, e := runtime.DefaultUnstructuredConverter.ToUnstructured(class)
		if e != nil {
			return nil, nil, e
		}
		objects = append(objects, &unstructured.Unstructured{Object: object})
	}
	return class, objects, nil
}

func setupNodeReady(node corev1.Node) bool {
	return !node.Spec.Unschedulable && node.Labels["kubernetes.io/os"] == "linux" && !slices.ContainsFunc(node.Spec.Taints, func(t corev1.Taint) bool {
		return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
	}) && slices.ContainsFunc(node.Status.Conditions, func(c corev1.NodeCondition) bool {
		return c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue
	})
}

func (c *Client) nodeIngress(ctx context.Context) (hostname, endpoint string, err error) {
	nodes, err := c.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", "", err
	}
	var selected *corev1.Node
	for i := range nodes.Items {
		n := &nodes.Items[i]
		if !setupNodeReady(*n) {
			continue
		}
		if wanted := c.Config.Details["ingress_node"]; wanted != "" && wanted != n.Name {
			continue
		}
		if selected != nil {
			return "", "", errors.New("choose ingress_node when several nodes can serve ingress")
		}
		selected = n
	}
	if selected == nil {
		return "", "", errors.New("no ready node matches ingress_node")
	}
	address := c.Config.Details["ingress_address"]
	if address == "" {
		for _, kind := range []corev1.NodeAddressType{corev1.NodeExternalIP, corev1.NodeInternalIP} {
			for _, a := range selected.Status.Addresses {
				if a.Type == kind && net.ParseIP(a.Address) != nil {
					address = a.Address
					break
				}
			}
			if address != "" {
				break
			}
		}
	}
	if address == "" {
		return "", "", errors.New("selected ingress node has no IP address; set ingress_address")
	}
	if selected.Labels["kubernetes.io/hostname"] == "" {
		return "", "", errors.New("ingress node has no hostname label")
	}
	return selected.Labels["kubernetes.io/hostname"], address, nil
}

func configureNodeIngress(objects []*unstructured.Unstructured, node, address string) error {
	for _, o := range objects {
		switch o.GetKind() {
		case "Deployment":
			if operationErr := unstructured.SetNestedField(o.Object, "Recreate", "spec", "strategy", "type"); operationErr != nil {
				return operationErr
			}
			unstructured.RemoveNestedField(o.Object, "spec", "strategy", "rollingUpdate")
			if operationErr := unstructured.SetNestedStringMap(o.Object, map[string]string{"kubernetes.io/hostname": node}, "spec", "template", "spec", "nodeSelector"); operationErr != nil {
				return operationErr
			}
			containers, _, err := unstructured.NestedSlice(o.Object, "spec", "template", "spec", "containers")
			if err != nil {
				return err
			}
			for _, v := range containers {
				container, ok := v.(map[string]any)
				if !ok {
					return ErrInvalid
				}
				args, ok := container["args"].([]any)
				if !ok {
					return ErrInvalid
				}
				var out []any
				for _, arg := range args {
					value, stringOK := arg.(string)
					if !stringOK {
						return ErrInvalid
					}
					if !strings.HasPrefix(value, "--providers.kubernetesingress.ingressendpoint.publishedservice=") {
						out = append(out, arg)
					}
				}
				key := "ip"
				if net.ParseIP(address) == nil {
					key = "hostname"
				}
				container["args"] = append(out, "--providers.kubernetesingress.ingressendpoint."+key+"="+address)
				ports, ok := container["ports"].([]any)
				if !ok {
					return ErrInvalid
				}
				for _, p := range ports {
					port, ok := p.(map[string]any)
					if !ok {
						return ErrInvalid
					}
					if port["name"] == "web" {
						port["hostPort"] = int64(80)
					}
					if port["name"] == "websecure" {
						port["hostPort"] = int64(443)
					}
				}
			}
			if operationErr := unstructured.SetNestedSlice(o.Object, containers, "spec", "template", "spec", "containers"); operationErr != nil {
				return operationErr
			}
		case "Service":
			if operationErr := unstructured.SetNestedField(o.Object, "ClusterIP", "spec", "type"); operationErr != nil {
				return operationErr
			}
		}
	}
	return nil
}

func (c *Client) metallbSetup(ctx context.Context, owner string, ingress []*unstructured.Unstructured) ([]*unstructured.Unstructured, error) {
	ranges, err := loadBalancerRanges(c.Config.Details["load_balancer_addresses"])
	if err != nil {
		return nil, err
	}
	nodes, err := c.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, node := range nodes.Items {
		for _, address := range node.Status.Addresses {
			ip, e := netip.ParseAddr(address.Address)
			if e != nil {
				continue
			}
			for _, pool := range ranges {
				if poolContains(pool, ip) {
					return nil, errors.New("load balancer pool overlaps a node address")
				}
			}
		}
	}
	for _, node := range nodes.Items {
		for _, cidr := range node.Spec.PodCIDRs {
			for _, pool := range ranges {
				if poolsOverlap(pool, cidr) {
					return nil, errors.New("load balancer pool overlaps a pod network")
				}
			}
		}
	}
	services, serviceErr := c.Kube.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if serviceErr != nil {
		return nil, serviceErr
	}
	for _, service := range services.Items {
		if service.Namespace == bootstrapNamespace && service.Name == "loomplane-ingress" && service.Labels[bootstrapOwner] == owner {
			continue
		}
		addresses := append([]string{}, service.Spec.ClusterIPs...)
		for _, address := range service.Status.LoadBalancer.Ingress {
			addresses = append(addresses, address.IP)
		}
		for _, address := range addresses {
			ip, parseErr := netip.ParseAddr(address)
			if parseErr != nil {
				continue
			}
			for _, pool := range ranges {
				if poolContains(pool, ip) {
					return nil, errors.New("load balancer pool overlaps an allocated service address")
				}
			}
		}
	}
	_, crdErr := c.Dynamic.Resource(bootstrapResources["CustomResourceDefinition"]).Get(ctx, "ipaddresspools.metallb.io", metav1.GetOptions{})
	if crdErr == nil {
		pools, listErr := c.Dynamic.Resource(bootstrapResources["IPAddressPool"]).List(ctx, metav1.ListOptions{})
		if listErr != nil {
			return nil, listErr
		}
		for _, pool := range pools.Items {
			if pool.GetNamespace() == bootstrapNamespace && pool.GetName() == "loomplane-registry" && pool.GetLabels()[bootstrapOwner] == owner {
				continue
			}
			addresses, _, getErr := unstructured.NestedStringSlice(pool.Object, "spec", "addresses")
			if getErr != nil {
				return nil, getErr
			}
			for _, address := range addresses {
				for _, candidate := range ranges {
					if poolsOverlap(candidate, address) {
						return nil, errors.New("load balancer pool overlaps an existing MetalLB pool")
					}
				}
			}
		}
	} else if !apierrors.IsNotFound(crdErr) {
		return nil, crdErr
	}
	objects, err := bootstrapObjects("metallb.yaml", owner)
	if err != nil {
		return nil, err
	}
	meta := func(name string) map[string]any {
		return map[string]any{"name": name, "namespace": bootstrapNamespace, "labels": map[string]any{bootstrapOwner: owner}, "annotations": map[string]any{bootstrapBundle: bootstrapVersions["metallb.yaml"]}}
	}
	values := make([]any, len(ranges))
	for i, value := range ranges {
		values[i] = value
	}
	objects = append(objects,
		&unstructured.Unstructured{Object: map[string]any{"apiVersion": "metallb.io/v1beta1", "kind": "IPAddressPool", "metadata": meta("loomplane-registry"), "spec": map[string]any{"addresses": values, "autoAssign": false}}},
		&unstructured.Unstructured{Object: map[string]any{"apiVersion": "metallb.io/v1beta1", "kind": "L2Advertisement", "metadata": meta("loomplane-registry"), "spec": map[string]any{"ipAddressPools": []any{"loomplane-registry"}}}},
	)
	if err := configureMetalLBIngress(ingress); err != nil {
		return nil, err
	}
	return objects, nil
}

func configureMetalLBIngress(objects []*unstructured.Unstructured) error {
	for _, object := range objects {
		if object.GetKind() != "Service" {
			continue
		}
		annotations := object.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations["metallb.io/address-pool"] = "loomplane-registry"
		object.SetAnnotations(annotations)
		if err := unstructured.SetNestedField(object.Object, "loomplane.io/metallb", "spec", "loadBalancerClass"); err != nil {
			return err
		}
	}
	return nil
}

func poolContains(pool string, ip netip.Addr) bool {
	if prefix, err := netip.ParsePrefix(pool); err == nil {
		return prefix.Contains(ip)
	}
	start, end, _ := strings.Cut(pool, "-")
	a, e1 := netip.ParseAddr(start)
	b, e2 := netip.ParseAddr(end)
	return e1 == nil && e2 == nil && a.BitLen() == ip.BitLen() && a.Compare(ip) <= 0 && b.Compare(ip) >= 0
}

func poolsOverlap(a, b string) bool {
	first := func(value string) netip.Addr {
		if prefix, err := netip.ParsePrefix(value); err == nil {
			return prefix.Masked().Addr()
		}
		start, _, _ := strings.Cut(value, "-")
		ip, err := netip.ParseAddr(start)
		if err != nil {
			return netip.Addr{}
		}
		return ip
	}
	return poolContains(a, first(b)) || poolContains(b, first(a))
}
