package node

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
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
	// labelledMissAge is how old a read must be before an address it
	// doesn't hold reads the containers again: a container that just
	// started isn't refused for labelledTTL, and a dial retried at a
	// refused address can't keep the daemon listing.
	labelledMissAge = 250 * time.Millisecond
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
// ip:port, or localhost:port for 127.0.0.1, and Arg the network, tcp when
// empty; only TCP is dialled. Nothing is looked up, so a dial can't make
// the node send a DNS query. The stream reaches loopback (127.0.0.0/8 and
// ::1) and the addresses running containers have on networks labelled
// loomplane.org, and nothing else: not the host's other addresses, the
// Docker bridge gateway, a Loomplane network's own gateway or the metadata
// service. The check runs, with IPv4-mapped IPv6 unmapped, on the address
// the dialer is about to connect to. The connect happens before the verdict:
// a refused address or a failed connect is a refusal with its reason, as the
// SSH dialer returns an error.
func DialHandler(socket string) Handler {
	return newDialGuard(socket).serve
}

// dialGuard keeps the labelled containers' addresses for labelledTTL. One
// guard serves every dial stream, so they share its cache and its one read
// in flight.
type dialGuard struct {
	api *http.Client
	now func() time.Time
	// connect dials address with d, whose Control runs the check. Tests
	// replace it to reach container addresses they can't route to.
	connect func(ctx context.Context, d *net.Dialer, network, address string) (net.Conn, error)

	mu  sync.Mutex
	at  time.Time
	ips map[netip.Addr]bool
	// reading is the read in flight, which every check that needs the
	// addresses waits on rather than starting its own.
	reading *labelledRead
}

// labelledRead is one read of the labelled addresses. done is closed once
// ips and err are set.
type labelledRead struct {
	done chan struct{}
	ips  map[netip.Addr]bool
	at   time.Time
	err  error
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
	address, refused := literalTarget(hdr.Target)
	if refused != nil {
		return refused
	}
	// The address is already an IP, so the dialer looks nothing up. The
	// check still runs on what it connects to, and within its deadline.
	d := &net.Dialer{Timeout: dialTimeout, ControlContext: func(ctx context.Context, _, address string, _ syscall.RawConn) error {
		return g.check(ctx, address)
	}}
	conn, err := g.connect(ctx, d, network, address)
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

// literalTarget is target as ip:port, with localhost read as 127.0.0.1, or
// the refusal for anything that would need a lookup: a name, an IPv4
// shorthand such as 127.1 that only getaddrinfo accepts, or a named port.
func literalTarget(target string) (string, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return "", refusal(fmt.Sprintf("dial target %s is not host:port", sanitize(target)))
	}
	if n, perr := strconv.ParseUint(port, 10, 16); perr != nil || n == 0 {
		return "", refusal(fmt.Sprintf("dial target %s has no port number", sanitize(target)))
	}
	if host == "localhost" {
		return net.JoinHostPort("127.0.0.1", port), nil
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "", refusal(fmt.Sprintf("dial target %s must be an IP address or localhost", sanitize(host)))
	}
	return net.JoinHostPort(ip.String(), port), nil
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
	ips, at, err := g.labelled(ctx, time.Time{})
	if err == nil && !ips[ip] && g.now().Sub(at) >= labelledMissAge {
		// The container may have started since the list was read.
		ips, _, err = g.labelled(ctx, at)
	}
	if err != nil {
		return &guardError{reason: "listing Loomplane containers: " + err.Error()}
	}
	if !ips[ip] {
		return &deniedAddrError{ip: ip}
	}
	return nil
}

// labelled returns the addresses and when they were read, cached for
// labelledTTL. The cache is a known, accepted risk: for up to labelledTTL a
// removed container's address stays allowed, until Docker hands it to
// another container. A cache read at or before stale doesn't count, so a
// check that missed in one list can ask for a newer one. When the cache is
// stale, the first check starts one read and every check waits on it for
// no longer than its own ctx allows, so a hung daemon holds no dial past
// its deadline; a check that wants a newer list joins a read already in
// flight, which started after the list it missed in. A failed read is not
// cached: it fails the checks that waited on it, and the next check reads
// again.
func (g *dialGuard) labelled(ctx context.Context, stale time.Time) (map[netip.Addr]bool, time.Time, error) {
	g.mu.Lock()
	if g.ips != nil && g.now().Sub(g.at) < labelledTTL && g.at.After(stale) {
		ips, at := g.ips, g.at
		g.mu.Unlock()
		return ips, at, nil
	}
	read := g.reading
	if read == nil {
		read = &labelledRead{done: make(chan struct{})}
		g.reading = read
		go g.read(read) //nolint:gosec // G118: the read is shared, so it must not end with the ctx of the check that started it
	}
	g.mu.Unlock()
	select {
	case <-read.done:
		return read.ips, read.at, read.err
	case <-ctx.Done():
		return nil, time.Time{}, ctx.Err()
	}
}

// read reads the addresses for every check waiting on r. It has its own
// deadline, since the check that started it may stop waiting first.
func (g *dialGuard) read(r *labelledRead) {
	ctx, cancel := context.WithTimeout(context.Background(), labelledTimeout)
	defer cancel()
	r.ips, r.err = labelledAddrs(ctx, g.api)
	g.mu.Lock()
	r.at = g.now()
	if r.err == nil {
		g.ips, g.at = r.ips, r.at
	}
	g.reading = nil
	g.mu.Unlock()
	close(r.done)
}

// labelledAddrs reads the networks labelled loomplane.org and the running
// containers, and returns each address a container has on one of those
// networks. A container's addresses on other networks are left out, and an
// endpoint counts only when it carries the network's ID: a name alone could
// belong to a network created after the list was read. The label and state
// are checked here as well as filtered by the daemon, so a daemon that drops
// the filter allows nothing more.
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
	labelled := map[string]bool{} // by network ID
	for _, n := range networks {
		if _, ok := n.Labels[loomplaneLabel]; ok && n.ID != "" {
			labelled[n.ID] = true
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
		for _, ep := range c.NetworkSettings.Networks {
			if ep.NetworkID == "" || !labelled[ep.NetworkID] {
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
