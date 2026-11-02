package agentsettings

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"syscall"
	"time"

	"github.com/xraph/nexus/provider"
)

// Errors of the dial guard. Neither carries the URL.
var (
	// ErrDialRefused is a call to a workspace base URL whose host is, or
	// now resolves to, an address the agent may not reach.
	ErrDialRefused = errors.New("agentsettings: refused to connect to the workspace provider key's base_url")
	// ErrRedirectRefused is a workspace base URL that answered a call with
	// a redirect. The agent never follows one: the provider's own client
	// would, to wherever it points, carrying the key.
	ErrRedirectRefused = errors.New("agentsettings: the workspace provider key's base_url answered with a redirect, which the agent doesn't follow")
)

// Dial settings of the guarded transport, as http.DefaultTransport's.
const (
	guardDialTimeout = 30 * time.Second
	guardKeepAlive   = 30 * time.Second
)

// ProviderTransport wraps the transport a nexus provider sends through
// (nexus.WithProviderTransport) so a call to a base URL a workspace chose
// can only reach where checkBaseURL would let it.
//
// Such a call carries a provider.Credential with a BaseURL on its
// context. It goes through a transport of its own, with its own
// connections, that:
//
//   - looks the host up with the same resolver checkBaseURL uses, checks
//     every address with the same rules, and dials only those addresses,
//     checking each again in the dialer's Control as it connects;
//   - never uses a proxy, whose address the check would never see;
//   - refuses a redirect (ErrRedirectRefused) instead of letting the
//     provider's client follow it. The client is the provider's, so this
//     is done here, where http.Client.CheckRedirect returning
//     http.ErrUseLastResponse can't be set.
//
// Every other call goes through next as it was: the server's own
// ai.base_url, the embeddings provider and a provider's own API carry no
// workspace base URL, and the operator chose those.
func (s *Service) ProviderTransport(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	base, ok := next.(*http.Transport)
	if !ok {
		base, ok = http.DefaultTransport.(*http.Transport)
	}
	guarded := &http.Transport{}
	if ok {
		guarded = base.Clone()
	}
	guarded.Proxy = nil
	guarded.DialContext = s.policy.dial
	guarded.DialTLSContext = nil
	return &guardTransport{policy: s.policy, plain: next, guarded: guarded}
}

// guardTransport sends calls to a workspace base URL through guarded and
// everything else through plain.
type guardTransport struct {
	policy  policy
	plain   http.RoundTripper
	guarded *http.Transport
}

// RoundTrip implements http.RoundTripper.
func (t *guardTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	cred, ok := provider.CredentialFromContext(r.Context())
	if !ok || cred.BaseURL == "" {
		return t.plain.RoundTrip(r)
	}
	if _, err := t.policy.checkURL(r.URL.String()); err != nil {
		closeBody(r)
		return nil, fmt.Errorf("%w: %s", ErrDialRefused, sentence(err))
	}
	resp, err := t.guarded.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if redirect(resp) {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10)) //nolint:errcheck // draining for reuse only
		_ = resp.Body.Close()
		return nil, ErrRedirectRefused
	}
	return resp, nil
}

// CloseIdleConnections closes both transports' idle connections.
func (t *guardTransport) CloseIdleConnections() {
	t.guarded.CloseIdleConnections()
	if c, ok := t.plain.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// redirect reports whether resp is one http.Client would follow.
func redirect(resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return resp.Header.Get("Location") != ""
	}
	return false
}

// closeBody closes a request's body, as a RoundTripper must even when it
// fails.
func closeBody(r *http.Request) {
	if r.Body != nil {
		_ = r.Body.Close()
	}
}

// dial connects to address for a call to a workspace base URL. It looks
// the host up once, refuses the whole answer when any address in it is
// refused, and dials the checked addresses themselves, never the name, so
// what's dialed is what was checked. The dialer's Control checks each
// address again as it connects.
func (p policy) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDialRefused, err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("%w: a port that isn't one", ErrDialRefused)
	}
	addrs, err := p.resolve(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDialRefused, sentence(err))
	}
	for _, a := range addrs {
		if cerr := p.checkAddr(a); cerr != nil {
			return nil, fmt.Errorf("%w: %s", ErrDialRefused, sentence(cerr))
		}
	}
	d := &net.Dialer{Timeout: guardDialTimeout, KeepAlive: guardKeepAlive, Control: func(_, address string, _ syscall.RawConn) error {
		ap, perr := netip.ParseAddrPort(address)
		if perr != nil {
			return fmt.Errorf("%w: an address that isn't one", ErrDialRefused)
		}
		if cerr := p.checkAddr(ap.Addr()); cerr != nil {
			return fmt.Errorf("%w: %s", ErrDialRefused, sentence(cerr))
		}
		return nil
	}}
	var errs []error
	for _, a := range addrs {
		conn, derr := d.DialContext(ctx, network, netip.AddrPortFrom(a.Unmap(), uint16(port)).String())
		if derr == nil {
			return conn, nil
		}
		errs = append(errs, derr)
	}
	return nil, errors.Join(errs...)
}
