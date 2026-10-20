package identity

import (
	"context"
	"path"
	"strings"
	"time"

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
	return r.Limiter.Allow(ctx, scopedRateKey(ctx, key), limit, window)
}
func (r routeLimiter) Remaining(ctx context.Context, key string, limit int, window time.Duration) (int, error) {
	return r.Limiter.Remaining(ctx, scopedRateKey(ctx, key), limit, window)
}
