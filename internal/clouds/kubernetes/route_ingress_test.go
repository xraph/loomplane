package kubernetes

import (
	"encoding/pem"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kuberuntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

func fakeRouteClient(t *testing.T, objects ...kuberuntime.Object) *Client {
	t.Helper()
	typed, dynamic := []kuberuntime.Object{}, []kuberuntime.Object{}
	for _, o := range objects {
		if _, ok := o.(*unstructured.Unstructured); ok {
			dynamic = append(dynamic, o)
		} else {
			typed = append(typed, o)
		}
	}
	kinds := map[schema.GroupVersionResource]string{issuerGVR: "IssuerList", certificateGVR: "CertificateList", challengeGVR: "ChallengeList"}
	kube := fake.NewClientset(typed...)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(kuberuntime.NewScheme(), kinds, dynamic...)
	// The API server assigns UIDs; the fakes don't.
	assignUID := func(action kubetesting.Action) (bool, kuberuntime.Object, error) {
		if o, ok := action.(kubetesting.CreateAction).GetObject().(metav1.Object); ok && o.GetUID() == "" {
			o.SetUID(types.UID("uid-" + o.GetName()))
		}
		return false, nil, nil
	}
	kube.PrependReactor("create", "*", assignUID)
	dyn.PrependReactor("create", "*", assignUID)
	return &Client{Config: &Config{Namespace: "selected", Details: map[string]string{"ingress_address": "203.0.113.10"}}, Kube: kube, Dynamic: dyn}
}

func routeInput(routeID, host string) RouteIngress {
	return RouteIngress{RouteID: routeID, OrgID: "org", ProjectID: "p", EnvironmentID: "e", Hostname: host, ServiceName: "lp-inst", ServicePort: 8080, IngressClass: "nginx", Issuer: HTTPIssuerName}
}

func getIngress(t *testing.T, c *Client, name string) *networkingv1.Ingress {
	t.Helper()
	ing, err := c.Kube.NetworkingV1().Ingresses("selected").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return ing
}

type certificateFixture struct {
	dnsNames       []string
	issuer, secret string
	labels         map[string]string
	secretLabels   map[string]any
}

func getCertificate(t *testing.T, c *Client, name string) certificateFixture {
	t.Helper()
	u, err := c.Dynamic.Resource(certificateGVR).Namespace("selected").Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	names, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "dnsNames")
	issuer, _, _ := unstructured.NestedString(u.Object, "spec", "issuerRef", "name")
	secret, _, _ := unstructured.NestedString(u.Object, "spec", "secretName")
	secretLabels, _, _ := unstructured.NestedMap(u.Object, "spec", "secretTemplate", "labels")
	return certificateFixture{dnsNames: names, issuer: issuer, secret: secret, labels: u.GetLabels(), secretLabels: secretLabels}
}

func TestRouteObjectNamesAreStableHashes(t *testing.T) {
	name := RouteObjectName("route_1")
	if !strings.HasPrefix(name, "lp-route-") || len(name) != len("lp-route-")+24 || name != RouteObjectName("route_1") || name == RouteObjectName("route_2") {
		t.Fatalf("route object name %q", name)
	}
	issuer := DNSIssuerName("zone_1")
	if !strings.HasPrefix(issuer, "lp-acme-dns01-") || len(issuer) != len("lp-acme-dns01-")+16 {
		t.Fatalf("DNS issuer name %q", issuer)
	}
}

func TestApplyRouteIngressIsOwnedAndIdempotent(t *testing.T) {
	c := fakeRouteClient(t)
	in := routeInput("route_1", "api.acme.dev")
	for range 2 {
		if err := c.ApplyRouteIngress(t.Context(), in); err != nil {
			t.Fatal(err)
		}
	}
	name := RouteObjectName("route_1")
	ing := getIngress(t, c, name)
	if ing.Labels[LabelRoute] != "route_1" || *ing.Spec.IngressClassName != "nginx" || ing.Spec.TLS[0].SecretName != name+"-tls" || ing.Spec.TLS[0].Hosts[0] != "api.acme.dev" {
		t.Fatalf("%+v", ing)
	}
	for key, want := range map[string]string{owned.LabelOrg: "org", owned.LabelProject: "p", owned.LabelEnvironment: "e"} {
		if ing.Labels[key] != want {
			t.Fatalf("ingress label %s = %q", key, ing.Labels[key])
		}
	}
	rule := ing.Spec.Rules[0]
	path := rule.HTTP.Paths[0]
	if len(ing.Spec.Rules) != 1 || rule.Host != "api.acme.dev" || len(rule.HTTP.Paths) != 1 || path.Path != "/" || *path.PathType != networkingv1.PathTypePrefix || path.Backend.Service.Name != "lp-inst" || path.Backend.Service.Port.Number != 8080 {
		t.Fatalf("rule %+v", rule)
	}
	cert := getCertificate(t, c, name)
	if len(cert.dnsNames) != 1 || cert.dnsNames[0] != "api.acme.dev" || cert.issuer != HTTPIssuerName || cert.secret != name+"-tls" || cert.labels[LabelRoute] != "route_1" || cert.secretLabels[LabelRoute] != "route_1" {
		t.Fatalf("%+v", cert)
	}
	if _, err := c.Kube.CoreV1().Services("selected").Get(t.Context(), RouteDenyService, metav1.GetOptions{}); err == nil {
		t.Fatal("deny Service created without blocked paths")
	}
}

func TestApplyRouteIngressRefusesForeignObject(t *testing.T) {
	name := RouteObjectName("route_1")
	foreign := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "selected", Labels: map[string]string{"team": "theirs"}}, Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "theirs.example.net"}}}}
	c := fakeRouteClient(t, foreign)
	if err := c.ApplyRouteIngress(t.Context(), routeInput("route_1", "api.acme.dev")); !errors.Is(err, ErrOwned) {
		t.Fatalf("foreign Ingress was adopted: %v", err)
	}
	ing := getIngress(t, c, name)
	if ing.Spec.Rules[0].Host != "theirs.example.net" || ing.Labels[LabelRoute] != "" {
		t.Fatalf("foreign Ingress changed: %+v", ing)
	}
	for _, action := range c.Kube.(*fake.Clientset).Actions() {
		if action.GetVerb() != "get" {
			t.Fatalf("foreign Ingress led to mutation: %+v", action)
		}
	}

	// Another route's Certificate under this name is just as foreign.
	cert := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "Certificate", "metadata": map[string]any{"name": name, "namespace": "selected", "labels": map[string]any{LabelRoute: "route_2"}}, "spec": map[string]any{"dnsNames": []any{"other.acme.dev"}}}}
	c = fakeRouteClient(t, cert)
	if err := c.ApplyRouteIngress(t.Context(), routeInput("route_1", "api.acme.dev")); !errors.Is(err, ErrOwned) {
		t.Fatalf("foreign Certificate was adopted: %v", err)
	}
	if got := getCertificate(t, c, name); got.dnsNames[0] != "other.acme.dev" {
		t.Fatalf("foreign Certificate changed: %+v", got)
	}
}

func TestBlockedPrefixesRouteToDenyService(t *testing.T) {
	c := fakeRouteClient(t)
	in := routeInput("route_1", "api.acme.dev")
	in.BlockedPathPrefixes = []string{"/admin"}
	for range 2 {
		if err := c.ApplyRouteIngress(t.Context(), in); err != nil {
			t.Fatal(err)
		}
	}
	paths := getIngress(t, c, RouteObjectName("route_1")).Spec.Rules[0].HTTP.Paths
	if len(paths) != 2 || paths[0].Path != "/" || paths[0].Backend.Service.Name != "lp-inst" || paths[1].Path != "/admin" || *paths[1].PathType != networkingv1.PathTypePrefix || paths[1].Backend.Service.Name != RouteDenyService || paths[1].Backend.Service.Port.Number != 80 {
		t.Fatalf("paths %+v", paths)
	}
	deny, err := c.Kube.CoreV1().Services("selected").Get(t.Context(), RouteDenyService, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if deny.Name != "lp-route-deny" || deny.Spec.Selector != nil || deny.Spec.Type != corev1.ServiceTypeClusterIP || len(deny.Spec.Ports) != 1 || deny.Spec.Ports[0].Port != 80 || deny.Labels[LabelRouteShared] == "" {
		t.Fatalf("deny Service %+v", deny)
	}

	// A same-named Service that Loomplane did not create is never adopted.
	theirs := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: RouteDenyService, Namespace: "selected"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "theirs"}}}
	c = fakeRouteClient(t, theirs)
	if err := c.ApplyRouteIngress(t.Context(), in); !errors.Is(err, ErrOwned) {
		t.Fatalf("foreign deny Service was adopted: %v", err)
	}
	if _, err := c.Kube.NetworkingV1().Ingresses("selected").Get(t.Context(), RouteObjectName("route_1"), metav1.GetOptions{}); err == nil {
		t.Fatal("Ingress pointed at a foreign Service")
	}
}

func TestApplyRouteIngressRejectsUnsafeInput(t *testing.T) {
	for name, change := range map[string]func(*RouteIngress){
		"no route":        func(in *RouteIngress) { in.RouteID = "" },
		"no org":          func(in *RouteIngress) { in.OrgID = "" },
		"no host":         func(in *RouteIngress) { in.Hostname = "" },
		"bad service":     func(in *RouteIngress) { in.ServiceName = "Not A Service" },
		"no port":         func(in *RouteIngress) { in.ServicePort = 0 },
		"no issuer":       func(in *RouteIngress) { in.Issuer = "" },
		"relative prefix": func(in *RouteIngress) { in.BlockedPathPrefixes = []string{"admin"} },
		"root prefix":     func(in *RouteIngress) { in.BlockedPathPrefixes = []string{"/"} },
	} {
		t.Run(name, func(t *testing.T) {
			c := fakeRouteClient(t)
			in := routeInput("route_1", "api.acme.dev")
			change(&in)
			if err := c.ApplyRouteIngress(t.Context(), in); err == nil {
				t.Fatal("accepted unsafe route")
			}
			if len(c.Kube.(*fake.Clientset).Actions()) != 0 {
				t.Fatal("unsafe route reached the cluster")
			}
		})
	}
}

func TestEnsureHTTPIssuerUsesIngressClassWithoutEmail(t *testing.T) {
	c := fakeRouteClient(t)
	for range 2 {
		name, err := c.EnsureHTTPIssuer(t.Context(), "nginx", "")
		if err != nil || name != HTTPIssuerName {
			t.Fatalf("%q %v", name, err)
		}
	}
	u, err := c.Dynamic.Resource(issuerGVR).Namespace("selected").Get(t.Context(), HTTPIssuerName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	server, _, _ := unstructured.NestedString(u.Object, "spec", "acme", "server")
	account, _, _ := unstructured.NestedString(u.Object, "spec", "acme", "privateKeySecretRef", "name")
	_, hasEmail, _ := unstructured.NestedString(u.Object, "spec", "acme", "email")
	solvers, _, _ := unstructured.NestedSlice(u.Object, "spec", "acme", "solvers")
	class, _, _ := unstructured.NestedString(solvers[0].(map[string]any), "http01", "ingress", "ingressClassName")
	if server != "https://acme-v02.api.letsencrypt.org/directory" || account != "lp-acme-account" || hasEmail || class != "nginx" || u.GetLabels()[LabelRouteShared] == "" {
		t.Fatalf("issuer %+v", u.Object)
	}
	if _, err = c.EnsureHTTPIssuer(t.Context(), "traefik", "https://acme-staging-v02.api.letsencrypt.org/directory"); err != nil {
		t.Fatal(err)
	}
	u, _ = c.Dynamic.Resource(issuerGVR).Namespace("selected").Get(t.Context(), HTTPIssuerName, metav1.GetOptions{})
	server, _, _ = unstructured.NestedString(u.Object, "spec", "acme", "server")
	solvers, _, _ = unstructured.NestedSlice(u.Object, "spec", "acme", "solvers")
	class, _, _ = unstructured.NestedString(solvers[0].(map[string]any), "http01", "ingress", "ingressClassName")
	if server != "https://acme-staging-v02.api.letsencrypt.org/directory" || class != "traefik" {
		t.Fatalf("issuer not updated: %+v", u.Object)
	}
	if _, err = c.EnsureHTTPIssuer(t.Context(), "nginx", "http://acme.example.net/directory"); err == nil {
		t.Fatal("accepted a plain HTTP ACME directory")
	}

	foreign := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "Issuer", "metadata": map[string]any{"name": HTTPIssuerName, "namespace": "selected"}, "spec": map[string]any{"selfSigned": map[string]any{}}}}
	c = fakeRouteClient(t, foreign)
	if _, err = c.EnsureHTTPIssuer(t.Context(), "nginx", ""); !errors.Is(err, ErrOwned) {
		t.Fatalf("foreign Issuer was adopted: %v", err)
	}
}

func certificateObject(name string, conditions ...map[string]any) *unstructured.Unstructured {
	list := make([]any, 0, len(conditions))
	for _, c := range conditions {
		list = append(list, c)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cert-manager.io/v1", "kind": "Certificate",
		"metadata": map[string]any{"name": name, "namespace": "selected", "labels": map[string]any{LabelRoute: "route_1"}},
		"spec":     map[string]any{"dnsNames": []any{"api.acme.dev"}},
		"status":   map[string]any{"conditions": list, "notAfter": "2027-01-20T00:00:00Z", "renewalTime": "2026-12-21T00:00:00Z"},
	}}
}

func challengeObject(name, host, state, reason string, created time.Time) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "acme.cert-manager.io/v1", "kind": "Challenge",
		"metadata": map[string]any{"name": name, "namespace": "selected", "creationTimestamp": created.UTC().Format(time.RFC3339)},
		"spec":     map[string]any{"dnsName": host, "type": "HTTP-01"},
		"status":   map[string]any{"state": state, "reason": reason},
	}}
}

func TestRouteIngressStatusReadsCertManagerConditions(t *testing.T) {
	name := RouteObjectName("route_1")
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "selected", Labels: map[string]string{LabelRoute: "route_1"}}, Status: networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{Ingress: []networkingv1.IngressLoadBalancerIngress{{Hostname: "lb.example.net"}}}}}
	ready, notReady := true, false
	slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "lp-inst-abc", Namespace: "selected", Labels: map[string]string{discoveryv1.LabelServiceName: "lp-inst"}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.4"}, Conditions: discoveryv1.EndpointConditions{Ready: &notReady}}, {Addresses: []string{"10.0.0.5"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}}}
	other := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "lp-other-abc", Namespace: "selected", Labels: map[string]string{discoveryv1.LabelServiceName: "lp-other"}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.6"}}}}

	issuing := certificateObject(name,
		map[string]any{"type": "Ready", "status": "True", "reason": "Ready", "message": "Certificate is up to date and has not expired"},
		map[string]any{"type": "Issuing", "status": "True", "reason": "Renewing", "message": "Renewing certificate as renewal was scheduled"})
	c := fakeRouteClient(t, ing, slice, other, issuing)
	status, err := c.RouteIngressStatus(t.Context(), "route_1", "lp-inst")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(status.Addresses, []string{"lb.example.net"}) || !status.Certificate.Ready || !status.Certificate.Issuing || status.Certificate.Failed || !status.BackendReady {
		t.Fatalf("%+v", status)
	}
	if !status.Certificate.NotAfter.Equal(time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC)) || !status.Certificate.RenewalTime.Equal(time.Date(2026, 12, 21, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("certificate times %+v", status.Certificate)
	}

	// A first issuance that failed: the Secret still does not exist, and the
	// failure shows on Issuing with lastFailureTime.
	retried := "The certificate request has failed to complete and will be retried: Failed to wait for order resource \"lp-route-1\" to become ready: order is in \"invalid\" state"
	failed := certificateObject(name,
		map[string]any{"type": "Ready", "status": "False", "reason": "DoesNotExist", "message": "Issuing certificate as Secret does not exist"},
		map[string]any{"type": "Issuing", "status": "False", "reason": "Failed", "message": retried})
	failed.Object["status"].(map[string]any)["lastFailureTime"] = "2026-10-22T10:00:00Z"
	now := time.Now()
	c = fakeRouteClient(t, ing, other, failed,
		challengeObject("old", "api.acme.dev", "invalid", "old failure", now.Add(-time.Hour)),
		challengeObject("new", "api.acme.dev", "errored", "Waiting for HTTP-01 challenge propagation: wrong status code '404', expected '200'", now),
		challengeObject("elsewhere", "other.acme.dev", "invalid", "not this route", now.Add(time.Hour)))
	status, err = c.RouteIngressStatus(t.Context(), "route_1", "lp-inst")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Certificate.Failed || status.Certificate.Ready || status.Certificate.Issuing || status.Certificate.Reason != "Failed" || !strings.Contains(status.Certificate.Message, "wrong status code '404'") || status.BackendReady {
		t.Fatalf("%+v", status)
	}
	// Without a Challenge the Issuing message is the reason given.
	c = fakeRouteClient(t, ing, failed)
	if status, err = c.RouteIngressStatus(t.Context(), "route_1", "lp-inst"); err != nil || !status.Certificate.Failed || status.Certificate.Message != retried {
		t.Fatalf("%+v %v", status, err)
	}

	// lastFailureTime alone marks a failure while no issuance is running.
	backoff := certificateObject(name, map[string]any{"type": "Ready", "status": "False", "reason": "DoesNotExist", "message": "Issuing certificate as Secret does not exist"})
	backoff.Object["status"].(map[string]any)["lastFailureTime"] = "2026-10-22T10:00:00Z"
	c = fakeRouteClient(t, ing, backoff)
	if status, err = c.RouteIngressStatus(t.Context(), "route_1", "lp-inst"); err != nil || !status.Certificate.Failed {
		t.Fatalf("%+v %v", status, err)
	}

	// A routine reissue (the request changed) is issuing, not failed.
	reissue := certificateObject(name,
		map[string]any{"type": "Ready", "status": "False", "reason": "RequestChanged", "message": "Fields on existing CertificateRequest resource not up to date: [spec.dnsNames]"},
		map[string]any{"type": "Issuing", "status": "True", "reason": "RequestChanged", "message": "Fields on existing CertificateRequest resource not up to date: [spec.dnsNames]"})
	reissue.Object["status"].(map[string]any)["lastFailureTime"] = "2026-10-01T10:00:00Z"
	c = fakeRouteClient(t, ing, reissue)
	if status, err = c.RouteIngressStatus(t.Context(), "route_1", "lp-inst"); err != nil || status.Certificate.Failed || status.Certificate.Ready || !status.Certificate.Issuing {
		t.Fatalf("%+v %v", status, err)
	}

	// Ready with its renewal still ahead.
	renewalPending := certificateObject(name, map[string]any{"type": "Ready", "status": "True", "reason": "Ready", "message": "Certificate is up to date and has not expired"})
	c = fakeRouteClient(t, ing, renewalPending)
	if status, err = c.RouteIngressStatus(t.Context(), "route_1", "lp-inst"); err != nil || !status.Certificate.Ready || status.Certificate.Issuing || status.Certificate.Failed {
		t.Fatalf("%+v %v", status, err)
	}

	// A failing renewal leaves the served certificate Ready but says why.
	renewalFailing := certificateObject(name,
		map[string]any{"type": "Ready", "status": "True", "reason": "Ready", "message": "Certificate is up to date and has not expired"},
		map[string]any{"type": "Issuing", "status": "False", "reason": "Failed", "message": retried})
	renewalFailing.Object["status"].(map[string]any)["lastFailureTime"] = "2026-10-22T10:00:00Z"
	c = fakeRouteClient(t, ing, renewalFailing)
	if status, err = c.RouteIngressStatus(t.Context(), "route_1", "lp-inst"); err != nil || !status.Certificate.Ready || status.Certificate.Failed || status.Certificate.Message != retried {
		t.Fatalf("%+v %v", status, err)
	}

	// A first issuance still running has not failed.
	pending := certificateObject(name,
		map[string]any{"type": "Ready", "status": "False", "reason": "DoesNotExist", "message": "Issuing certificate as Secret does not exist"},
		map[string]any{"type": "Issuing", "status": "True", "reason": "DoesNotExist", "message": "Issuing certificate as Secret does not exist"})
	starting := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "lp-inst-abc", Namespace: "selected", Labels: map[string]string{discoveryv1.LabelServiceName: "lp-inst"}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.4"}, Conditions: discoveryv1.EndpointConditions{Ready: &notReady}}}}
	c = fakeRouteClient(t, ing, starting, pending)
	if status, err = c.RouteIngressStatus(t.Context(), "route_1", "lp-inst"); err != nil || status.Certificate.Failed || status.Certificate.Ready || !status.Certificate.Issuing || status.BackendReady {
		t.Fatalf("%+v %v", status, err)
	}

	// Nothing applied yet reads as an empty status.
	c = fakeRouteClient(t)
	if status, err = c.RouteIngressStatus(t.Context(), "route_1", "lp-inst"); err != nil || len(status.Addresses) != 0 || status.Certificate.Ready || status.Certificate.Failed {
		t.Fatalf("%+v %v", status, err)
	}

	// A same-named Ingress that belongs to nobody is not reported as ours.
	foreign := ing.DeepCopy()
	foreign.Labels = nil
	c = fakeRouteClient(t, foreign)
	if _, err = c.RouteIngressStatus(t.Context(), "route_1", "lp-inst"); !errors.Is(err, ErrOwned) {
		t.Fatalf("foreign Ingress status: %v", err)
	}
}

func allowExcept(kube *fake.Clientset, denied ...string) {
	kube.PrependReactor("create", "selfsubjectaccessreviews", func(action kubetesting.Action) (bool, kuberuntime.Object, error) {
		review := action.(kubetesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview).DeepCopy()
		attrs := review.Spec.ResourceAttributes
		review.Status.Allowed = attrs.Namespace == "selected" && !slices.Contains(denied, attrs.Resource+":"+attrs.Verb)
		return true, review, nil
	})
}

func TestCheckRoutePrerequisitesNamesWhatIsMissing(t *testing.T) {
	c := fakeRouteClient(t)
	kube := c.Kube.(*fake.Clientset)
	allowExcept(kube, "ingresses:create")
	c.Config.Details = map[string]string{}
	got, err := c.CheckRoutePrerequisites(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cert-manager.io/v1 certificates", "cert-manager.io/v1 issuers", "ingresses:create", "ingress_address setting"} {
		if !slices.Contains(got.Missing, want) {
			t.Fatalf("missing %q from %v", want, got.Missing)
		}
	}
	if got.Ready || len(got.Missing) != 4 {
		t.Fatalf("%+v", got)
	}

	c = fakeRouteClient(t)
	kube = c.Kube.(*fake.Clientset)
	allowExcept(kube)
	kube.Discovery().(*fakediscovery.FakeDiscovery).Resources = []*metav1.APIResourceList{{GroupVersion: "cert-manager.io/v1", APIResources: []metav1.APIResource{{Name: "certificates"}, {Name: "issuers"}, {Name: "clusterissuers"}}}}
	if got, err = c.CheckRoutePrerequisites(t.Context()); err != nil || !got.Ready || len(got.Missing) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	checked := []string{}
	for _, action := range kube.Actions() {
		if create, ok := action.(kubetesting.CreateAction); ok {
			if review, ok := create.GetObject().(*authv1.SelfSubjectAccessReview); ok {
				a := review.Spec.ResourceAttributes
				checked = append(checked, a.Group+"/"+a.Resource+":"+a.Verb)
			}
		}
	}
	for _, want := range []string{"networking.k8s.io/ingresses:list", "cert-manager.io/issuers:patch", "cert-manager.io/certificates:delete", "acme.cert-manager.io/orders:list", "acme.cert-manager.io/challenges:get", "discovery.k8s.io/endpointslices:list", "/services:patch", "/secrets:delete"} {
		if !slices.Contains(checked, want) {
			t.Fatalf("unchecked %s in %v", want, checked)
		}
	}
	for _, review := range checked {
		if strings.Contains(review, "clusterissuers") {
			t.Fatal("route prerequisites asked for a cluster-scoped issuer")
		}
	}
}

func TestCheckReportsRoutePrerequisitesWithoutMissing(t *testing.T) {
	s, _, _ := clusterFixture(t, false)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
	cfg, err := ParseConfig(kubeconfig(t, s.URL, ca), map[string]string{"namespace": "production", "ingress_class": "nginx", "ingress_address": "lb.example.net"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c.Dynamic == nil {
		t.Fatal("no dynamic client")
	}
	result, err := c.Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Details["route_prerequisites"] != "missing: cert-manager.io/v1 certificates, cert-manager.io/v1 issuers" || result.Details["ingress_class"] != "nginx" || result.Details["ingress_address"] != "lb.example.net" {
		t.Fatalf("%+v", result.Details)
	}
	if len(result.Missing) != 0 {
		t.Fatalf("route prerequisites made the connection incomplete: %v", result.Missing)
	}
}

func TestConfigValidatesRouteSettings(t *testing.T) {
	raw := kubeconfig(t, "https://cluster.example.com", testCA(t))
	for value, want := range map[string]string{"203.0.113.10": "203.0.113.10", "2001:DB8::1": "2001:db8::1", "LB.Example.NET.": "lb.example.net"} {
		c, err := ParseConfig(raw, map[string]string{"ingress_address": value, "ingress_class": "nginx"})
		if err != nil {
			t.Fatal(value, err)
		}
		if c.Details["ingress_address"] != want || c.Details["ingress_class"] != "nginx" {
			t.Fatalf("%s: %+v", value, c.Details)
		}
	}
	for _, value := range []string{"*.example.net", "lb", "lb.example.net:443", "https://lb.example.net", "-lb.example.net", "lb_1.example.net"} {
		if _, err := ParseConfig(raw, map[string]string{"ingress_address": value}); err == nil {
			t.Fatalf("accepted ingress address %q", value)
		}
	}
	if _, err := ParseConfig(raw, map[string]string{"ingress_class": "Not A Class"}); err == nil {
		t.Fatal("accepted an invalid ingress class")
	}
}

func TestDeleteRouteIngressLeavesOtherRoutes(t *testing.T) {
	c := fakeRouteClient(t)
	first, second := routeInput("route_1", "api.acme.dev"), routeInput("route_2", "www.acme.dev")
	first.BlockedPathPrefixes = []string{"/admin"}
	for _, in := range []RouteIngress{first, second} {
		if err := c.ApplyRouteIngress(t.Context(), in); err != nil {
			t.Fatal(err)
		}
	}
	for _, routeID := range []string{"route_1", "route_2"} {
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: RouteObjectName(routeID) + "-tls", Namespace: "selected", UID: types.UID("uid-" + routeID), Labels: map[string]string{LabelRoute: routeID}}}
		if _, err := c.Kube.CoreV1().Secrets("selected").Create(t.Context(), secret, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := c.DeleteRouteIngress(t.Context(), "route_1"); err != nil {
			t.Fatal(err)
		}
	}
	gone := RouteObjectName("route_1")
	if _, err := c.Kube.NetworkingV1().Ingresses("selected").Get(t.Context(), gone, metav1.GetOptions{}); err == nil {
		t.Fatal("Ingress survived")
	}
	if _, err := c.Dynamic.Resource(certificateGVR).Namespace("selected").Get(t.Context(), gone, metav1.GetOptions{}); err == nil {
		t.Fatal("Certificate survived")
	}
	if _, err := c.Kube.CoreV1().Secrets("selected").Get(t.Context(), gone+"-tls", metav1.GetOptions{}); err == nil {
		t.Fatal("TLS Secret survived")
	}
	kept := RouteObjectName("route_2")
	getIngress(t, c, kept)
	getCertificate(t, c, kept)
	if _, err := c.Kube.CoreV1().Secrets("selected").Get(t.Context(), kept+"-tls", metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Kube.CoreV1().Services("selected").Get(t.Context(), RouteDenyService, metav1.GetOptions{}); err != nil {
		t.Fatal("shared deny Service was deleted", err)
	}
	sawPrecondition := false
	for _, action := range c.Kube.(*fake.Clientset).Actions() {
		if d, ok := action.(kubetesting.DeleteAction); ok {
			if d.GetName() != gone && d.GetName() != gone+"-tls" {
				t.Fatalf("deleted %s", d.GetName())
			}
			if d.GetDeleteOptions().Preconditions == nil || d.GetDeleteOptions().Preconditions.UID == nil {
				t.Fatalf("delete without UID precondition: %+v", d)
			}
			sawPrecondition = true
		}
	}
	if !sawPrecondition {
		t.Fatal("no typed deletes were sent")
	}

	// Objects under this route's names that it does not own stay put.
	foreignSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: gone + "-tls", Namespace: "selected"}}
	foreignIngress := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: gone, Namespace: "selected", Labels: map[string]string{LabelRoute: "route_9"}}}
	c = fakeRouteClient(t, foreignSecret, foreignIngress)
	if err := c.DeleteRouteIngress(t.Context(), "route_1"); err != nil {
		t.Fatal(err)
	}
	getIngress(t, c, gone)
	if _, err := c.Kube.CoreV1().Secrets("selected").Get(t.Context(), gone+"-tls", metav1.GetOptions{}); err != nil {
		t.Fatal("foreign Secret was deleted")
	}
}
