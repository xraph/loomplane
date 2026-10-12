// Package progress lets a cloud client report what it's waiting on while
// it polls a long-running operation, so the durable activity driving it
// can heartbeat and show the step's progress.
package progress

import "context"

type key struct{}

// With returns a context whose waits report to fn.
func With(ctx context.Context, fn func(message string)) context.Context {
	return context.WithValue(ctx, key{}, fn)
}

// Report tells ctx's listener, if any, what's happening.
func Report(ctx context.Context, message string) {
	if fn, ok := ctx.Value(key{}).(func(string)); ok {
		fn(message)
	}
}
