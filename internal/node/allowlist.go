package node

import (
	"slices"
	"sync"
	"sync/atomic"

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
		return c.IngressMode == nodeproto.IngressRelay || c.IngressMode == nodeproto.IngressDirect
	}
	return slices.Contains(c.Endpoints, e)
}

// Narrow keeps the endpoints that are both allowed now and in keep, plus any
// still in PendingAllowed, and returns the result. It never widens, so a
// stale Narrow that arrives after a narrower one changes nothing. A pending
// endpoint is one the owner just allowed and Loomplane has not acknowledged,
// so a Narrow made before it knew cannot take it away. A pending endpoint that
// is in keep is acknowledged and stops being pending.
func (c *Config) Narrow(keep []nodeproto.Endpoint) []nodeproto.Endpoint {
	next := make([]nodeproto.Endpoint, 0, len(c.Endpoints))
	for _, e := range c.Endpoints {
		if slices.Contains(keep, e) || slices.Contains(c.PendingAllowed, e) {
			next = append(next, e)
		}
	}
	c.Endpoints = next
	var pending []nodeproto.Endpoint
	for _, e := range c.PendingAllowed {
		if !slices.Contains(keep, e) && slices.Contains(next, e) {
			pending = append(pending, e)
		}
	}
	c.PendingAllowed = pending
	return next
}

// Acknowledge stops treating eps as pending: Loomplane has decided on them.
// What it kept is in the Narrow that carries them, which runs after this.
func (c *Config) Acknowledge(eps []nodeproto.Endpoint) {
	var pending []nodeproto.Endpoint
	for _, e := range c.PendingAllowed {
		if !slices.Contains(eps, e) {
			pending = append(pending, e)
		}
	}
	c.PendingAllowed = pending
}

// Live holds the node's current Config so the control loop can change it while
// streams are being served. It is copy on write: readers get a value and never
// see a half-updated slice.
type Live struct {
	mu sync.Mutex
	p  atomic.Pointer[Config]
}

// NewLive returns a Live holding a copy of c.
func NewLive(c Config) *Live {
	l := &Live{}
	c.Endpoints = slices.Clone(c.Endpoints)
	c.PendingAllowed = slices.Clone(c.PendingAllowed)
	l.p.Store(&c)
	return l
}

// Load returns a snapshot. Its Endpoints do not alias the stored slice.
func (l *Live) Load() Config {
	c := *l.p.Load()
	c.Endpoints = slices.Clone(c.Endpoints)
	c.PendingAllowed = slices.Clone(c.PendingAllowed)
	return c
}

// Update applies f to a copy and publishes it, then returns the new Config.
// Updates are serialized.
func (l *Live) Update(f func(*Config)) Config {
	l.mu.Lock()
	defer l.mu.Unlock()
	next := *l.p.Load()
	next.Endpoints = slices.Clone(next.Endpoints)
	next.PendingAllowed = slices.Clone(next.PendingAllowed)
	f(&next)
	stored := next
	stored.Endpoints = slices.Clone(next.Endpoints)
	stored.PendingAllowed = slices.Clone(next.PendingAllowed)
	l.p.Store(&stored)
	return next
}
