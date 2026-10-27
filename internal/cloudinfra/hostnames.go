package cloudinfra

import (
	"errors"
	"strings"

	"github.com/xraph/loomplane/internal/clouds/server"
)

// ErrWildcardUnsupported refuses a wildcard route the zone can't issue a
// certificate for.
var ErrWildcardUnsupported = errors.New("wildcard routes need a verified Cloudflare zone with a DNS challenge credential")

// wildcardRefusalError is ErrWildcardUnsupported with the reason a person reads.
type wildcardRefusalError string

func (e wildcardRefusalError) Error() string { return string(e) }
func (wildcardRefusalError) Unwrap() error   { return ErrWildcardUnsupported }

// RouteHostname is a route's hostname once validated. Base is the name a
// wildcard covers the labels of, or the hostname itself.
type RouteHostname struct {
	Hostname string
	Wildcard bool
	Base     string
}

// ValidateRouteHostname admits what a route may serve. Without a * it is
// NormalizeHostname inside the zone. A wildcard is *. and an exact name
// inside the zone, and only on a verified zone that allows wildcards.
// Claims still go through NormalizeHostname, which refuses every *.
func ValidateRouteHostname(zone *Zone, value string) (RouteHostname, error) {
	value = strings.TrimSpace(value)
	rest, wildcard := strings.CutPrefix(value, "*.")
	if !wildcard {
		host, err := NormalizeHostname(value)
		if err != nil {
			return RouteHostname{}, err
		}
		if _, err = zoneRelative(host, zone.Name); err != nil {
			return RouteHostname{}, err
		}
		return RouteHostname{Hostname: host, Base: host}, nil
	}
	if strings.Contains(rest, "*") {
		return RouteHostname{}, ErrInvalid
	}
	base, err := NormalizeHostname(rest)
	if err != nil || len(base) > 251 {
		return RouteHostname{}, ErrInvalid
	}
	if _, err = zoneRelative(base, zone.Name); err != nil {
		return RouteHostname{}, err
	}
	if !zone.verified() || !zone.Wildcards() {
		return RouteHostname{}, ErrWildcardUnsupported
	}
	return RouteHostname{Hostname: "*." + base, Wildcard: true, Base: base}, nil
}

func isWildcard(hostname string) bool { return strings.HasPrefix(hostname, "*.") }

// routeProbeHostname is the name a route is checked through: its DNS
// lookup, TLS server name and the proxy probe's Host header.
func routeProbeHostname(route Route) string {
	return server.IngressProbeHostname(route.ID, route.Input.Hostname)
}
