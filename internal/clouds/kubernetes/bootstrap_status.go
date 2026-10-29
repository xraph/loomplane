package kubernetes

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// discoverManagedSetup observes controller readiness and load-balancer addresses.
// It never creates or patches cluster resources, including during Recheck.
func (c *Client) discoverManagedSetup(ctx context.Context, out *Result) error {
	if c.Config.Details["registry_hostname"] == "" {
		return nil
	}
	out.Details["registry_setup_status"] = "ready"
	missing := func(message string) {
		out.Missing = append(out.Missing, message)
		out.Details["registry_setup_status"] = "waiting"
	}
	keys := []string{"architecture", "storage_class"}
	if out.Details["registry_setup"] == "automatic" {
		keys = append(keys, "ingress_class")
	}
	for _, key := range keys {
		if out.Details[key] == "" {
			missing("cluster setup: save the connection to configure " + key)
		}
	}
	if out.Details["registry_setup"] == "automatic" {
		deployments, err := c.Kube.AppsV1().Deployments(bootstrapNamespace).List(ctx, metav1.ListOptions{LabelSelector: bootstrapOwner})
		if err != nil {
			return err
		}
		for _, deployment := range deployments.Items {
			if !deploymentReady(&deployment) {
				missing("cluster setup: waiting for " + deployment.Name)
			}
		}
		var expected []string
		if out.Details["registry_cert_manager"] == "managed" {
			expected = append(expected, "loomplane-cert-manager", "loomplane-cert-manager-cainjector", "loomplane-cert-manager-webhook")
		}
		if out.Details["registry_ingress"] == "managed" {
			expected = append(expected, "loomplane-ingress")
		}
		for _, name := range expected {
			found := false
			for _, deployment := range deployments.Items {
				if deployment.Name == name {
					found = true
					break
				}
			}
			if !found {
				missing("cluster setup: " + name + " is missing; save the connection again to repair setup")
			}
		}
	}
	if out.Details["registry_ingress_address"] == "automatic" {
		delete(out.Details, "ingress_address")
	}
	if out.Details["ingress_address"] != "" {
		return nil
	}
	class := out.Details["ingress_class"]
	if class == "" {
		return nil
	}
	addresses := map[string]bool{}
	collect := func(ingress []corev1.LoadBalancerIngress) {
		for _, item := range ingress {
			value := item.IP
			if value == "" {
				value = item.Hostname
			}
			if normalized, ok := ingressAddress(value); ok {
				addresses[normalized] = true
			}
		}
	}
	if class == bootstrapIngress {
		service, err := c.Kube.CoreV1().Services(bootstrapNamespace).Get(ctx, "loomplane-ingress", metav1.GetOptions{})
		if err == nil && service.Labels[bootstrapOwner] != "" {
			collect(service.Status.LoadBalancer.Ingress)
			if len(addresses) == 0 {
				missing("cluster setup: waiting for the managed ingress load-balancer address; clusters without a load-balancer controller need ingress_address")
				return nil
			}
		} else if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	if len(addresses) == 0 {
		// An existing Ingress gives a controller-independent endpoint. For a new
		// Traefik/ingress-nginx installation, inspect its public Service instead.
		ingresses, err := c.Kube.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		for _, ingress := range ingresses.Items {
			if ingress.Spec.IngressClassName != nil && *ingress.Spec.IngressClassName == class {
				for _, item := range ingress.Status.LoadBalancer.Ingress {
					collect([]corev1.LoadBalancerIngress{{IP: item.IP, Hostname: item.Hostname}})
				}
			}
		}
		if len(addresses) == 0 {
			selected, err := c.Kube.NetworkingV1().IngressClasses().Get(ctx, class, metav1.GetOptions{})
			if err != nil {
				return err
			}
			app := ""
			switch selected.Spec.Controller {
			case "traefik.io/ingress-controller":
				app = "traefik"
			case "k8s.io/ingress-nginx":
				app = "ingress-nginx"
			}
			if app != "" {
				services, err := c.Kube.CoreV1().Services("").List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=" + app})
				if err != nil {
					return err
				}
				for _, service := range services.Items {
					if service.Spec.Type == corev1.ServiceTypeLoadBalancer && slices.ContainsFunc(service.Spec.Ports, func(port corev1.ServicePort) bool { return port.Port == 443 }) {
						collect(service.Status.LoadBalancer.Ingress)
					}
				}
			}
		}
	}
	if len(addresses) == 1 {
		for address := range addresses {
			out.Details["ingress_address"] = address
			out.Details["registry_ingress_address"] = "automatic"
		}
		return nil
	}
	if len(addresses) > 1 {
		missing("cluster setup: multiple ingress addresses found; set ingress_address")
	} else {
		missing(fmt.Sprintf("cluster setup: waiting for ingress class %s to receive a load-balancer address; clusters without a load-balancer controller need ingress_address", class))
	}
	return nil
}
