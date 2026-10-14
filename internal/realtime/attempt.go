package realtime

import "context"

type activityAttemptKey struct{}

func WithActivityAttempt(ctx context.Context, attempt int64) context.Context {
	return context.WithValue(ctx, activityAttemptKey{}, attempt)
}

func ActivityAttempt(ctx context.Context) int64 {
	value, found := ctx.Value(activityAttemptKey{}).(int64)
	if !found || value < 1 {
		return 1
	}
	return value
}
