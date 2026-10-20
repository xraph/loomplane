package workloadidentity

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"

	domain "github.com/xraph/loomplane/pkg/federation"
)

// Source follows Workload API rotation without writing private keys to disk.
// The expected identity comes from the enrollment record; additional SVIDs on
// the socket cannot silently become the observer's identity.
type Source struct {
	source   *workloadapi.X509Source
	expected spiffeid.ID
}

func OpenSource(ctx context.Context, address, expectedURI string) (*Source, error) {
	socket, err := url.Parse(address)
	if err != nil || socket.Scheme != "unix" || socket.Host != "" || socket.User != nil || socket.RawQuery != "" || socket.Fragment != "" || !filepath.IsAbs(socket.Path) || filepath.Clean(socket.Path) != socket.Path || socket.RawPath != "" {
		return nil, ErrIdentity
	}
	expected, err := spiffeid.FromString(expectedURI)
	if err != nil {
		return nil, ErrIdentity
	}
	source, err := workloadapi.NewX509Source(ctx, workloadapi.WithClientOptions(workloadapi.WithAddr(address)), workloadapi.WithDefaultX509SVIDPicker(func(svids []*x509svid.SVID) *x509svid.SVID {
		for _, svid := range svids {
			if svid.ID == expected {
				return svid
			}
		}
		return nil
	}))
	if err != nil {
		return nil, err
	}
	out := &Source{source: source, expected: expected}
	if _, err := out.GetX509SVID(); err != nil {
		return nil, errors.Join(err, source.Close())
	}
	return out, nil
}
func (s *Source) GetX509SVID() (*x509svid.SVID, error) {
	svid, err := s.source.GetX509SVID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if svid.ID != s.expected || len(svid.Certificates) == 0 || !svid.Certificates[0].NotAfter.After(now) || svid.Certificates[0].NotBefore.After(now) || svid.Certificates[0].NotAfter.After(now.Add(MaxCredentialLifetime)) {
		return nil, ErrIdentity
	}
	return svid, nil
}
func (s *Source) GetX509BundleForTrustDomain(td spiffeid.TrustDomain) (*x509bundle.Bundle, error) {
	if td != s.expected.TrustDomain() {
		return nil, ErrIdentity
	}
	return s.source.GetX509BundleForTrustDomain(td)
}
func (s *Source) Close() error             { return s.source.Close() }
func (s *Source) Updated() <-chan struct{} { return s.source.Updated() }

// ClientTLS binds the peer to one control-plane SPIFFE ID. DNS names alone do
// not authenticate the control plane, and TLS session reuse cannot skip checks.
func (s *Source) ClientTLS(controlURI string) (*tls.Config, error) {
	peer, err := spiffeid.FromString(controlURI)
	if err != nil || peer.TrustDomain() != s.expected.TrustDomain() {
		return nil, ErrIdentity
	}
	agent, err := ParseAgentURI(s.expected.String())
	if err != nil {
		return nil, ErrIdentity
	}
	expected, err := ControlURI(agent)
	if err != nil || peer.String() != expected {
		return nil, ErrIdentity
	}
	config := tlsconfig.MTLSClientConfig(s, s, tlsconfig.AuthorizeID(peer))
	config.MinVersion = tls.VersionTLS13
	config.SessionTicketsDisabled = true
	return config, nil
}

func (s *Source) Domain() string { return s.expected.TrustDomain().Name() }

// ServerTLS accepts only agent identities in this control plane's organization.
// The runtime handler separately checks active or pending enrollment per request.
func (s *Source) ServerTLS(bundles x509bundle.Source) (*tls.Config, error) {
	const prefix = controlPathPrefix
	path := s.expected.Path()
	if bundles == nil || !strings.HasPrefix(path, prefix) || !domain.ValidIdentitySegment(strings.TrimPrefix(path, prefix)) {
		return nil, ErrIdentity
	}
	org := strings.TrimPrefix(path, prefix)
	authorize := func(id spiffeid.ID, _ [][]*x509.Certificate) error {
		if id.TrustDomain() != s.expected.TrustDomain() {
			return ErrIdentity
		}
		a, err := ParseAgentURI(id.String())
		if err != nil || a.OrgID != org {
			return ErrIdentity
		}
		return nil
	}
	config := tlsconfig.MTLSServerConfig(s, bundles, authorize)
	config.MinVersion = tls.VersionTLS13
	config.SessionTicketsDisabled = true
	return config, nil
}
