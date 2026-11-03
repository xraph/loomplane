package repositories

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"
)

// ErrPrivateAddress means a connection was refused because the address is
// one only a private network can reach.
var ErrPrivateAddress = errors.New("that address is on a private network, which Loomplane won't connect to")

// dialTimeout bounds one connection attempt.
const dialTimeout = 10 * time.Second

// sharedAddressSpace is the carrier-grade NAT range, 100.64.0.0/10, which
// cloud providers use for their internal services. benchmarking,
// 198.18.0.0/15, is for testing networks and never public. localNAT64,
// 64:ff9b:1::/48, is a NAT64 prefix a network sets up for itself, which may
// put its IPv4 address anywhere, so all of it is refused.
var (
	sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")
	thisNetwork        = netip.MustParsePrefix("0.0.0.0/8")
	benchmarking       = netip.MustParsePrefix("198.18.0.0/15")
	nat64              = netip.MustParsePrefix("64:ff9b::/96")
	localNAT64         = netip.MustParsePrefix("64:ff9b:1::/48")
	sixToFour          = netip.MustParsePrefix("2002::/16")
)

// PublicDialer returns a dial function for an http.Transport that connects
// only to public addresses. The check runs on the address the connection is
// about to use, after the name is resolved, so a name that resolves to a
// private address, a redirect to one and a DNS rebind between a check and
// the connection are all refused with ErrPrivateAddress.
func PublicDialer() func(ctx context.Context, network, address string) (net.Conn, error) {
	d := &net.Dialer{Timeout: dialTimeout, Control: refusePrivate}
	return d.DialContext
}

// refusePrivate is a net.Dialer.Control. address is the resolved ip:port.
func refusePrivate(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: can't read %q as an address", ErrPrivateAddress, address)
	}
	if privateAddr(ap.Addr()) {
		return ErrPrivateAddress
	}
	return nil
}

// privateAddr reports whether a is loopback, private, link-local,
// unspecified, multicast, in the shared address space, for benchmarking, or
// in a local-use NAT64 prefix. An IPv4 address carried inside an IPv6 one
// is judged as the IPv4 address it is.
func privateAddr(a netip.Addr) bool {
	a = a.WithZone("").Unmap()
	switch {
	case !a.IsValid():
		return true
	case a.Is6() && localNAT64.Contains(a):
		return true
	case a.Is6() && nat64.Contains(a):
		return privateAddr(embeddedIPv4(a, 12))
	case a.Is6() && sixToFour.Contains(a):
		return privateAddr(embeddedIPv4(a, 2))
	}
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() ||
		sharedAddressSpace.Contains(a) || thisNetwork.Contains(a) || benchmarking.Contains(a)
}

// embeddedIPv4 reads the four bytes of a at offset.
func embeddedIPv4(a netip.Addr, offset int) netip.Addr {
	b := a.As16()
	return netip.AddrFrom4([4]byte(b[offset : offset+4]))
}
