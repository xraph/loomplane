package cloudinfra

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

// kubernetesRouteClient is what a route needs from its cluster.
type kubernetesRouteClient interface {
	CheckRoutePrerequisites(ctx context.Context) (kubernetes.RoutePrerequisites, error)
	EnsureHTTPIssuer(ctx context.Context, ingressClass, acmeDirectory string) (string, error)
	EnsureDNSIssuer(ctx context.Context, zoneID, zoneName string, token []byte, acmeDirectory string) (string, error)
	RemoveUnusedDNSIssuer(ctx context.Context, zoneID string) error
	ApplyRouteIngress(ctx context.Context, in kubernetes.RouteIngress) error
	RouteIngressStatus(ctx context.Context, routeID, serviceName string) (kubernetes.RouteIngressStatus, error)
	DeleteRouteIngress(ctx context.Context, routeID string) error
}

// routeNoticeError carries a message that is safe to show on the operation.
type routeNoticeError struct {
	err     error
	message string
}

func (e *routeNoticeError) Error() string { return e.message }
func (e *routeNoticeError) Unwrap() error { return e.err }

// executeKubernetesRoute drives the route's Ingress and Certificate, then DNS.
// DNS is written only once the Ingress reports the connection's address.
func (s *Service) executeKubernetesRoute(ctx context.Context, st DomainStore, op *RouteOperation, zone *Zone) error {
	route := &op.Route
	if op.Kind != "apply" && op.Kind != "verify" && op.Kind != "remove" {
		return &provider.UnsupportedCapabilityError{Provider: "kubernetes", Capability: provider.IngressManagement}
	}
	c, err := s.kubernetesRoutes(ctx, *route, op.Kind == "remove")
	if err != nil {
		return err
	}
	if op.Kind == "remove" {
		if checkErr := c.DeleteRouteIngress(ctx, route.ID); checkErr != nil {
			return checkErr
		}
		if isWildcard(route.Input.Hostname) {
			if checkErr := c.RemoveUnusedDNSIssuer(ctx, route.Input.ZoneID); checkErr != nil {
				return checkErr
			}
		}
		if checkErr := s.applyRouteDNS(ctx, st, op, zone); checkErr != nil {
			return checkErr
		}
		route.Status = "removed"
		route.UpdatedAt = s.now().UTC()
		return st.PublishRoute(ctx, op)
	}
	if op.Kind == "apply" {
		prerequisites, checkErr := c.CheckRoutePrerequisites(ctx)
		if checkErr != nil {
			return checkErr
		}
		if !prerequisites.Ready {
			return &routeNoticeError{err: ErrInvalid, message: "This cluster can't serve routes yet: " + strings.Join(prerequisites.Missing, ", ") + "."}
		}
		if route.Input.Port < 1 || route.Input.Port > 65535 {
			return ErrInvalid
		}
		issuer, checkErr := s.kubernetesRouteIssuer(ctx, c, *route, zone)
		if checkErr != nil {
			return checkErr
		}
		ingress := kubernetes.RouteIngress{RouteID: route.ID, OrgID: route.OrgID, ProjectID: route.ProjectID, EnvironmentID: route.Input.EnvironmentID, Hostname: route.Input.Hostname,
			ServiceName: route.Binding.ServiceName, ServicePort: int32(route.Input.Port), IngressClass: route.Binding.IngressClass, Issuer: issuer, BlockedPathPrefixes: slices.Clone(route.Input.BlockedPathPrefixes)}
		if applyErr := c.ApplyRouteIngress(ctx, ingress); applyErr != nil {
			return applyErr
		}
	}
	if op.Kind == "verify" && isWildcard(route.Input.Hostname) {
		// A check moves a replaced challenge token to the cluster.
		if _, checkErr := s.kubernetesRouteIssuer(ctx, c, *route, zone); checkErr != nil {
			return checkErr
		}
	}
	status, err := c.RouteIngressStatus(ctx, route.ID, route.Binding.ServiceName)
	if err != nil {
		return err
	}
	if op.Kind == "apply" && op.Receipt == nil {
		if mismatch := ingressAddressMismatch(status, route.Binding.Address); mismatch != "" {
			return &routeNoticeError{err: ErrPending, message: mismatch}
		}
		if checkErr := s.applyRouteDNS(ctx, st, op, zone); checkErr != nil {
			return checkErr
		}
	}
	route.Verification = s.verifyKubernetesRoute(ctx, *route, status)
	route.Status = "waiting"
	if route.Verification.DNS && route.Verification.TLS && route.Verification.Backend {
		route.Status = "ready"
	}
	route.UpdatedAt = s.now().UTC()
	if checkErr := st.PublishRoute(ctx, op); checkErr != nil {
		return checkErr
	}
	if route.Status != "ready" {
		return ErrPending
	}
	return nil
}

// kubernetesRouteIssuer keeps the Issuer a route's Certificate names. A
// wildcard uses its zone's DNS-01 Issuer, whose token passes the challenge
// checks again before every write; the cluster rewrites its Secret only
// when the token changed.
func (s *Service) kubernetesRouteIssuer(ctx context.Context, c kubernetesRouteClient, route Route, zone *Zone) (string, error) {
	if !isWildcard(route.Input.Hostname) {
		return c.EnsureHTTPIssuer(ctx, route.Binding.IngressClass, s.ACMEDirectory)
	}
	token, _, err := s.zoneChallengeCredential(ctx, zone)
	if err != nil {
		return "", &routeNoticeError{err: err, message: "The DNS challenge connection for " + zone.Name + " failed its checks, so Loomplane didn't put its token on the cluster: " + err.Error()}
	}
	return c.EnsureDNSIssuer(ctx, zone.ID, zone.Name, token, s.ACMEDirectory)
}

// ingressAddressMismatch says why DNS can't point at the declared address yet.
func ingressAddressMismatch(status kubernetes.RouteIngressStatus, address string) string {
	if len(status.Addresses) == 0 {
		return "Waiting for the ingress controller to publish an address."
	}
	if !slices.ContainsFunc(status.Addresses, func(reported string) bool { return sameAddress(reported, address) }) {
		return fmt.Sprintf("The ingress reports %s, but the connection declares %s. Update the Kubernetes connection's ingress address.", strings.Join(status.Addresses, ", "), address)
	}
	return ""
}

// sameAddress compares IPs by value and hostnames without case or a final dot.
func sameAddress(a, b string) bool {
	if ipA, ipB := net.ParseIP(a), net.ParseIP(b); ipA != nil || ipB != nil {
		return ipA.Equal(ipB)
	}
	return strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, "."))
}

func (s *Service) verifyKubernetesRoute(ctx context.Context, route Route, status kubernetes.RouteIngressStatus) RouteVerification {
	out := RouteVerification{CheckedAt: s.now().UTC(), CertState: kubernetesCertState(status.Certificate)}
	address := route.Binding.Address
	// A wildcard is checked through one name under it.
	hostname := routeProbeHostname(route)
	if net.ParseIP(address) != nil {
		addresses, err := s.domainResolver().LookupIPAddr(ctx, hostname)
		out.DNS = err == nil && len(addresses) > 0
		for _, resolved := range addresses {
			out.DNS = out.DNS && resolved.IP.Equal(net.ParseIP(address))
		}
	} else {
		out.DNS = s.cnameReaches(ctx, hostname, address)
	}
	if !out.DNS {
		out.DNSError = "DNS does not resolve exclusively to the connection's ingress address."
	}
	notAfter, code, err := s.routeHTTPS(ctx, hostname)
	if err == nil {
		out.CertExpires = notAfter
		out.TLS = notAfter.After(s.now().Add(24 * time.Hour))
	}
	if !out.TLS {
		out.TLSError = "A trusted certificate for this hostname with at least 24 hours remaining is not available."
		if status.Certificate.Failed && status.Certificate.Message != "" {
			out.TLSError = "The certificate could not be issued: " + truncate(status.Certificate.Message, 300)
		}
	}
	mismatch := ingressAddressMismatch(status, address)
	out.Backend = err == nil && status.BackendReady && mismatch == "" && code != http.StatusBadGateway && code != http.StatusServiceUnavailable && code != http.StatusGatewayTimeout
	switch {
	case out.Backend:
	case mismatch != "":
		out.BackendError = mismatch
	case !status.BackendReady:
		out.BackendError = "The Service has no ready endpoints."
	default:
		out.BackendError = "The hostname did not answer an HTTPS request without a gateway error."
	}
	return out
}

// cnameReaches reports whether the hostname's canonical name is the ingress
// address or the name the address itself resolves through.
func (s *Service) cnameReaches(ctx context.Context, hostname, address string) bool {
	resolver, ok := s.domainResolver().(interface {
		LookupCNAME(context.Context, string) (string, error)
	})
	if !ok {
		return false
	}
	target, err := resolver.LookupCNAME(ctx, hostname)
	if err != nil {
		return false
	}
	target = strings.TrimSuffix(strings.ToLower(target), ".")
	if target == address {
		return true
	}
	canonical, err := resolver.LookupCNAME(ctx, address)
	return err == nil && target == strings.TrimSuffix(strings.ToLower(canonical), ".")
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

// kubernetesCertState reads a certificate that is still valid during a
// reissue as ready.
func kubernetesCertState(c kubernetes.CertificateStatus) string {
	switch {
	case c.Failed:
		return "failed"
	case c.Ready:
		return "ready"
	case c.Issuing:
		return "issuing"
	}
	return ""
}

// probeRouteHTTPS requests the hostname's root over HTTPS without a proxy or
// redirects, and returns the leaf certificate's expiry and the status code.
func probeRouteHTTPS(ctx context.Context, hostname string) (time.Time, int, error) {
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 15 * time.Second}).DialContext, TLSHandshakeTimeout: 15 * time.Second, DisableKeepAlives: true,
		TLSClientConfig: &tls.Config{ServerName: hostname, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+hostname+"/", http.NoBody)
	if err != nil {
		return time.Time{}, 0, err
	}
	response, err := client.Do(request)
	if err != nil {
		return time.Time{}, 0, err
	}
	defer response.Body.Close()
	if response.TLS == nil || len(response.TLS.PeerCertificates) == 0 {
		return time.Time{}, 0, errors.New("the hostname presented no certificate")
	}
	return response.TLS.PeerCertificates[0].NotAfter, response.StatusCode, nil
}

// openKubernetesRoute connects to the route's namespace only while the
// connection still names the cluster and namespace the route was accepted on.
// Cleanup keeps working after the credential rotates.
func (s *Service) openKubernetesRoute(ctx context.Context, route Route, cleanup bool) (kubernetesRouteClient, error) {
	conn, raw, err := s.Connections.Credential(ctx, route.OrgID, route.Binding.ConnectionID)
	if err != nil {
		return nil, err
	}
	if conn.OrgID != route.OrgID || conn.Cloud != connections.Kubernetes || (!cleanup && conn.Generation != route.Binding.ConnectionGeneration) {
		return nil, ErrConflict
	}
	if route.Binding.RuntimeID == "" || conn.Details["namespace_uid"] != route.Binding.RuntimeID || conn.Details["namespace"] != route.Binding.Namespace {
		return nil, provider.ErrInfrastructureIdentity
	}
	cfg, err := kubernetes.ParseConfig(raw, conn.Details)
	if err != nil {
		return nil, err
	}
	if cfg.Namespace != route.Binding.Namespace || cfg.Details["endpoint"] != conn.Details["endpoint"] || cfg.Details["cluster_ca"] != conn.Details["cluster_ca"] {
		return nil, provider.ErrInfrastructureIdentity
	}
	maps.Copy(cfg.Details, conn.Details)
	client, err := kubernetes.New(cfg)
	if err != nil {
		return nil, err
	}
	namespace, err := client.Kube.CoreV1().Namespaces().Get(ctx, cfg.Namespace, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if string(namespace.UID) != route.Binding.RuntimeID {
		return nil, provider.ErrInfrastructureIdentity
	}
	return client, nil
}
