package kubernetes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/idna"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/discovery"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

const (
	// LabelRoute names the route that owns an Ingress, Certificate or TLS Secret.
	LabelRoute = "loomplane.route"
	// LabelRouteShared marks the namespace objects every route shares.
	LabelRouteShared = "loomplane.route.shared"
	// LabelZone names the zone whose DNS-01 Issuer and token Secret an object is.
	LabelZone = "loomplane.zone"
	// RouteDenyService answers blocked path prefixes. It has no endpoints.
	RouteDenyService = "lp-route-deny"
	HTTPIssuerName   = "lp-acme-http01"

	acmeAccountSecret    = "lp-acme-account"
	dnsTokenKey          = "api-token"
	defaultACMEDirectory = "https://acme-v02.api.letsencrypt.org/directory"
	sharedDeny           = "deny"
	sharedIssuer         = "issuer"
)

var (
	issuerGVR      = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "issuers"}
	certificateGVR = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	challengeGVR   = schema.GroupVersionResource{Group: "acme.cert-manager.io", Version: "v1", Resource: "challenges"}
)

// Every route permission is namespaced. The ingress address comes from the
// Ingress status, so the controller's own namespace is never read.
var routePermissions = []struct {
	group, resource string
	verbs           []string
}{
	{"networking.k8s.io", "ingresses", []string{"get", "list", "create", "update", "patch", "delete"}},
	{"cert-manager.io", "issuers", []string{"get", "list", "create", "update", "patch", "delete"}},
	{"cert-manager.io", "certificates", []string{"get", "list", "create", "update", "patch", "delete"}},
	{"acme.cert-manager.io", "orders", []string{"get", "list"}},
	{"acme.cert-manager.io", "challenges", []string{"get", "list"}},
	{"discovery.k8s.io", "endpointslices", []string{"get", "list"}},
	{"", "services", []string{"get", "create", "update", "patch", "delete"}},
	{"", "secrets", []string{"get", "create", "update", "patch", "delete"}},
}

type RoutePrerequisites struct {
	Ready   bool
	Missing []string
}

type RouteIngress struct {
	RouteID, OrgID, ProjectID, EnvironmentID, Hostname string
	ServiceName                                        string
	ServicePort                                        int32
	IngressClass                                       string
	Issuer                                             string
	BlockedPathPrefixes                                []string
}

type CertificateStatus struct {
	Ready, Issuing, Failed bool
	Reason, Message        string
	NotAfter, RenewalTime  time.Time
}

type RouteIngressStatus struct {
	Addresses    []string
	Certificate  CertificateStatus
	BackendReady bool
}

func RouteObjectName(routeID string) string {
	sum := sha256.Sum256([]byte(routeID))
	return "lp-route-" + hex.EncodeToString(sum[:])[:24]
}

const dnsIssuerPrefix = "lp-acme-dns01-"

func DNSIssuerName(zoneID string) string {
	sum := sha256.Sum256([]byte(zoneID))
	return dnsIssuerPrefix + hex.EncodeToString(sum[:])[:16]
}

// CheckRoutePrerequisites names what the cluster lacks before routes can use it.
func (c *Client) CheckRoutePrerequisites(ctx context.Context) (RoutePrerequisites, error) {
	missing := []string{}
	resources, err := discovery.ToDiscoveryInterfaceWithContext(c.Kube.Discovery()).ServerResourcesForGroupVersionWithContext(ctx, "cert-manager.io/v1")
	switch {
	case apierrors.IsUnauthorized(err):
		return RoutePrerequisites{}, ErrRejected
	case err != nil && !apierrors.IsNotFound(err) && !apierrors.IsForbidden(err):
		return RoutePrerequisites{}, fmt.Errorf("read the cert-manager API: %w", err)
	}
	for _, name := range []string{"certificates", "issuers"} {
		if resources == nil || !slices.ContainsFunc(resources.APIResources, func(r metav1.APIResource) bool { return r.Name == name }) {
			missing = append(missing, "cert-manager.io/v1 "+name)
		}
	}
	for _, requirement := range routePermissions {
		for _, verb := range requirement.verbs {
			allowed, checkErr := c.allowed(ctx, requirement.group, requirement.resource, verb)
			if checkErr != nil {
				return RoutePrerequisites{}, checkErr
			}
			if !allowed {
				missing = append(missing, requirement.resource+":"+verb)
			}
		}
	}
	if c.Config.Details["ingress_address"] == "" {
		missing = append(missing, "ingress_address setting")
	}
	return RoutePrerequisites{Ready: len(missing) == 0, Missing: missing}, nil
}

// EnsureHTTPIssuer keeps the namespace's HTTP-01 Issuer. The ACME account has
// no email, so nothing about the person reaches the CA.
func (c *Client) EnsureHTTPIssuer(ctx context.Context, ingressClass, acmeDirectory string) (string, error) {
	if ingressClass != "" && len(validation.IsDNS1123Subdomain(ingressClass)) != 0 {
		return "", fmt.Errorf("%w: invalid ingress class", ErrInvalid)
	}
	acmeDirectory, err := validACMEDirectory(acmeDirectory)
	if err != nil {
		return "", err
	}
	// http01.ingress.ingressClassName needs cert-manager 1.12 or later.
	ingress := map[string]any{}
	if ingressClass != "" {
		ingress["ingressClassName"] = ingressClass
	}
	issuer := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cert-manager.io/v1", "kind": "Issuer",
		"metadata": map[string]any{"name": HTTPIssuerName, "namespace": c.Config.Namespace, "labels": map[string]any{LabelRouteShared: sharedIssuer}},
		"spec": map[string]any{"acme": map[string]any{
			"server":              acmeDirectory,
			"privateKeySecretRef": map[string]any{"name": acmeAccountSecret},
			"solvers":             []any{map[string]any{"http01": map[string]any{"ingress": ingress}}},
		}},
	}}
	issuers := c.Dynamic.Resource(issuerGVR).Namespace(c.Config.Namespace)
	old, err := issuers.Get(ctx, HTTPIssuerName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		_, err = issuers.Create(ctx, issuer, metav1.CreateOptions{})
	case err != nil:
	case old.GetLabels()[LabelRouteShared] != sharedIssuer:
		return "", fmt.Errorf("%w: Issuer %s", ErrOwned, HTTPIssuerName)
	default:
		issuer.SetResourceVersion(old.GetResourceVersion())
		_, err = issuers.Update(ctx, issuer, metav1.UpdateOptions{})
	}
	if err != nil {
		return "", fmt.Errorf("apply the ACME Issuer: %w", err)
	}
	return HTTPIssuerName, nil
}

func validACMEDirectory(acmeDirectory string) (string, error) {
	if acmeDirectory == "" {
		acmeDirectory = defaultACMEDirectory
	}
	if u, err := url.Parse(acmeDirectory); err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return "", fmt.Errorf("%w: the ACME directory must be an https URL", ErrInvalid)
	}
	return acmeDirectory, nil
}

// EnsureDNSIssuer keeps the zone's DNS-01 Issuer and the Secret holding its
// Cloudflare token. The token lives only in that Secret: the Issuer names
// it, and no error repeats it. Both are read before either is written, so a
// same-named object Loomplane didn't create leaves the namespace untouched.
func (c *Client) EnsureDNSIssuer(ctx context.Context, zoneID, zoneName string, token []byte, acmeDirectory string) (string, error) {
	if zoneID == "" || len(validation.IsValidLabelValue(zoneID)) != 0 || len(validation.IsDNS1123Subdomain(zoneName)) != 0 || !strings.Contains(zoneName, ".") {
		return "", fmt.Errorf("%w: invalid DNS challenge zone", ErrInvalid)
	}
	if len(token) == 0 || len(token) > 256 || slices.ContainsFunc(token, func(b byte) bool { return b <= ' ' || b > '~' }) {
		return "", fmt.Errorf("%w: invalid DNS challenge token", ErrInvalid)
	}
	acmeDirectory, err := validACMEDirectory(acmeDirectory)
	if err != nil {
		return "", err
	}
	ns, name := c.Config.Namespace, DNSIssuerName(zoneID)
	owner := map[string]string{LabelZone: zoneID}
	secrets := c.Kube.CoreV1().Secrets(ns)
	issuers := c.Dynamic.Resource(issuerGVR).Namespace(ns)
	oldSecret, err := secrets.Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		oldSecret = nil
	case err != nil:
		return "", fmt.Errorf("read the DNS challenge Secret: %w", err)
	case oldSecret.Labels[LabelZone] != zoneID:
		return "", fmt.Errorf("%w: Secret %s", ErrOwned, name)
	}
	oldIssuer, err := issuers.Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		oldIssuer = nil
	case err != nil:
		return "", fmt.Errorf("read the DNS challenge Issuer: %w", err)
	case oldIssuer.GetLabels()[LabelZone] != zoneID:
		return "", fmt.Errorf("%w: Issuer %s", ErrOwned, name)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: maps.Clone(owner)},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{dnsTokenKey: slices.Clone(token)},
	}
	switch {
	case oldSecret == nil:
		_, err = secrets.Create(ctx, secret, metav1.CreateOptions{})
	case oldSecret.Type != secret.Type || len(oldSecret.Data) != 1 || !bytes.Equal(oldSecret.Data[dnsTokenKey], token):
		// A rotated token replaces the old one; an unchanged one isn't rewritten.
		secret.ResourceVersion = oldSecret.ResourceVersion
		_, err = secrets.Update(ctx, secret, metav1.UpdateOptions{})
	}
	if err != nil {
		return "", fmt.Errorf("apply the DNS challenge Secret: %w", apiReason(err))
	}

	// The selector keeps this Issuer to names in its zone.
	issuer := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cert-manager.io/v1", "kind": "Issuer",
		"metadata": map[string]any{"name": name, "namespace": ns, "labels": map[string]any{LabelZone: zoneID}},
		"spec": map[string]any{"acme": map[string]any{
			"server":              acmeDirectory,
			"privateKeySecretRef": map[string]any{"name": acmeAccountSecret},
			"solvers": []any{map[string]any{
				"dns01":    map[string]any{"cloudflare": map[string]any{"apiTokenSecretRef": map[string]any{"name": name, "key": dnsTokenKey}}},
				"selector": map[string]any{"dnsZones": []any{zoneName}},
			}},
		}},
	}}
	if oldIssuer == nil {
		_, err = issuers.Create(ctx, issuer, metav1.CreateOptions{})
	} else {
		issuer.SetResourceVersion(oldIssuer.GetResourceVersion())
		_, err = issuers.Update(ctx, issuer, metav1.UpdateOptions{})
	}
	if err != nil {
		return "", fmt.Errorf("apply the DNS challenge Issuer: %w", err)
	}
	return name, nil
}

// apiReason keeps an API error's status and drops its message, which can
// quote the object that was sent.
func apiReason(err error) error {
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		return fmt.Errorf("%s (%d)", status.Status().Reason, status.Status().Code)
	}
	return err
}

// RemoveUnusedDNSIssuer deletes the zone's DNS-01 Issuer and token Secret
// once no route Certificate in the namespace names that Issuer.
func (c *Client) RemoveUnusedDNSIssuer(ctx context.Context, zoneID string) error {
	if zoneID == "" || len(validation.IsValidLabelValue(zoneID)) != 0 {
		return fmt.Errorf("%w: invalid DNS challenge zone", ErrInvalid)
	}
	ns, name := c.Config.Namespace, DNSIssuerName(zoneID)
	certificates, err := c.Dynamic.Resource(certificateGVR).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: LabelRoute})
	if err != nil {
		return fmt.Errorf("read the route Certificates: %w", err)
	}
	for _, certificate := range certificates.Items {
		if certificate.GetLabels()[LabelRoute] != "" && certificate.GetDeletionTimestamp() == nil && field(certificate.Object, "spec", "issuerRef", "name") == name {
			return nil
		}
	}
	issuers := c.Dynamic.Resource(issuerGVR).Namespace(ns)
	issuer, err := issuers.Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return err
	case issuer.GetLabels()[LabelZone] == zoneID:
		if err = issuers.Delete(ctx, name, deleteOwnedOptions(issuer)); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete the DNS challenge Issuer: %w", err)
		}
	}
	secret, err := c.Kube.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return err
	case secret.Labels[LabelZone] == zoneID:
		if err = c.Kube.CoreV1().Secrets(ns).Delete(ctx, name, deleteOwnedOptions(secret)); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete the DNS challenge Secret: %w", err)
		}
	}
	return nil
}

// ApplyRouteIngress reads every object it will write before changing any, so
// a foreign object under one of the names leaves the namespace untouched.
func (c *Client) ApplyRouteIngress(ctx context.Context, in RouteIngress) error {
	if err := validRouteIngress(in); err != nil {
		return err
	}
	ns, name := c.Config.Namespace, RouteObjectName(in.RouteID)
	ingresses := c.Kube.NetworkingV1().Ingresses(ns)
	certificates := c.Dynamic.Resource(certificateGVR).Namespace(ns)
	oldIngress, err := routeObject(ingresses.Get(ctx, name, metav1.GetOptions{}))(in.RouteID, "Ingress")
	if err != nil {
		return err
	}
	oldCertificate, err := routeObject(certificates.Get(ctx, name, metav1.GetOptions{}))(in.RouteID, "Certificate")
	if err != nil {
		return err
	}
	deny := false
	if len(in.BlockedPathPrefixes) > 0 {
		existing, getErr := c.Kube.CoreV1().Services(ns).Get(ctx, RouteDenyService, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(getErr):
			deny = true
		case getErr != nil:
			return getErr
		case existing.Labels[LabelRouteShared] != sharedDeny:
			return fmt.Errorf("%w: Service %s", ErrOwned, RouteDenyService)
		}
	}
	if deny {
		service := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: RouteDenyService, Namespace: ns, Labels: map[string]string{LabelRouteShared: sharedDeny}},
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt32(80)}}},
		}
		_, err = c.Kube.CoreV1().Services(ns).Create(ctx, service, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			// Another route created it first; it must still be ours.
			existing, getErr := c.Kube.CoreV1().Services(ns).Get(ctx, RouteDenyService, metav1.GetOptions{})
			if getErr != nil {
				return getErr
			}
			if existing.Labels[LabelRouteShared] != sharedDeny {
				return fmt.Errorf("%w: Service %s", ErrOwned, RouteDenyService)
			}
			err = nil
		}
		if err != nil {
			return fmt.Errorf("create the blocked path Service: %w", err)
		}
	}

	identity := routeLabels(in)
	secretLabels := map[string]any{}
	for key, value := range identity {
		secretLabels[key] = value
	}
	certificate := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cert-manager.io/v1", "kind": "Certificate",
		"metadata": map[string]any{"name": name, "namespace": ns},
		"spec": map[string]any{
			"secretName": name + "-tls",
			"dnsNames":   []any{in.Hostname},
			"issuerRef":  map[string]any{"name": in.Issuer, "kind": "Issuer", "group": "cert-manager.io"},
			// The issued Secret carries the route label so deletes can prove
			// ownership. secretTemplate needs cert-manager 1.5 or later.
			"secretTemplate": map[string]any{"labels": secretLabels},
		},
	}}
	certificate.SetLabels(maps.Clone(identity))
	if oldCertificate != nil {
		certificate.SetResourceVersion(oldCertificate.GetResourceVersion())
		_, err = certificates.Update(ctx, certificate, metav1.UpdateOptions{})
	} else {
		_, err = certificates.Create(ctx, certificate, metav1.CreateOptions{})
	}
	if err != nil {
		return fmt.Errorf("apply the route Certificate: %w", err)
	}

	pathType := networkingv1.PathTypePrefix
	backend := func(service string, port int32) networkingv1.IngressBackend {
		return networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: service, Port: networkingv1.ServiceBackendPort{Number: port}}}
	}
	paths := []networkingv1.HTTPIngressPath{{Path: "/", PathType: &pathType, Backend: backend(in.ServiceName, in.ServicePort)}}
	for _, prefix := range in.BlockedPathPrefixes {
		paths = append(paths, networkingv1.HTTPIngressPath{Path: prefix, PathType: &pathType, Backend: backend(RouteDenyService, 80)})
	}
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: maps.Clone(identity)},
		Spec: networkingv1.IngressSpec{
			TLS:   []networkingv1.IngressTLS{{Hosts: []string{in.Hostname}, SecretName: name + "-tls"}},
			Rules: []networkingv1.IngressRule{{Host: in.Hostname, IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: paths}}}},
		},
	}
	if in.IngressClass != "" {
		class := in.IngressClass
		ingress.Spec.IngressClassName = &class
	}
	if oldIngress != nil {
		ingress.ResourceVersion = oldIngress.ResourceVersion
		_, err = ingresses.Update(ctx, ingress, metav1.UpdateOptions{})
	} else {
		_, err = ingresses.Create(ctx, ingress, metav1.CreateOptions{})
	}
	if err != nil {
		return fmt.Errorf("apply the route Ingress: %w", err)
	}
	return nil
}

// RouteIngressStatus reads what the controller and cert-manager report. Objects
// not created yet read as an empty status.
func (c *Client) RouteIngressStatus(ctx context.Context, routeID, serviceName string) (RouteIngressStatus, error) {
	out := RouteIngressStatus{Addresses: []string{}}
	if routeID == "" || len(validation.IsDNS1035Label(serviceName)) != 0 {
		return out, fmt.Errorf("%w: invalid route status request", ErrInvalid)
	}
	ns, name := c.Config.Namespace, RouteObjectName(routeID)
	ingress, err := routeObject(c.Kube.NetworkingV1().Ingresses(ns).Get(ctx, name, metav1.GetOptions{}))(routeID, "Ingress")
	if err != nil {
		return out, err
	}
	if ingress != nil {
		for _, lb := range ingress.Status.LoadBalancer.Ingress {
			if lb.IP != "" {
				out.Addresses = append(out.Addresses, lb.IP)
			}
			if lb.Hostname != "" {
				out.Addresses = append(out.Addresses, lb.Hostname)
			}
		}
	}
	certificate, err := routeObject(c.Dynamic.Resource(certificateGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{}))(routeID, "Certificate")
	if err != nil {
		return out, err
	}
	if certificate != nil {
		out.Certificate = certificateStatus(certificate)
		if out.Certificate.Failed {
			message, challengeErr := c.challengeFailure(ctx, certificate)
			if challengeErr != nil {
				return out, challengeErr
			}
			if message != "" {
				out.Certificate.Message = message
			}
		}
	}
	endpointSlices, err := c.Kube.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{LabelSelector: labels.Set{discoveryv1.LabelServiceName: serviceName}.String()})
	if err != nil {
		return out, fmt.Errorf("read the route backend endpoints: %w", err)
	}
	for _, slice := range endpointSlices.Items {
		for _, endpoint := range slice.Endpoints {
			// An unset ready condition means ready.
			if endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready {
				out.BackendReady = true
			}
		}
	}
	return out, nil
}

// certificateStatus follows cert-manager v1: Ready reports the Secret, and a
// failed issuance shows on Issuing (False, reason Failed) with lastFailureTime.
func certificateStatus(u *unstructured.Unstructured) CertificateStatus {
	out := CertificateStatus{}
	conditions, _, err := unstructured.NestedSlice(u.Object, "status", "conditions")
	if err != nil {
		conditions = nil
	}
	issuingFailed := false
	issuingReason, issuingMessage := "", ""
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		status := field(condition, "status")
		switch field(condition, "type") {
		case "Ready":
			out.Reason, out.Message = field(condition, "reason"), field(condition, "message")
			out.Ready = status == "True"
		case "Issuing":
			out.Issuing = status == "True"
			issuingReason, issuingMessage = field(condition, "reason"), field(condition, "message")
			issuingFailed = status == "False" && issuingReason == "Failed"
		}
	}
	lastFailure := field(u.Object, "status", "lastFailureTime") != ""
	switch {
	case !out.Ready && (issuingFailed || lastFailure && !out.Issuing):
		out.Failed = true
		if issuingReason != "" {
			out.Reason, out.Message = issuingReason, issuingMessage
		}
	case out.Ready && issuingFailed:
		// The current certificate still serves while its reissue fails.
		out.Message = issuingMessage
	}
	for name, target := range map[string]*time.Time{"notAfter": &out.NotAfter, "renewalTime": &out.RenewalTime} {
		if parsed, err := time.Parse(time.RFC3339, field(u.Object, "status", name)); err == nil {
			*target = parsed
		}
	}
	return out
}

// challengeFailure finds the newest failing Challenge for the Certificate's
// names. A wildcard's Challenge names the base domain and sets wildcard.
func (c *Client) challengeFailure(ctx context.Context, certificate *unstructured.Unstructured) (string, error) {
	names, _, err := unstructured.NestedStringSlice(certificate.Object, "spec", "dnsNames")
	if err != nil {
		return "", nil
	}
	list, err := c.Dynamic.Resource(challengeGVR).Namespace(c.Config.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("read ACME challenges: %w", err)
	}
	var newest time.Time
	message := ""
	for _, challenge := range list.Items {
		name := field(challenge.Object, "spec", "dnsName")
		if wildcard, _, boolErr := unstructured.NestedBool(challenge.Object, "spec", "wildcard"); boolErr == nil && wildcard {
			name = "*." + name
		}
		reason, created := field(challenge.Object, "status", "reason"), challenge.GetCreationTimestamp().Time
		if reason == "" || field(challenge.Object, "status", "state") == "valid" || !slices.Contains(names, name) || (message != "" && !created.After(newest)) {
			continue
		}
		newest, message = created, reason
	}
	return message, nil
}

// field reads a string from an unstructured object, empty when absent or not a string.
func field(object map[string]any, path ...string) string {
	value, _, err := unstructured.NestedString(object, path...)
	if err != nil {
		return ""
	}
	return value
}

// DeleteRouteIngress removes only objects labelled for the route. The shared
// deny Service and Issuer stay for other routes.
func (c *Client) DeleteRouteIngress(ctx context.Context, routeID string) error {
	if routeID == "" {
		return fmt.Errorf("%w: a route is required", ErrInvalid)
	}
	ns, name := c.Config.Namespace, RouteObjectName(routeID)
	ignoreMissing := func(err error) error {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	ingress, err := c.Kube.NetworkingV1().Ingresses(ns).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return err
	case ingress.Labels[LabelRoute] == routeID:
		if err = ignoreMissing(c.Kube.NetworkingV1().Ingresses(ns).Delete(ctx, name, deleteOwnedOptions(ingress))); err != nil {
			return fmt.Errorf("delete the route Ingress: %w", err)
		}
	}
	certificates := c.Dynamic.Resource(certificateGVR).Namespace(ns)
	certificate, err := certificates.Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return err
	case certificate.GetLabels()[LabelRoute] == routeID:
		if err = ignoreMissing(certificates.Delete(ctx, name, deleteOwnedOptions(certificate))); err != nil {
			return fmt.Errorf("delete the route Certificate: %w", err)
		}
	}
	// cert-manager leaves the issued Secret behind when its Certificate goes.
	secret, err := c.Kube.CoreV1().Secrets(ns).Get(ctx, name+"-tls", metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return err
	case secret.Labels[LabelRoute] == routeID:
		if err = ignoreMissing(c.Kube.CoreV1().Secrets(ns).Delete(ctx, secret.Name, deleteOwnedOptions(secret))); err != nil {
			return fmt.Errorf("delete the route TLS Secret: %w", err)
		}
	}
	return nil
}

func routeLabels(in RouteIngress) map[string]string {
	return map[string]string{LabelRoute: in.RouteID, owned.LabelOrg: in.OrgID, owned.LabelProject: in.ProjectID, owned.LabelEnvironment: in.EnvironmentID}
}

// routeObject reads a Get result: a missing object is nil, and one without the
// matching route label is refused.
func routeObject[T interface {
	comparable
	metav1.Object
}](object T, err error) func(routeID, kind string) (T, error) {
	return func(routeID, kind string) (T, error) {
		var zero T
		if apierrors.IsNotFound(err) {
			return zero, nil
		}
		if err != nil {
			return zero, err
		}
		if object.GetLabels()[LabelRoute] != routeID {
			return zero, fmt.Errorf("%w: %s %s", ErrOwned, kind, object.GetName())
		}
		return object, nil
	}
}

func validRouteIngress(in RouteIngress) error {
	for _, value := range []string{in.RouteID, in.OrgID, in.ProjectID, in.EnvironmentID} {
		if value == "" || len(validation.IsValidLabelValue(value)) != 0 {
			return fmt.Errorf("%w: a route needs its route, organization, project and environment", ErrInvalid)
		}
	}
	host, wildcard := strings.CutPrefix(in.Hostname, "*.")
	if len(validation.IsDNS1123Subdomain(host)) != 0 || !strings.Contains(host, ".") {
		return fmt.Errorf("%w: invalid route hostname", ErrInvalid)
	}
	// HTTP-01 can't prove a wildcard; only a zone's DNS-01 Issuer can.
	if wildcard && !strings.HasPrefix(in.Issuer, dnsIssuerPrefix) {
		return fmt.Errorf("%w: a wildcard route needs its zone's DNS challenge Issuer", ErrInvalid)
	}
	switch {
	case len(validation.IsDNS1035Label(in.ServiceName)) != 0 || in.ServiceName == RouteDenyService:
		return fmt.Errorf("%w: invalid route Service", ErrInvalid)
	case in.ServicePort < 1 || in.ServicePort > 65535:
		return fmt.Errorf("%w: invalid route Service port", ErrInvalid)
	case in.IngressClass != "" && len(validation.IsDNS1123Subdomain(in.IngressClass)) != 0:
		return fmt.Errorf("%w: invalid ingress class", ErrInvalid)
	case len(validation.IsDNS1123Subdomain(in.Issuer)) != 0:
		return fmt.Errorf("%w: invalid route Issuer", ErrInvalid)
	case !validBlockedPrefixes(in.BlockedPathPrefixes):
		return fmt.Errorf("%w: invalid blocked path prefixes", ErrInvalid)
	}
	return nil
}

// validBlockedPrefixes keeps the server ingress's rules, so a prefix that
// blocks a path on Docker blocks the same paths on Kubernetes.
func validBlockedPrefixes(prefixes []string) bool {
	if len(prefixes) > 8 {
		return false
	}
	seen := map[string]bool{}
	for _, p := range prefixes {
		if seen[p] || !strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") || len(p) > 128 || strings.Contains(p, "//") || strings.ContainsAny(p, "*?#%") {
			return false
		}
		seen[p] = true
		for _, segment := range strings.Split(p[1:], "/") {
			if segment == "." || segment == ".." {
				return false
			}
		}
	}
	return true
}

// ingressAddress accepts an IP or an exact hostname. The hostname rules match
// cloudinfra.NormalizeHostname, which this package can't import.
func ingressAddress(value string) (string, bool) {
	if ip := net.ParseIP(value); ip != nil {
		return ip.String(), true
	}
	host, err := idna.Lookup.ToASCII(strings.ToLower(strings.TrimSuffix(value, ".")))
	if err != nil || len(host) > 253 || !strings.Contains(host, ".") {
		return "", false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, ch := range label {
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
				return "", false
			}
		}
	}
	return host, true
}
