package identity

import (
	"context"
	"path"
	"strings"
	"sync/atomic"
	"time"

	authmw "github.com/xraph/authsome/middleware"
	"github.com/xraph/authsome/ratelimit"
	"github.com/xraph/forge"
)

type rateScopeKey struct{}

// Authsome's route limits share a client-address key. Scope their shared KV
// limiter by canonical route so recovery cannot exhaust the sign-in budget.
func rateLimitScope(next forge.Handler) forge.Handler {
	return func(c forge.Context) error {
		r := c.Request()
		scope := r.Method + ":" + strings.ToLower(path.Clean(r.URL.Path)) + ":"
		c.WithContext(context.WithValue(r.Context(), rateScopeKey{}, scope))
		return next(c)
	}
}

type routeLimiter struct{ ratelimit.Limiter }

func scopedRateKey(ctx context.Context, key string) string {
	scope, ok := ctx.Value(rateScopeKey{}).(string)
	if !ok {
		return key
	}
	return scope + key
}
func (r routeLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	key = scopedRateKey(ctx, key)
	if c, ok := ctx.Value(countedKey{}).(*counted); ok && c.key == key && c.spent.CompareAndSwap(false, true) {
		return true, nil
	}
	return r.Limiter.Allow(ctx, key, limit, window)
}

type countedKey struct{}

// counted is a rate-limit key a request was already counted under, by a
// limiter that ran ahead of its route's own.
type counted struct {
	key   string
	spent atomic.Bool
}

// countedOnce marks the request as counted under the key a route limiter
// keyed by client address would use, so that limiter lets it through once
// rather than count it a second time. Put it behind a limiter that shares
// the route's budget.
func countedOnce(next forge.Handler) forge.Handler {
	return func(c forge.Context) error {
		r := c.Request()
		key := scopedRateKey(r.Context(), authmw.ClientIP(r))
		c.WithContext(context.WithValue(r.Context(), countedKey{}, &counted{key: key}))
		return next(c)
	}
}
func (r routeLimiter) Remaining(ctx context.Context, key string, limit int, window time.Duration) (int, error) {
	return r.Limiter.Remaining(ctx, scopedRateKey(ctx, key), limit, window)
}
