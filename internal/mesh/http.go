// Package mesh binds private HTTP requests to verified workload identities and
// the agent's current signed policy. It does not provide a public gateway.
package mesh

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// SessionOpener is implemented by agent.SnapshotReceiver. Its durable enrollment
// check must remain in the request path when this guard is installed by an agent.
type SessionOpener interface {
	Open(context.Context, access.Request) (*access.Session, error)
}

// HTTPConfig binds one listener authority to one export and immutable handler.
// Handler must route only to that target and honor request cancellation. Route
// freshness, upstream identity and backend isolation are separate prerequisites.
type HTTPConfig struct {
	Trust     workloadidentity.TrustSource
	Policy    SessionOpener
	Target    domain.WorkloadSelector
	ExportID  string
	Port      uint16
	Authority string
	Handler   http.Handler
}

type HTTPGuard struct{ config HTTPConfig }

func NewHTTPGuard(config HTTPConfig) (*HTTPGuard, error) {
	if config.Trust == nil || config.Policy == nil || config.Handler == nil || !domain.ValidIdentitySegment(config.ExportID) || config.Port == 0 {
		return nil, access.ErrDenied
	}
	if backend, ok := config.Handler.(*loopbackHTTP); ok && backend.port != config.Port {
		return nil, access.ErrDenied
	}
	if _, err := workloadidentity.WorkloadURI(config.Trust.Domain(), config.Target); err != nil {
		return nil, access.ErrDenied
	}
	authority, err := url.Parse("https://" + config.Authority)
	if err != nil || config.Authority == "" || len(config.Authority) > 253 || authority.Host != config.Authority || authority.Hostname() == "" || authority.User != nil || authority.Path != "" || authority.RawQuery != "" || authority.ForceQuery || authority.Fragment != "" {
		return nil, access.ErrDenied
	}
	return &HTTPGuard{config: config}, nil
}

func (g *HTTPGuard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !g.canonical(r) || r.TLS == nil || !r.TLS.HandshakeComplete || r.TLS.Version < tls.VersionTLS13 {
		http.Error(w, "federation access denied", http.StatusForbidden)
		return
	}
	source, err := workloadidentity.VerifyWorkloadPeer(g.config.Trust, r.TLS.PeerCertificates, g.config.Target.OrgID, time.Now())
	if err != nil {
		http.Error(w, "federation access denied", http.StatusForbidden)
		return
	}
	// A reused TLS connection must not keep a request alive beyond its certificate.
	ctx, cancel := context.WithDeadline(r.Context(), r.TLS.PeerCertificates[0].NotAfter.Add(-domain.MaxClockSkew))
	defer cancel()
	session, err := g.config.Policy.Open(ctx, access.Request{Source: source, Target: g.config.Target, ExportID: g.config.ExportID, Port: g.config.Port, Protocol: domain.HTTP, IdentityProfile: domain.WorkloadIdentity, HTTPMethod: r.Method, HTTPPath: r.URL.Path})
	if err != nil {
		http.Error(w, "federation access denied", http.StatusForbidden)
		return
	}
	defer session.Close()
	forwarded := r.Clone(session.Context())
	for key := range forwarded.Header {
		lower := strings.ToLower(key)
		if lower == "forwarded" || lower == "x-real-ip" || strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "x-spiffe-") || strings.HasPrefix(lower, "x-envoy-") || strings.HasPrefix(lower, "x-loomplane-") {
			delete(forwarded.Header, key)
		}
	}
	g.config.Handler.ServeHTTP(w, forwarded)
}

func (g *HTTPGuard) canonical(r *http.Request) bool {
	if r.URL == nil || r.Host != g.config.Authority || r.URL.IsAbs() || r.URL.Host != "" || r.URL.User != nil || r.URL.Opaque != "" || r.URL.RawPath != "" || r.URL.Fragment != "" || !domain.ValidHTTPPath(r.URL.Path) || !domain.ValidHTTPMethod(r.Method) || r.Method == http.MethodConnect || r.Method == http.MethodTrace {
		return false
	}
	path, _, _ := strings.Cut(r.RequestURI, "?")
	return path == r.URL.Path && r.Header.Get("Upgrade") == "" && !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/grpc")
}

// ServerTLS follows Workload API rotation while pinning the server service URI.
// Every client must present a service SVID in this organization's private trust
// domain. HTTPGuard re-verifies that identity and its grant on every request.
func ServerTLS(source x509svid.Source, trust workloadidentity.TrustSource, target domain.WorkloadSelector) (*tls.Config, error) {
	if source == nil || trust == nil {
		return nil, workloadidentity.ErrIdentity
	}
	expected, err := workloadidentity.WorkloadURI(trust.Domain(), target)
	if err != nil {
		return nil, err
	}
	bound := serviceSource{source: source, expected: expected}
	if _, err = bound.GetX509SVID(); err != nil {
		return nil, err
	}
	authorize := func(id spiffeid.ID, _ [][]*x509.Certificate) error {
		selector, parseErr := workloadidentity.ParseWorkloadURI(id.String())
		if parseErr != nil || id.TrustDomain().Name() != trust.Domain() || selector.OrgID != target.OrgID {
			return workloadidentity.ErrIdentity
		}
		return nil
	}
	config := tlsconfig.MTLSServerConfig(bound, trust, authorize)
	config.MinVersion = tls.VersionTLS13
	config.SessionTicketsDisabled = true
	return config, nil
}

type serviceSource struct {
	source   x509svid.Source
	expected string
}

func (s serviceSource) GetX509SVID() (*x509svid.SVID, error) {
	svid, err := s.source.GetX509SVID()
	if err != nil || svid == nil || svid.ID.String() != s.expected || len(svid.Certificates) == 0 || svid.Certificates[0] == nil {
		return nil, workloadidentity.ErrIdentity
	}
	now := time.Now()
	cert := svid.Certificates[0]
	id, err := x509svid.IDFromCert(cert)
	if err != nil || id.String() != s.expected {
		return nil, workloadidentity.ErrIdentity
	}
	if cert.NotBefore.After(now) || !cert.NotAfter.After(now.Add(domain.MaxClockSkew)) || cert.NotAfter.After(now.Add(workloadidentity.MaxCredentialLifetime)) {
		return nil, workloadidentity.ErrIdentity
	}
	return svid, nil
}
