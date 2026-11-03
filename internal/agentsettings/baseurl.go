package agentsettings

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

// lookupTimeout bounds a base URL's host lookup.
const lookupTimeout = 5 * time.Second

// nonPublic are ranges that aren't somewhere on the internet, beyond what
// netip's Is* methods cover: shared address space, benchmarking, the
// documentation ranges, 6to4 and NAT64 (which can reach private IPv4),
// IPv4-compatible IPv6, the reserved block and IPv6 site-local, which
// netip counts as global.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

// metadata are cloud instance metadata and host agent addresses outside
// the link-local ranges: AWS's IPv6 one, Alibaba Cloud's and Azure's
// WireServer. Like link-local addresses, they're refused everywhere.
var metadata = []netip.Prefix{
	netip.MustParsePrefix("fd00:ec2::254/128"), netip.MustParsePrefix("100.100.100.200/32"),
	netip.MustParsePrefix("168.63.129.16/32"),
}

// hostOnly reports whether a is link-local or a cloud metadata address:
// one that answers only the machine asking, and can hand out its
// credentials.
func hostOnly(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() {
		return true
	}
	for _, p := range metadata {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// public reports whether a is an address on the internet.
func public(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// systemLookup resolves host with the system resolver.
func systemLookup(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// policy is where the agent may send a workspace key and its requests.
// The check when a key is chosen and the dial guard share it, resolver
// included, so they can't disagree.
type policy struct {
	environment string
	// denied is planning.endpoint_denied_cidrs, with IPv4-mapped prefixes
	// written as the IPv4 prefixes they are.
	denied []netip.Prefix
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

func newPolicy(environment string, denied []netip.Prefix, lookup func(ctx context.Context, host string) ([]netip.Addr, error)) policy {
	if lookup == nil {
		lookup = systemLookup
	}
	out := make([]netip.Prefix, 0, len(denied))
	for _, p := range denied {
		out = append(out, unmapPrefix(p))
	}
	return policy{environment: environment, denied: out, lookup: lookup}
}

// unmapPrefix writes an IPv4-mapped IPv6 prefix (::ffff:10.0.0.0/104) as
// the IPv4 prefix it covers (10.0.0.0/8). Addresses are unmapped before
// they're matched, so a mapped prefix would otherwise never match.
func unmapPrefix(p netip.Prefix) netip.Prefix {
	if p.Addr().Is4In6() && p.Bits() >= 96 {
		return netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96).Masked()
	}
	return p
}

// deniedContains reports whether a falls in a denied prefix, in either of
// its forms: a short IPv6 prefix can still cover the mapped form of an
// IPv4 address.
func (p policy) deniedContains(a netip.Addr) bool {
	a = a.Unmap()
	mapped := netip.AddrFrom16(a.As16())
	for _, d := range p.denied {
		if d.Contains(a) || (a.Is4() && d.Contains(mapped)) {
			return true
		}
	}
	return false
}

// checkURL checks raw without looking anything up: an absolute http(s)
// URL without credentials, https in production. It returns the host.
func (p policy) checkURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.Opaque != "" {
		return "", invalid("the provider key's base_url must be an absolute http or https URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", invalid("the provider key's base_url must be an absolute http or https URL")
	}
	if u.User != nil {
		return "", invalid("the provider key's base_url can't carry credentials. Put the key in api_key")
	}
	if scheme != "https" && p.environment == config.EnvProduction {
		return "", invalid("the agent sends its key to the provider key's base_url, so in production it must be https")
	}
	return strings.TrimSuffix(u.Hostname(), "."), nil
}

// checkAddr decides whether the agent may connect to a: never a denied
// range, a link-local or a cloud metadata address, and outside
// development never a private, loopback or other non-public one.
func (p policy) checkAddr(a netip.Addr) error {
	a = a.Unmap()
	if p.deniedContains(a) {
		return invalid("the provider key's base_url reaches a network this server refuses (planning.endpoint_denied_cidrs)")
	}
	if hostOnly(a) {
		return invalid("the provider key's base_url reaches a link-local or cloud metadata address, which the agent never calls")
	}
	if p.environment != config.EnvDevelopment && !public(a) {
		return invalid("the provider key's base_url reaches a private or loopback address, which the agent can't call outside development")
	}
	return nil
}

// resolve returns host's addresses: itself when it's an address, else
// what the policy's resolver answers. An empty answer is an error.
func (p policy) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	addrs, err := p.lookup(lctx, host)
	if err != nil || len(addrs) == 0 {
		return nil, invalid("Loomplane couldn't look up the provider key's base_url host. Check the address and try again")
	}
	return addrs, nil
}

// checkBaseURL decides whether the agent may send a workspace key, and
// its requests, to raw. It must be an absolute http(s) URL without
// credentials, https in production, and its host's every address outside
// planning.endpoint_denied_cidrs, link-local ranges and cloud metadata
// addresses, and except in development (where a local model server is
// common) outside private, loopback and other non-public ranges. Messages
// never quote the URL: it's the user's and could hold anything.
//
// It runs when a key is chosen for the agent. Each call checks the URL
// again without a lookup (checkBaseURLStatic), and the dial guard checks
// every address nexus dials, so a name that resolves somewhere else later
// is refused then.
func (s *Service) checkBaseURL(ctx context.Context, raw string) error {
	host, err := s.policy.checkURL(raw)
	if err != nil {
		return err
	}
	addrs, err := s.policy.resolve(ctx, host)
	if err != nil {
		return err
	}
	for _, a := range addrs {
		if err := s.policy.checkAddr(a); err != nil {
			return err
		}
	}
	return nil
}

// checkBaseURLStatic is checkBaseURL without a lookup: the URL's shape,
// and its host when the host is an address. Each call runs it; where a
// name resolves is the dial guard's to check, at dial.
func (s *Service) checkBaseURLStatic(raw string) error {
	host, err := s.policy.checkURL(raw)
	if err != nil {
		return err
	}
	if a, perr := netip.ParseAddr(host); perr == nil {
		return s.policy.checkAddr(a)
	}
	return nil
}
