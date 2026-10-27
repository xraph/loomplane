package issuer

import (
	"net/netip"
	"net/url"
	"strings"
)

// developmentEnvironment is config.EnvDevelopment, spelled out so the
// issuer doesn't import config for one string.
const developmentEnvironment = "development"

// devOnly is the warning a development issuer on a loopback address gets.
const devOnly = "development only: no cloud can reach this issuer"

// sharedAddressSpace is 100.64.0.0/10, the carrier-grade NAT range, which
// netip.Addr.IsPrivate doesn't count.
var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

// privateSuffixes are names that never resolve on the public internet.
var privateSuffixes = []string{".localhost", ".local", ".internal", ".lan", ".home.arpa", ".test", ".invalid"}

// Usable says whether a cloud can use issuerURL as an OpenID Connect
// issuer. Clouds fetch discovery and keys themselves, so the issuer has to
// be HTTPS on a host the internet reaches, with no user name, password,
// path, query or fragment. In development an HTTP or HTTPS loopback issuer is
// allowed too, so keyless can be tried against fakes, and the reason says
// no real cloud can reach it. Otherwise reason names what's wrong.
func Usable(issuerURL, environment string) (ok bool, reason string) {
	issuerURL = strings.TrimSpace(issuerURL)
	if issuerURL == "" {
		return false, "public_url isn't set"
	}
	u, err := url.Parse(issuerURL)
	if err != nil || u.Host == "" {
		return false, "public_url isn't a URL"
	}
	// An issuer is compared with iss byte for byte and has paths added to
	// it, so it can't carry anything after its path. The reasons don't
	// repeat what's there: userinfo can hold a password.
	switch {
	case u.User != nil:
		return false, "public_url can't have a user name or password"
	case u.RawQuery != "" || u.ForceQuery:
		return false, "public_url can't have a query"
	case u.Fragment != "" || strings.Contains(issuerURL, "#"):
		return false, "public_url can't have a fragment"
	case strings.Trim(u.Path, "/") != "":
		// Discovery and the JWKS are served at the root, whatever the
		// public URL's path.
		return false, "public_url must not have a path for keyless"
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if environment == developmentEnvironment && loopback(host) && (u.Scheme == "http" || u.Scheme == "https") {
		return true, devOnly
	}
	if u.Scheme != "https" {
		return false, "public_url isn't HTTPS"
	}
	if !public(host) {
		return false, host + " isn't reachable from the internet"
	}
	return true, ""
}

func loopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.Unmap().IsLoopback()
}

// public says whether host looks like one the internet can reach. It
// doesn't resolve names: a name that's public in form but points
// somewhere private is the operator's to fix.
func public(host string) bool {
	if host == "" {
		return false
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		return addr.IsGlobalUnicast() && !addr.IsPrivate() && !sharedAddressSpace.Contains(addr)
	}
	if host == "localhost" || !strings.Contains(host, ".") {
		return false
	}
	for _, suffix := range privateSuffixes {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	return true
}
