package deployments

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// errLoomplaneNetwork is an address a check from Loomplane's own network
// doesn't connect to.
var errLoomplaneNetwork = errors.New("an address Loomplane doesn't connect to from its own network")

// dialGuardTTL is how long a DialGuard keeps the addresses it looked up.
const dialGuardTTL = time.Minute

// dialGuardLookupTimeout bounds looking up one service's host.
const dialGuardLookupTimeout = 2 * time.Second

// nat64 are the NAT64 prefixes whose addresses reach the IPv4 address in
// their last 32 bits through a NAT64 gateway: the well-known prefix
// (RFC 6052), where 64:ff9b::a.b.c.d reaches a.b.c.d, and the local-use
// prefix (RFC 8215), read the same way.
var nat64 = []netip.Prefix{netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48")}

// awsMetadata is AWS's instance metadata service over IPv6. Its IPv4
// address, 169.254.169.254, is link-local; this one isn't.
var awsMetadata = netip.MustParseAddr("fd00:ec2::254")

// DialGuard decides what an endpoint check dialed from Loomplane's own
// network may connect to. Its own host is always refused (see
// loomplaneDials). Beyond that it refuses the services Loomplane itself
// depends on, at their ports, and an operator's denied ranges at any port.
// Private ranges stay dialable: real endpoints live there.
//
// A nil *DialGuard refuses only Loomplane's own host.
type DialGuard struct {
	// Denied are ranges refused at any port, such as an operator's
	// management network.
	Denied []netip.Prefix
	// Services are the host:port addresses Loomplane depends on: its
	// database, its own API and its provider plugins. A host is looked up
	// when it's needed and kept for a minute. An empty or unspecified
	// host, as in ":8780", is every address of this host.
	Services []string
	// Lookup resolves a service's host. Nil is the system resolver.
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	// Local lists this host's own addresses. Nil is every interface's.
	Local func() ([]netip.Addr, error)

	now func() time.Time

	mu         sync.Mutex
	at         time.Time
	resolved   map[string][]netip.AddrPort
	refreshing bool // a check is looking the services up again
}

// Dial dials an endpoint from Loomplane's own network. The guard runs on
// each address the dialer actually connects to, after the lookup, so a
// name that resolves to a refused address is refused like the address.
func (g *DialGuard) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	denied := g.services(ctx)
	d := &net.Dialer{Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil || !g.permits(denied, ap) {
			return errLoomplaneNetwork
		}
		return nil
	}}
	conn, err := d.DialContext(ctx, network, address)
	if errors.Is(err, errLoomplaneNetwork) {
		host, _, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			host = address
		}
		return nil, fmt.Errorf("%s resolves to %w", host, errLoomplaneNetwork)
	}
	return conn, err
}

// allows reports whether a check may connect to ap.
func (g *DialGuard) allows(ctx context.Context, ap netip.AddrPort) bool {
	return g.permits(g.services(ctx), ap)
}

// permits reports whether a check may connect to ap, given the addresses
// of the services Loomplane depends on.
func (g *DialGuard) permits(services map[netip.AddrPort]bool, ap netip.AddrPort) bool {
	for _, ip := range dialForms(ap.Addr()) {
		if !loomplaneDials(ip) || services[netip.AddrPortFrom(ip, ap.Port())] {
			return false
		}
		if g == nil {
			continue
		}
		for _, p := range g.Denied {
			if p.Contains(ip) {
				return false
			}
		}
	}
	return true
}

// dialForms are the addresses ip reaches: itself, unmapped, and the IPv4
// address inside a NAT64 one.
func dialForms(ip netip.Addr) []netip.Addr {
	ip = ip.Unmap()
	forms := []netip.Addr{ip}
	if slices.ContainsFunc(nat64, func(p netip.Prefix) bool { return p.Contains(ip) }) {
		b := ip.As16()
		forms = append(forms, netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
	}
	return forms
}

// loomplaneDials reports whether a check from Loomplane's own network may
// connect to ip: never its loopback, an unspecified address or "this
// network" (0.0.0.0/8), a link-local one such as a cloud metadata
// service, or AWS's IPv6 metadata service, in IPv4, IPv4-mapped or NAT64
// form.
func loomplaneDials(ip netip.Addr) bool {
	for _, ip := range dialForms(ip) {
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || (ip.Is4() && ip.As4()[0] == 0) || ip == awsMetadata {
			return false
		}
	}
	return true
}

// services is every address, with its port, of the services Loomplane
// depends on, looked up again once what's kept is a minute old. A failed
// lookup keeps what was known of that service before. Lookups run outside
// the lock: while one check looks the services up again, others use what
// was known. Only before anything is known does a check look them up
// itself.
func (g *DialGuard) services(ctx context.Context) map[netip.AddrPort]bool {
	if g == nil || len(g.Services) == 0 {
		return nil
	}
	now := time.Now
	if g.now != nil {
		now = g.now
	}
	g.mu.Lock()
	stale := g.resolved == nil || now().Sub(g.at) >= dialGuardTTL
	refresh := stale && (g.resolved == nil || !g.refreshing)
	if refresh {
		g.refreshing = true
	}
	g.mu.Unlock()
	if refresh {
		found := map[string][]netip.AddrPort{}
		for _, service := range g.Services {
			if addrs, err := g.resolve(ctx, service); err == nil {
				found[service] = addrs
			}
		}
		g.mu.Lock()
		if g.resolved == nil {
			g.resolved = map[string][]netip.AddrPort{}
		}
		maps.Copy(g.resolved, found)
		g.at, g.refreshing = now(), false
		g.mu.Unlock()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	all := map[netip.AddrPort]bool{}
	for _, addrs := range g.resolved {
		for _, ap := range addrs {
			for _, ip := range dialForms(ap.Addr()) {
				all[netip.AddrPortFrom(ip, ap.Port())] = true
			}
		}
	}
	return all
}

// resolve looks up one host:port service.
func (g *DialGuard) resolve(ctx context.Context, service string) ([]netip.AddrPort, error) {
	host, p, err := net.SplitHostPort(service)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(p, 10, 16)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	ip, ipErr := netip.ParseAddr(host)
	switch {
	case host == "" || (ipErr == nil && ip.IsUnspecified()):
		local := g.Local
		if local == nil {
			local = interfaceAddrs
		}
		if ips, err = local(); err != nil {
			return nil, err
		}
	case ipErr == nil:
		ips = []netip.Addr{ip}
	default:
		lookup := g.Lookup
		if lookup == nil {
			lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
				return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			}
		}
		lctx, cancel := context.WithTimeout(ctx, dialGuardLookupTimeout)
		defer cancel()
		if ips, err = lookup(lctx, host); err != nil {
			return nil, err
		}
	}
	addrs := make([]netip.AddrPort, 0, len(ips))
	for _, ip := range ips {
		addrs = append(addrs, netip.AddrPortFrom(ip.Unmap(), uint16(port)))
	}
	return addrs, nil
}

// interfaceAddrs is every address of this host's interfaces.
func interfaceAddrs() ([]netip.Addr, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	for _, a := range addrs {
		if p, perr := netip.ParsePrefix(a.String()); perr == nil {
			ips = append(ips, p.Addr())
		}
	}
	return ips, nil
}
