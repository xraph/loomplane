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
// IPv4-compatible IPv6 and the reserved block.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
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

// checkBaseURL decides whether the agent may send a workspace key, and
// its requests, to raw. It must be an absolute http(s) URL without
// credentials, https in production, and its host's every address outside
// planning.endpoint_denied_cidrs and, except in development, outside
// private, loopback, link-local and other non-public ranges. Messages
// never quote the URL: it's the user's and could hold anything.
//
// It runs when a key is chosen for the agent and again before every call,
// since what a name resolves to can change. The call's own dial is the
// last word: nexus dials the URL itself.
func (s *Service) checkBaseURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.Opaque != "" {
		return invalid("the provider key's base_url must be an absolute http or https URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return invalid("the provider key's base_url must be an absolute http or https URL")
	}
	if u.User != nil {
		return invalid("the provider key's base_url can't carry credentials. Put the key in api_key")
	}
	if scheme != "https" && s.environment == config.EnvProduction {
		return invalid("the agent sends its key to the provider key's base_url, so in production it must be https")
	}
	host := strings.TrimSuffix(u.Hostname(), ".")
	var addrs []netip.Addr
	if a, perr := netip.ParseAddr(host); perr == nil {
		addrs = []netip.Addr{a}
	} else {
		lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		defer cancel()
		addrs, err = s.lookup(lctx, host)
		if err != nil || len(addrs) == 0 {
			return invalid("Loomplane couldn't look up the provider key's base_url host. Check the address and try again")
		}
	}
	allowPrivate := s.environment == config.EnvDevelopment
	for _, a := range addrs {
		a = a.Unmap()
		for _, p := range s.denied {
			if p.Contains(a) {
				return invalid("the provider key's base_url reaches a network this server refuses (planning.endpoint_denied_cidrs)")
			}
		}
		if !allowPrivate && !public(a) {
			return invalid("the provider key's base_url reaches a private, loopback or link-local address, which the agent can't call outside development")
		}
	}
	return nil
}
