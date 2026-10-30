package security

import "context"

type consoleReadKey struct{}

// WithConsoleRead marks ctx as a read the instance console's gate has
// admitted for a platform admin. Only that gate sets it, and only on a query
// the dashboard registry has as a read.
func WithConsoleRead(ctx context.Context) context.Context {
	return context.WithValue(ctx, consoleReadKey{}, true)
}

// IsConsoleRead reports whether ctx is a console read WithConsoleRead
// marked.
func IsConsoleRead(ctx context.Context) bool {
	read, ok := ctx.Value(consoleReadKey{}).(bool)
	return ok && read
}
