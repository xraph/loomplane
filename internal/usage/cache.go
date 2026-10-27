package usage

import (
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// maxEntries is when the cache sweeps out what's expired.
const maxEntries = 4096

// cache keeps what clouds answered for a while, and makes concurrent
// askers of one key share one read. Callers authorize before they ask, so
// it holds only reads and never decides who sees them.
type cache struct {
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]entry
	flight  singleflight.Group
}

type entry struct {
	value any
	err   error
	until time.Time
}

func newCache(now func() time.Time) *cache {
	return &cache{now: now, entries: map[string]entry{}}
}

// get returns what's kept for key, or loads it. A value is kept for ttl,
// an error for failureTTL.
func (c *cache) get(key string, ttl time.Duration, load func() (any, error)) (any, error) {
	c.mu.Lock()
	e, ok := c.entries[key]
	c.mu.Unlock()
	if ok && c.now().Before(e.until) {
		return e.value, e.err
	}
	v, err, _ := c.flight.Do(key, func() (any, error) {
		v, err := load()
		keep := ttl
		if err != nil {
			keep = failureTTL
		}
		c.put(key, entry{value: v, err: err, until: c.now().Add(keep)})
		return v, err
	})
	return v, err
}

// cached is get for one type of value.
func cached[T any](c *cache, key string, ttl time.Duration, load func() (T, error)) (T, error) {
	var zero T
	v, err := c.get(key, ttl, func() (any, error) { return load() })
	if err != nil {
		return zero, err
	}
	t, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("usage cache: %s holds a %T", key, v)
	}
	return t, nil
}

func (c *cache) put(key string, e entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxEntries {
		now := c.now()
		for k, old := range c.entries {
			if !now.Before(old.until) {
				delete(c.entries, k)
			}
		}
	}
	c.entries[key] = e
}
