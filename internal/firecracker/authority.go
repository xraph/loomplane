package firecracker

import "context"

// Issuer persists authority before a caller sends a mutation to a host.
type Issuer interface {
	IssueFirecracker(context.Context, Scope, string, string) (Authority, error)
}
