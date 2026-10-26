package octopusloom

import (
	"context"
	"errors"
	"fmt"
)

// Errors a Store and the model's validators return. Test with errors.Is.
// ErrInvalid and ErrConflict from NormalizeRouteInput and CheckRouteConflicts
// carry a message written for the person who typed the route: Error() is
// that message and nothing else, so callers can show it as it is.
var (
	ErrNotFound    = errors.New("octopus gateway or route not found")
	ErrInvalid     = errors.New("invalid octopus route")
	ErrConflict    = errors.New("octopus route conflicts with an existing route")
	ErrStale       = errors.New("octopus gateway or route changed since it was read")
	ErrBusy        = errors.New("octopus gateway is busy")
	ErrUnavailable = errors.New("octopus admin API is unavailable")
)

// userError is a sentinel error whose text is a message for the user.
type userError struct {
	kind error
	msg  string
}

func (e *userError) Error() string { return e.msg }
func (e *userError) Unwrap() error { return e.kind }

func invalidf(format string, args ...any) error {
	return &userError{kind: ErrInvalid, msg: fmt.Sprintf(format, args...)}
}

func conflictf(format string, args ...any) error {
	return &userError{kind: ErrConflict, msg: fmt.Sprintf(format, args...)}
}

// Store keeps the Octopus gateways Loomplane manages and the manual routes
// on them. Every change to what a gateway should serve bumps its Generation;
// the syncer records the Generation it applied with SaveSyncState.
type Store interface {
	// EnsureGateway upserts the gateway's provider and namespace. It keeps
	// the FARP hostnames, generation and sync state of an existing gateway,
	// and fills g with the stored row.
	EnsureGateway(ctx context.Context, g *Gateway) error
	// Gateway returns ErrNotFound when the gateway doesn't exist.
	Gateway(ctx context.Context, ref GatewayRef) (*Gateway, error)
	Gateways(ctx context.Context) ([]*Gateway, error)
	// SetFarpHostnames replaces the hostnames FARP routes answer on and bumps
	// the generation. ErrStale on a revision mismatch.
	SetFarpHostnames(ctx context.Context, ref GatewayRef, hostnames []string, revision int64) (*Gateway, error)
	// SaveSyncState records what the syncer applied. It leaves the generation,
	// revision and UpdatedAt alone. ErrNotFound when the gateway is gone.
	SaveSyncState(ctx context.Context, ref GatewayRef, applied int64, state SyncState) error
	// BumpGeneration marks the gateway as needing a sync, for a change that
	// lives outside this store (a release, a domain). ErrNotFound when missing.
	BumpGeneration(ctx context.Context, ref GatewayRef) error
	// DeleteGateway removes the gateway and, by cascade, its routes. A gateway
	// that is already gone is not an error.
	DeleteGateway(ctx context.Context, ref GatewayRef) error
	Routes(ctx context.Context, ref GatewayRef) ([]*Route, error)
	// CreateRoute fills in ID, Revision and the timestamps, and bumps the
	// gateway's generation in the same transaction. ErrConflict when the
	// hostname and path are taken, ErrNotFound when the gateway is missing.
	CreateRoute(ctx context.Context, r *Route) error
	// UpdateRoute needs r.Revision to be the revision read. ErrStale on a
	// mismatch, ErrNotFound when the route is gone. It bumps the generation.
	UpdateRoute(ctx context.Context, r *Route) error
	// DeleteRoute has the same ErrStale and ErrNotFound rules as UpdateRoute.
	DeleteRoute(ctx context.Context, ref GatewayRef, id string, revision int64) error
	// WithGatewayLock runs run while holding the gateway's lock. ErrBusy when
	// another caller holds it.
	WithGatewayLock(ctx context.Context, ref GatewayRef, run func() error) error
}
