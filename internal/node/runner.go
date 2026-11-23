package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// A runner is a node with Role "runner". It serves two endpoints besides
// control and probe:
//
//   - firecracker: a Firecracker host, in this process, that runs only build
//     VMs, each build in a fresh microVM. Loomplane drives it with the same
//     calls it makes to a Firecracker host agent, over a node stream instead
//     of mutual TLS.
//   - relay: TCP from this machine's network to a host:port its owner
//     allowed in allow.json, so a deploy reaches a server, cluster or host
//     only the runner can. SSH and TLS run end to end through it, so the
//     runner sees only ciphertext.
//
// It never serves docker or dial, so it never runs a workload.

// errRunnerKVM is what a runner says when its host can't run microVMs.
var errRunnerKVM = errors.New("a runner builds every image in a Firecracker microVM and needs Linux, root, /dev/kvm and the Firecracker toolchain")

// registerRunner opens the runner's Firecracker host and registers its
// endpoints. The returned host is closed when the runner stops.
func (r *Runtime) registerRunner(ctx context.Context) (*fc.RunnerHost, error) {
	cfg := r.live.Load()
	if cfg.Role != RoleRunner {
		return nil, errors.New("this node isn't a runner")
	}
	if cfg.FirecrackerConfig == "" || cfg.PoolConnection == "" {
		return nil, errors.New("this runner has no Firecracker config or pool; join it again with loomctl runner start")
	}
	host, err := fc.OpenRunnerHost(ctx, cfg.FirecrackerConfig, cfg.NodeID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRunnerKVM, err)
	}
	go host.Reconcile(ctx)
	// A global runner's pool is the platform's: its builds run under the
	// pool's own ID, for any workspace the platform owner enabled, and it
	// reports what the platform policy checks.
	global := strings.HasPrefix(cfg.PoolConnection, "rpool_")
	handler := fc.Handler{Host: host.Host, Node: &fc.Peer{Org: cfg.OrgID, Connection: cfg.PoolConnection}, BuildsOnly: true, NodeAnyOrg: global,
		Capabilities: func(c fc.Capabilities) fc.Capabilities {
			smt, err := fc.SMTActive()
			c.SMTActive = err != nil || smt
			c.Platform = global
			return c
		}}
	r.reg.Register(nodeproto.EndpointFirecracker, HTTPStreamHandler(handler))
	r.reg.Register(nodeproto.EndpointRelay, RelayHandler(r.relayAllowed, nil))
	return host, nil
}

// relayAllowed reads the networks this runner may relay to from allow.json,
// each time, so an owner's `runner relay allow` takes effect for the next
// stream. Anything wrong with the file allows nothing.
func (r *Runtime) relayAllowed() []netip.Prefix {
	c, err := r.st.ReadCeiling(r.ceilingOwner)
	if err != nil || c.NodeID != r.live.Load().NodeID {
		return nil
	}
	return ParseRelayAllow(c.Relay)
}

// ParseRelayAllow keeps the valid CIDR prefixes in raw, masked.
func ParseRelayAllow(raw []string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range raw {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
		}
	}
	return out
}

// neverRelayed are addresses a relay never reaches, whatever the owner
// allowed: cloud metadata services, link-local, loopback and unspecified.
var neverRelayed = []netip.Prefix{
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fd00:ec2::254/128"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("::/128"),
}

// relayable reports whether a relay may reach addr under allowed.
func relayable(addr netip.Addr, allowed []netip.Prefix) bool {
	addr = addr.Unmap()
	for _, p := range neverRelayed {
		if p.Contains(addr) {
			return false
		}
	}
	for _, p := range allowed {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// relayDialTimeout bounds connecting to a relay's target.
const relayDialTimeout = 15 * time.Second

// RelayHandler serves the relay endpoint. It resolves the header's target,
// keeps only addresses allowed returns and never a metadata, link-local or
// loopback one, dials the first that answers and copies bytes both ways
// until either side closes. It dials the address it checked, so a name that
// resolves elsewhere later can't move the connection. dial replaces the
// network dialer, for tests; nil dials TCP.
func RelayHandler(allowed func() []netip.Prefix, dial func(ctx context.Context, address string) (net.Conn, error)) Handler {
	if dial == nil {
		dial = func(ctx context.Context, address string) (net.Conn, error) {
			return (&net.Dialer{Timeout: relayDialTimeout}).DialContext(ctx, "tcp", address)
		}
	}
	return func(ctx context.Context, hdr nodeproto.StreamHeader, stream net.Conn) error {
		host, portText, err := net.SplitHostPort(hdr.Target)
		if err != nil {
			return refusal("the relay target is not host:port")
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			return refusal("the relay target has no valid port")
		}
		var addrs []netip.Addr
		if a, parseErr := netip.ParseAddr(host); parseErr == nil {
			addrs = []netip.Addr{a}
		} else {
			lookup, cancel := context.WithTimeout(ctx, relayDialTimeout)
			addrs, err = net.DefaultResolver.LookupNetIP(lookup, "ip", host)
			cancel()
			if err != nil {
				return refusal(fmt.Sprintf("this runner couldn't resolve %s", sanitize(host)))
			}
		}
		permitted := allowed()
		var target net.Conn
		reached := false
		for _, a := range addrs {
			if !relayable(a, permitted) {
				continue
			}
			reached = true
			dialCtx, cancel := context.WithTimeout(ctx, relayDialTimeout)
			target, err = dial(dialCtx, netip.AddrPortFrom(a.Unmap(), uint16(port)).String()) //nolint:gosec // Validate bounds the port to 1-65535.
			cancel()
			if err == nil {
				break
			}
		}
		if !reached {
			return refusal(fmt.Sprintf("this runner may not relay to %s; its owner allows networks with: sudo loomctl runner relay allow <cidr>", sanitize(hdr.Target)))
		}
		if target == nil {
			return refusal(fmt.Sprintf("this runner couldn't reach %s", sanitize(hdr.Target)))
		}
		defer func() { _ = target.Close() }()
		if err := accept(stream); err != nil {
			return err
		}
		relaySplice(stream, target)
		return nil
	}
}

// relaySplice copies both ways until one side ends, then closes the other's
// writes so it ends too.
func relaySplice(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	copyHalf := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src) //nolint:errcheck // a relay ends when either side does
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite() //nolint:errcheck // the other side may be gone already
		} else {
			_ = dst.Close()
		}
	}
	go copyHalf(a, b)
	go copyHalf(b, a)
	wg.Wait()
}

// HTTPStreamHandler serves h over each stream, as one HTTP connection. It
// returns when the HTTP server is done with the stream, because Serve closes
// the stream as soon as a handler returns.
func HTTPStreamHandler(h http.Handler) Handler {
	listener := &streamListener{conns: make(chan net.Conn), done: make(chan struct{})}
	server := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 35 * time.Minute, WriteTimeout: 35 * time.Minute, IdleTimeout: time.Minute, MaxHeaderBytes: 64 << 10}
	var start sync.Once
	return func(ctx context.Context, _ nodeproto.StreamHeader, stream net.Conn) error {
		start.Do(func() { go func() { _ = server.Serve(listener) }() }) //nolint:errcheck // it serves until the process ends
		conn := &closeNotifyConn{Conn: stream, closed: make(chan struct{})}
		select {
		case listener.conns <- conn:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-conn.closed:
		case <-ctx.Done():
			_ = conn.Close()
		}
		return nil
	}
}

// streamListener hands node streams to an http.Server as connections.
type streamListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *streamListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *streamListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *streamListener) Addr() net.Addr { return streamAddr{} }

type streamAddr struct{}

func (streamAddr) Network() string { return "loomplane-node" }
func (streamAddr) String() string  { return "stream" }

// closeNotifyConn tells its handler when the HTTP server closes it.
type closeNotifyConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *closeNotifyConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}
