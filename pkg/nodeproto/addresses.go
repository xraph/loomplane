package nodeproto

import (
	"net/netip"
	"slices"
	"strings"
)

// notPublic are ranges netip counts as global unicast that can't be a
// node's public address: this network, CGNAT, IETF protocol assignments,
// benchmarking, the reserved 240/4, NAT64, 6to4 and IPv4-compatible IPv6.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("::/96"),
}

// PublicAddress reports whether a could be a machine's public address:
// global unicast, not private (RFC 1918 or IPv6 unique local), and outside
// every range in notPublic. Loopback, link-local, multicast and unspecified
// addresses aren't global unicast, so they are left out too.
func PublicAddress(a netip.Addr) bool {
	a = a.Unmap()
	if a.Zone() != "" || !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// PublicAddresses keeps the candidates that could be public addresses,
// IPv4 first and then IPv6, each once, in the order given. Anything that
// isn't an IP address is left out.
func PublicAddresses(candidates []string) []string {
	var v4, v6 []string
	for _, c := range candidates {
		a, err := netip.ParseAddr(strings.TrimSpace(c))
		if err != nil || !PublicAddress(a) {
			continue
		}
		a = a.Unmap()
		s := a.String()
		if a.Is4() && !slices.Contains(v4, s) {
			v4 = append(v4, s)
		} else if a.Is6() && !slices.Contains(v6, s) {
			v6 = append(v6, s)
		}
	}
	return append(v4, v6...)
}
