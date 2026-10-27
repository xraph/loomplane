package plane

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// GatewayError is a refusal from the gateway itself rather than the backend,
// with the SQLSTATE a Postgres client would have seen. Message never holds a
// password or a backend address.
type GatewayError struct {
	Code    string
	Message string
}

func (e *GatewayError) Error() string { return e.Message }

func gatewayError(code, message string) *GatewayError {
	return &GatewayError{Code: code, Message: message}
}

// WithLease serves one request that has no Postgres session of its own, such
// as an HTTP query. It does what Serve does before a session starts: it finds
// the route for host (holding a branch label like Serve), checks password
// against the role's verifier (a mock one when the role has none), counts the
// request as a session so Drain and route retirement wait for it, probes the
// backend, waits at the route's gate and leases a pooled connection. Then it
// runs fn and releases the lease in the state fn returns.
//
// The lease is released LeaseBroken when fn panics (the panic goes on up) or
// when ctx ends, or the router cancels the request, while fn runs: the
// connection's deadline moves into the past, so fn's I/O fails, and its
// state can't be trusted.
//
// Refusals are *GatewayError: 08004 for an unknown route, 28P01 for a failed
// login, 57P01 while the gateway or route drains, or once the route was
// removed or stayed paused too long, 53300 when the pool is busy and 08006
// when the backend can't be reached. fn's error comes back unchanged, so a
// *pgconn.PgError from the backend passes through, unless the router cut the
// request off, which is 57P01. When ctx ends first, WithLease returns its
// error.
func (r *Router) WithLease(ctx context.Context, host, role, password string, fn func(l *Lease) (LeaseState, error)) error {
	return r.withLease(ctx, host, role, password, nil, fn)
}

// withLease is WithLease with one more step: authed, when set, runs once the
// password has been checked and before anything touches the backend, and an
// error from it comes back unchanged. The HTTP handler reads the request body
// there, so a client that fails to log in never has its body read.
func (r *Router) withLease(ctx context.Context, host, role, password string, authed func() error, fn func(l *Lease) (LeaseState, error)) error {
	deadline := time.Now().Add(r.holdLimit)
	if r.Draining() {
		return gatewayError("57P01", msgShuttingDown)
	}
	label, role, err := RouteKey(r.suffix, host, map[string]string{"user": role})
	if err != nil {
		return gatewayError("08004", r.noRouteMessage("the database gateway could not tell which database to connect to"))
	}
	h, err := r.lookup(ctx, label, deadline)
	switch {
	case errors.Is(err, errShuttingDown):
		return gatewayError("57P01", msgShuttingDown)
	case errors.Is(err, errUnknownRoute):
		return gatewayError("08004", r.noRouteMessage("the database gateway has no route for "+label.String()))
	case err != nil:
		return err
	}
	if h.State() == RouteStateDraining {
		return gatewayError("57P01", msgRouteDraining)
	}

	ls, sctx, err := r.register(ctx, h)
	if err != nil {
		if errors.Is(err, errShuttingDown) {
			return gatewayError("57P01", msgShuttingDown)
		}
		return gatewayError("57P01", msgRouteRemoved)
	}
	defer r.unregister(ls)

	if VerifyPassword(h.verifier(role), password) != nil {
		return gatewayError("28P01", `password authentication failed for user "`+role+`"`)
	}
	if authed != nil {
		if aerr := authed(); aerr != nil {
			return aerr
		}
	}
	if h.pool == nil {
		return gatewayError("08006", msgNoBackend)
	}
	if perr := h.probe(sctx, deadline); perr != nil {
		if sctx.Err() != nil {
			return r.cutOff(ctx, sctx)
		}
		// The dial error names the backend's address.
		return gatewayError("08006", msgNoBackend)
	}
	select {
	case <-h.Removed():
		return gatewayError("57P01", msgRouteRemoved)
	default:
	}
	if gerr := h.Gate(sctx); gerr != nil {
		if sctx.Err() != nil {
			return r.cutOff(ctx, sctx)
		}
		return gatewayError("57P01", gerr.Error())
	}
	l, err := h.pool.Acquire(sctx, nil)
	if err != nil {
		var pgErr *pgconn.PgError
		switch {
		case errors.Is(err, ErrPoolTimeout):
			return gatewayError("53300", msgPoolBusy)
		case sctx.Err() != nil:
			return r.cutOff(ctx, sctx)
		case errors.As(err, &pgErr):
			return pgErr
		case errors.Is(err, ErrPoolClosed):
			return gatewayError("57P01", msgRouteRemoved)
		default:
			return gatewayError("08006", msgNoBackend)
		}
	}
	return r.runLease(ctx, sctx, l, fn)
}

// runLease runs fn on l and releases it.
func (r *Router) runLease(ctx, sctx context.Context, l *Lease, fn func(l *Lease) (LeaseState, error)) (err error) {
	c := l.Conn()
	stopIO := context.AfterFunc(sctx, func() {
		_ = c.Conn.SetDeadline(time.Unix(1, 0)) //nolint:errcheck // a dead connection is already failing the work
	})
	state := LeaseBroken // what a panic in fn leaves
	defer func() {
		if !stopIO() || c.Broken {
			state = LeaseBroken
		}
		l.Release(state)
	}()
	state, err = fn(l)
	if err != nil && sctx.Err() != nil {
		return r.cutOff(ctx, sctx)
	}
	return err
}

// cutOff is the error for a request whose session context ended: ctx's own
// error when the caller's context ended, otherwise 57P01 for the reason the
// router cancelled it.
func (r *Router) cutOff(ctx, sctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if errors.Is(context.Cause(sctx), errShuttingDown) {
		return gatewayError("57P01", msgShuttingDown)
	}
	return gatewayError("57P01", msgRouteRemoved)
}
