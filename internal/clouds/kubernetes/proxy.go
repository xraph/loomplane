package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
)

// checkServiceProxyPermissions reports "service_proxy" ready when the
// connection can reach a Service's ports through the API server's service
// proxy in its namespace, which is how Loomplane reaches a catalog
// component's control port. Without them the detail is absent; ordinary
// workloads don't need them.
func (c *Client) checkServiceProxyPermissions(ctx context.Context, out *Result) error {
	delete(out.Details, "service_proxy")
	for _, verb := range []string{"get", "create"} {
		allowed, err := c.allowed(ctx, "", "services/proxy", verb)
		if err != nil {
			return err
		}
		if !allowed {
			return nil
		}
	}
	out.Details["service_proxy"] = "ready"
	return nil
}

// ServiceProxyTransport sends requests to one port of a Service in the
// connection's namespace through the API server's service proxy, with the
// connection's own credential and trust. A request's scheme and host are
// replaced; its path, query, method and body are kept, so
// http://component/admin?x=1 reaches
// {apiserver}/api/v1/namespaces/{namespace}/services/{service}:{port}/proxy/admin?x=1.
// Like any RoundTripper, it never follows a redirect.
func (c *Client) ServiceProxyTransport(namespace, service string, port int) (http.RoundTripper, error) {
	if c == nil || c.Config == nil || c.Config.REST == nil {
		return nil, ErrInvalid
	}
	if namespace == "" || namespace != c.Config.Namespace {
		return nil, fmt.Errorf("%w: the service proxy reaches only the connection's namespace", ErrInvalid)
	}
	if len(validation.IsDNS1035Label(service)) > 0 || port < 1 || port > 65535 {
		return nil, fmt.Errorf("%w: no service port %s:%d", ErrInvalid, service, port)
	}
	base, _, err := rest.DefaultServerUrlFor(c.Config.REST)
	if err != nil {
		return nil, fmt.Errorf("read the Kubernetes API address: %w", err)
	}
	next, err := rest.TransportFor(c.Config.REST)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes transport: %w", err)
	}
	prefix := strings.TrimSuffix(base.Path, "/") + "/api/v1/namespaces/" + namespace + "/services/" + service + ":" + strconv.Itoa(port) + "/proxy"
	return &serviceProxy{next: next, scheme: base.Scheme, host: base.Host, prefix: prefix}, nil
}

// serviceProxy rewrites requests onto a Service's proxy path.
type serviceProxy struct {
	next                 http.RoundTripper
	scheme, host, prefix string
}

// errProxyAuthorization is a request that brings its own credential: it
// would replace the cluster credential, and the API server doesn't pass
// one on to the Service.
var errProxyAuthorization = errors.New("a request through the Kubernetes service proxy can't carry its own Authorization header")

func (p *serviceProxy) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Authorization") != "" {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, errProxyAuthorization
	}
	out := req.Clone(req.Context())
	target := *req.URL
	target.Scheme, target.Host, target.User = p.scheme, p.host, nil
	path := req.URL.Path
	if path == "" {
		path = "/"
	}
	target.Path = p.prefix + path
	if req.URL.RawPath != "" {
		target.RawPath = p.prefix + req.URL.RawPath
	}
	out.URL, out.Host = &target, ""
	return p.next.RoundTrip(out)
}

var _ http.RoundTripper = (*serviceProxy)(nil)
