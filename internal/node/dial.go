package node

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"syscall"
	"time"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

const (
	// loomplaneLabel is the label every Loomplane network carries, the
	// owner org's ID. Its value is not checked here.
	loomplaneLabel = "loomplane.org"
	// labelledTTL is how long the dial guard keeps the addresses it read.
	labelledTTL = 5 * time.Second
	// labelledTimeout bounds reading them from the daemon.
	labelledTimeout = 5 * time.Second
	// dialTimeout bounds a connect. It stays under the hub's 30 second wait
	// for a verdict, so a dial that fails arrives as a refusal with its
	// reason rather than as a timeout.
	dialTimeout = 10 * time.Second
)

// deniedAddrError refuses an address the dial endpoint doesn't reach.
type deniedAddrError struct{ ip netip.Addr }

func (e *deniedAddrError) Error() string {
	return fmt.Sprintf("address %s is not a Loomplane container or loopback", e.ip)
}

// guardError is a check that could not be made. It refuses the address too.
type guardError struct{ reason string }

func (e *guardError) Error() string { return e.reason }

// DialHandler serves the dial endpoint with the daemon at socket. Target is
// host:port and Arg the network, tcp when empty; only TCP is dialled. The
// stream reaches loopback (127.0.0.0/8 and ::1) and the addresses running
// containers have on networks labelled loomplane.org, and nothing else: not
// the host's other addresses, the Docker bridge gateway or the metadata
// service. The check runs on each address the dialer is about to connect to,
// after any lookup and with IPv4-mapped IPv6 unmapped, so a name can't
// resolve to one address for the check and another for the connect. The
// connect happens before the verdict: a refused address or a failed connect
// is a refusal with its reason, as the SSH dialer returns an error.
func DialHandler(socket string) Handler {
	return newDialGuard(socket).serve
}

// dialGuard keeps the labelled containers' addresses for labelledTTL. One
// guard serves every dial stream, so they share its cache.
type dialGuard struct {
	api *http.Client
	now func() time.Time
	// connect dials address with d, whose Control runs the check. Tests
	// replace it to reach container addresses they can't route to.
	connect func(ctx context.Context, d *net.Dialer, network, address string) (net.Conn, error)

	mu  sync.Mutex
	at  time.Time
	ips map[netip.Addr]bool
}

func newDialGuard(socket string) *dialGuard {
	return &dialGuard{api: dockerAPI(socket), now: time.Now,
		connect: func(ctx context.Context, d *net.Dialer, network, address string) (net.Conn, error) {
			return d.DialContext(ctx, network, address)
		}}
}

func (g *dialGuard) serve(ctx context.Context, hdr nodeproto.StreamHeader, stream net.Conn) error {
	network := hdr.Arg
	switch network {
	case "":
		network = "tcp"
	case "tcp", "tcp4", "tcp6":
	default:
		return refusal("the dial endpoint dials only TCP, not " + sanitize(network))
	}
	if _, _, err := net.SplitHostPort(hdr.Target); err != nil {
		return refusal(fmt.Sprintf("dial target %s is not host:port", sanitize(hdr.Target)))
	}
	d := &net.Dialer{Timeout: dialTimeout, Control: func(_, address string, _ syscall.RawConn) error {
		return g.check(ctx, address)
	}}
	conn, err := g.connect(ctx, d, network, hdr.Target)
	if err != nil {
		// The guard's own errors are the reason; the dialer's prefix would
		// only repeat the target.
		var denied *deniedAddrError
		var guard *guardError
		switch {
		case errors.As(err, &denied):
			return refusal(denied.Error())
		case errors.As(err, &guard):
			return refusal(guard.reason)
		}
		return refusal(err.Error())
	}
	return pipe(stream, conn)
}

// check allows address, an ip:port the dialer is about to connect to, when
// its IP is loopback or a labelled container's.
func (g *dialGuard) check(ctx context.Context, address string) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return &guardError{reason: fmt.Sprintf("dialing %s: %v", sanitize(address), err)}
	}
	ip := ap.Addr().Unmap().WithZone("")
	if ip.IsLoopback() {
		return nil
	}
	ips, err := g.labelled(ctx)
	if err != nil {
		return &guardError{reason: "listing Loomplane containers: " + err.Error()}
	}
	if !ips[ip] {
		return &deniedAddrError{ip: ip}
	}
	return nil
}

// labelled returns the addresses, cached for labelledTTL. A failed read is
// not cached, so it fails only the dials that asked.
func (g *dialGuard) labelled(ctx context.Context) (map[netip.Addr]bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ips != nil && g.now().Sub(g.at) < labelledTTL {
		return g.ips, nil
	}
	ctx, cancel := context.WithTimeout(ctx, labelledTimeout)
	defer cancel()
	ips, err := labelledAddrs(ctx, g.api)
	if err != nil {
		return nil, err
	}
	g.ips, g.at = ips, g.now()
	return ips, nil
}

// labelledAddrs reads the networks labelled loomplane.org and the running
// containers, and returns each address a container has on one of those
// networks. A container's addresses on other networks are left out. The
// label and state are checked here as well as filtered by the daemon, so a
// daemon that drops the filter allows nothing more.
func labelledAddrs(ctx context.Context, api *http.Client) (map[netip.Addr]bool, error) {
	var networks []struct {
		ID     string `json:"Id"`
		Name   string
		Labels map[string]string
	}
	filter := url.Values{"filters": {`{"label":["` + loomplaneLabel + `"]}`}}
	if err := dockerGetJSON(ctx, api, "/networks?"+filter.Encode(), &networks); err != nil {
		return nil, err
	}
	ids := map[string]string{} // name to ID
	for _, n := range networks {
		if _, ok := n.Labels[loomplaneLabel]; ok && n.ID != "" && n.Name != "" {
			ids[n.Name] = n.ID
		}
	}
	var containers []struct {
		State           string
		NetworkSettings *struct {
			Networks map[string]struct {
				NetworkID, IPAddress, GlobalIPv6Address string
			}
		}
	}
	if err := dockerGetJSON(ctx, api, "/containers/json", &containers); err != nil {
		return nil, err
	}
	ips := map[netip.Addr]bool{}
	for _, c := range containers {
		if c.State != "running" || c.NetworkSettings == nil {
			continue
		}
		for name, ep := range c.NetworkSettings.Networks {
			id, ok := ids[name]
			if !ok || (ep.NetworkID != "" && ep.NetworkID != id) {
				continue
			}
			for _, s := range []string{ep.IPAddress, ep.GlobalIPv6Address} {
				if ip, err := netip.ParseAddr(s); err == nil {
					ips[ip.Unmap().WithZone("")] = true
				}
			}
		}
	}
	return ips, nil
}

// registerDial registers the dial endpoint when this node may use dial and
// has a socket for the guard to read containers from: socket when the caller
// already found it, or else the one registerDocker would find. It returns
// the socket, or "" when it left the registry alone, so dial streams are
// refused as unavailable.
func (r *Runtime) registerDial(ctx context.Context, socket string) string {
	cfg := r.live.Load()
	if !cfg.Allows(nodeproto.EndpointDial) {
		return ""
	}
	if socket == "" {
		socket = findSocket(ctx, cfg.DockerSocket, r.detectBase)
	}
	if socket == "" {
		r.log().Warn("loomplane-node: dial is allowed, but no Docker or Podman socket answered to say which containers it may reach, so this node refuses dial streams until it restarts")
		return ""
	}
	r.reg.Register(nodeproto.EndpointDial, DialHandler(socket))
	r.log().Info("loomplane-node: serving dial", "socket", socket)
	return socket
}
