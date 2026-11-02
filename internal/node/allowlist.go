package node

import (
	"slices"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// Allows reports whether the node serves e. Control is always on. Public is on
// only when the ingress mode is relay or direct. Everything else must be in
// the allowlist.
func (c Config) Allows(e nodeproto.Endpoint) bool {
	switch e {
	case nodeproto.EndpointControl:
		return true
	case nodeproto.EndpointPublic:
		return c.IngressMode == "relay" || c.IngressMode == "direct"
	}
	return slices.Contains(c.Endpoints, e)
}

// Narrow keeps only the endpoints that are both allowed now and in keep, and
// returns the result. It never widens, so a stale Narrow that arrives after a
// narrower one changes nothing.
func (c *Config) Narrow(keep []nodeproto.Endpoint) []nodeproto.Endpoint {
	next := make([]nodeproto.Endpoint, 0, len(c.Endpoints))
	for _, e := range c.Endpoints {
		if slices.Contains(keep, e) {
			next = append(next, e)
		}
	}
	c.Endpoints = next
	return next
}
