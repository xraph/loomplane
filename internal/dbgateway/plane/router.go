package plane

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

const (
	// holdLimit is how long a connection waits, from arrival, for an unknown
	// branch route to appear or a waking backend to answer.
	holdLimit = 10 * time.Second
	// pauseLimit is how long a new transaction waits on a paused route.
	pauseLimit = 30 * time.Second
	// drainLimit is how long a retired route, or a draining gateway, lets
	// live sessions run before it ends them.
	drainLimit = 30 * time.Second
	// wakeRetry is how often a dial that found the backend waking is retried.
	wakeRetry = 250 * time.Millisecond
	// closeGrace is how long a cancelled session has to end on its own,
	// telling its client why, before the router closes the client's
	// connection under it.
	closeGrace = time.Second
	// replicationLimit is how many replication connections one gateway
	// instance relays at once.
	replicationLimit = 2
)

const (
	msgShuttingDown    = "the gateway is shutting down"
	msgRouteDraining   = "the database route is draining"
	msgRoutePaused     = "the database route is paused"
	msgReplicationBusy = "the database gateway has too many replication connections open, try again"
)

var (
	errShuttingDown    = errors.New(msgShuttingDown)
	errRouteDraining   = errors.New(msgRouteDraining)
	errRoutePaused     = errors.New(msgRoutePaused)
	errRouteRetired    = errors.New("the database route was retired")
	errUnknownRoute    = errors.New("no database route for the label")
	errUnreachable     = errors.New("the database route's backend is unreachable")
	errReplicationBusy = errors.New("too many replication connections")
)

// Holder asks the control plane to bring up the route for a label the
// gateway doesn't know yet, such as a branch created on first connect. A
// nil Holder means unknown labels fail at once.
type Holder interface {
	Hold(ctx context.Context, l Label) error
}

// Router is what a transport hands each client connection to. It finds the
// route from the snapshot it was last given, authenticates the client, holds
// the connection while a route or backend comes up, and serves the session.
// It owns one pool and one LISTEN hub per route, built, updated and retired
// as snapshots change.
type Router struct {
	store     *Store
	key       *ecdh.PrivateKey
	gatewayID string
	dialer    Dialer
	holder    Holder
	suffix    Suffix

	// The mock verifier's keys, random per router. See routeHandle.verifier.
	mockStored, mockServer []byte

	// Limits, set from the constants above. Tests shorten them before the
	// router serves anything.
	holdLimit, pauseLimit, retireLimit, drainLimit, wakeRetry, closeGrace time.Duration

	instances atomic.Int64 // live gateway instances; 0 until SetInstances

	applyMu sync.Mutex // serialises Apply and the start of Drain

	mu       sync.Mutex
	routes   map[string]*routeHandle // by route ID
	labels   map[string]*routeHandle // by label
	retiring map[*routeHandle]struct{}
	changed  chan struct{} // closed and replaced by every Apply
	sessions map[*liveSession]struct{}
	idle     chan struct{} // closed while no session is live
	draining chan struct{} // closed when Drain starts
	replicas int

	drainOnce sync.Once
}

// liveSession is one client connection the router is serving.
type liveSession struct {
	h      *routeHandle
	cancel context.CancelCauseFunc
}

// NewRouter returns a router with no routes. key opens the backend
// credentials in the snapshot, d dials backends, and h may be nil.
func NewRouter(store *Store, key *ecdh.PrivateKey, gatewayID string, d Dialer, h Holder, suffix Suffix) *Router {
	r := &Router{
		store:       store,
		key:         key,
		gatewayID:   gatewayID,
		dialer:      d,
		holder:      h,
		suffix:      suffix,
		mockStored:  make([]byte, 32),
		mockServer:  make([]byte, 32),
		holdLimit:   holdLimit,
		pauseLimit:  pauseLimit,
		retireLimit: drainLimit,
		drainLimit:  drainLimit,
		wakeRetry:   wakeRetry,
		closeGrace:  closeGrace,
		routes:      map[string]*routeHandle{},
		labels:      map[string]*routeHandle{},
		retiring:    map[*routeHandle]struct{}{},
		changed:     make(chan struct{}),
		sessions:    map[*liveSession]struct{}{},
		idle:        closedChan(),
		draining:    make(chan struct{}),
	}
	// crypto/rand.Read never returns an error on supported platforms.
	_, _ = rand.Read(r.mockStored)
	_, _ = rand.Read(r.mockServer)
	return r
}

func closedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// Apply makes s the store's current snapshot and brings the routes in line
// with it. A new route gets a pool and a hub. A route whose backend,
// credential, parameters or idle timeout changed gets new ones, and the old
// ones are retired. A route that only changed state, label or verifiers is
// updated in place. A route that is gone is retired: its sessions end at
// their next idle point, and its pool closes once they have, or after 30s.
//
// The caller verifies s first. Apply refuses a snapshot for another gateway,
// one that is not newer than the store's (ErrStaleSnapshot), and anything
// once the router drains.
func (r *Router) Apply(s Snapshot) error {
	if s.GatewayID != r.gatewayID {
		return fmt.Errorf("apply snapshot: it is for gateway %q, this is %q", s.GatewayID, r.gatewayID)
	}
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	if r.Draining() {
		return errShuttingDown
	}
	if err := r.store.Swap(s); err != nil {
		return err
	}

	var retire []*routeHandle
	r.mu.Lock()
	routes := make(map[string]*routeHandle, len(s.Routes))
	labels := make(map[string]*routeHandle, len(s.Routes))
	for _, rt := range s.Routes {
		h := r.routes[rt.ID]
		switch {
		case h == nil:
			h = r.build(rt)
		case !r.sameBackend(h, rt):
			retire = append(retire, h)
			h = r.build(rt)
		default:
			h.update(rt)
		}
		routes[rt.ID] = h
		labels[rt.Label.String()] = h
	}
	for id, h := range r.routes {
		if routes[id] == nil {
			retire = append(retire, h)
		}
	}
	for _, h := range retire {
		r.retiring[h] = struct{}{}
	}
	r.routes, r.labels = routes, labels
	close(r.changed)
	r.changed = make(chan struct{})
	r.mu.Unlock()

	for _, h := range retire {
		r.retire(h)
	}
	return nil
}

// sameBackend reports whether rt reaches the backend the way h does, so h's
// pool can keep serving it. Sealing is randomised, so the credentials are
// compared opened: a control plane that seals the same secret afresh for
// each snapshot doesn't replace every pool.
func (r *Router) sameBackend(h *routeHandle, rt Route) bool {
	a, b := h.route.Backend, rt.Backend
	a.Credential, b.Credential = Sealed{}, Sealed{}
	if !reflect.DeepEqual(a, b) || !maps.Equal(h.route.Params, rt.Params) || h.route.IdleTimeout != rt.IdleTimeout {
		return false
	}
	secret, err := r.openCredential(rt)
	if opened := err == nil; opened != (h.pool != nil) {
		return false
	}
	return secret == h.secret
}

// retire ends a route handle: its sessions are told at their next idle
// point, its hub closes now, and its pool once nobody uses it, or after the
// retire limit, when the sessions still running are cancelled.
func (r *Router) retire(h *routeHandle) {
	h.remove()
	if h.hub != nil {
		h.hub.Close()
	}
	r.mu.Lock()
	done := h.noSessions
	r.mu.Unlock()
	go func() {
		t := time.NewTimer(r.retireLimit)
		defer t.Stop()
		select {
		case <-done:
		case <-t.C:
			r.cancelSessions(h, errRouteRetired)
			// Give them a moment to release their leases cleanly.
			grace := time.NewTimer(2 * r.closeGrace)
			select {
			case <-done:
			case <-grace.C:
			}
			grace.Stop()
		}
		if h.pool != nil {
			h.pool.Close()
		}
		r.mu.Lock()
		delete(r.retiring, h)
		r.mu.Unlock()
	}()
}

// SetInstances records how many gateway instances share each backend, and
// resizes every pool to its share.
func (r *Router) SetInstances(n int) {
	r.instances.Store(int64(n))
	r.mu.Lock()
	handles := make([]*routeHandle, 0, len(r.routes))
	for _, h := range r.routes {
		handles = append(handles, h)
	}
	r.mu.Unlock()
	for _, h := range handles {
		h.applyLimit()
	}
}

// Draining reports whether Drain has started.
func (r *Router) Draining() bool {
	select {
	case <-r.draining:
		return true
	default:
		return false
	}
}

// Drain shuts the router down. New connections get 57P01 at once. Live
// sessions end at their next idle point, and those still running after 30s
// (or when ctx ends) are cancelled. Then every pool and hub closes. Calling
// it again waits for the first call and does nothing more.
func (r *Router) Drain(ctx context.Context) {
	r.drainOnce.Do(func() {
		r.applyMu.Lock()
		r.mu.Lock()
		close(r.draining)
		handles := make([]*routeHandle, 0, len(r.routes)+len(r.retiring))
		for _, h := range r.routes {
			handles = append(handles, h)
		}
		for h := range r.retiring {
			handles = append(handles, h)
		}
		idle := r.idle
		r.mu.Unlock()
		r.applyMu.Unlock()

		for _, h := range handles {
			h.remove()
		}
		t := time.NewTimer(r.drainLimit)
		select {
		case <-idle:
		case <-t.C:
		case <-ctx.Done():
		}
		t.Stop()
		r.cancelSessions(nil, errShuttingDown)
		grace := time.NewTimer(2 * r.closeGrace)
		select {
		case <-idle:
		case <-grace.C:
		}
		grace.Stop()
		for _, h := range handles {
			if h.hub != nil {
				h.hub.Close()
			}
			if h.pool != nil {
				h.pool.Close()
			}
		}
	})
}

// Stats reports every current route, sorted by ID.
func (r *Router) Stats() []RouteStats {
	r.mu.Lock()
	type entry struct {
		h    *routeHandle
		live int
	}
	entries := make([]entry, 0, len(r.routes))
	for _, h := range r.routes {
		entries = append(entries, entry{h, h.sessions})
	}
	r.mu.Unlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].h.route.ID < entries[j].h.route.ID })

	out := make([]RouteStats, 0, len(entries))
	for _, e := range entries {
		s := e.h.stats()
		if e.live > 0 {
			s.PinRate = float64(s.Pinned) / float64(e.live)
		}
		out = append(out, s)
	}
	return out
}

// Serve serves one client connection, from the startup packet on. The
// transport has read the StartupMessage and, for WebSocket clients, the
// PasswordMessage pipelined behind it; host is the logical host the client
// asked for. conn is the client's transport: the router closes it when the
// session is cancelled (the route retired or the gateway drained past their
// limits, or ctx ended), so a session blocked writing to a client that
// stopped reading still ends. The transport closes it too once Serve
// returns.
//
// Every refusal reaches the client as a FATAL ErrorResponse: 08004 when no
// route matches, 28P01 for a failed login, 57P01 while the route or the
// gateway drains, 08006 when the backend can't be reached and 53300 when
// replication connections are at their limit.
func (r *Router) Serve(ctx context.Context, be *pgproto3.Backend, conn io.Closer, host string, startup map[string]string, pipelined *pgproto3.PasswordMessage) error {
	deadline := time.Now().Add(r.holdLimit)
	if r.Draining() {
		SendError(be, "57P01", msgShuttingDown)
		return errShuttingDown
	}
	label, role, err := RouteKey(r.suffix, host, startup)
	if err != nil {
		SendError(be, "08004", r.noRouteMessage())
		return err
	}
	h, err := r.lookup(ctx, label, deadline)
	switch {
	case errors.Is(err, errShuttingDown):
		SendError(be, "57P01", msgShuttingDown)
		return err
	case errors.Is(err, errUnknownRoute):
		SendError(be, "08004", "the database gateway has no route for "+label.String())
		return err
	case err != nil:
		return err
	}
	if h.State() == RouteStateDraining {
		SendError(be, "57P01", msgRouteDraining)
		return errRouteDraining
	}

	ls, sctx, err := r.register(ctx, h)
	if err != nil {
		if errors.Is(err, errShuttingDown) {
			SendError(be, "57P01", msgShuttingDown)
		} else {
			SendError(be, "57P01", msgRouteRemoved)
		}
		return err
	}
	defer r.unregister(ls)
	stop := closeOnDone(sctx, conn, r.closeGrace)
	defer stop()

	err = r.serveRoute(sctx, be, conn, h, role, startup, pipelined, deadline)
	// A session the router cancelled hears why, if its connection still
	// takes writes. One that ended on its own has told its client already.
	if errors.Is(err, context.Canceled) && ctx.Err() == nil {
		switch cause := context.Cause(sctx); {
		case errors.Is(cause, errShuttingDown):
			SendError(be, "57P01", msgShuttingDown)
		case errors.Is(cause, errRouteRetired):
			SendError(be, "57P01", msgRouteRemoved)
		}
	}
	return err
}

// serveRoute authenticates the client against the route and serves it.
func (r *Router) serveRoute(ctx context.Context, be *pgproto3.Backend, conn io.Closer, h *routeHandle, role string, startup map[string]string, pipelined *pgproto3.PasswordMessage, deadline time.Time) error {
	// Phase 1 transports are WebSocket only, where the client sends its
	// password in cleartext inside TLS. The Phase 2 TCP listener will choose
	// SCRAM here.
	if err := AuthenticateCleartext(be, h.verifier(role), pipelined); err != nil {
		if errors.Is(err, ErrAuth) {
			SendError(be, "28P01", `password authentication failed for user "`+role+`"`)
		}
		return err
	}
	if h.pool == nil {
		SendError(be, "08006", msgNoBackend)
		return errUnreachable
	}
	if _, ok := startup["replication"]; ok {
		return r.replicate(ctx, be, conn, h, startup, deadline)
	}
	if err := h.probe(ctx, deadline); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The dial error names the backend's address, which the client must
		// not learn.
		SendError(be, "08006", msgNoBackend)
		return err
	}
	return ServeSession(ctx, be, h, startup)
}

func (r *Router) noRouteMessage() string {
	return "the database gateway could not tell which database to connect to: connect to <label>." + string(r.suffix) +
		", set options=endpoint=<label>, or connect as user=<role>@<label>"
}

// lookup finds the route for label. An unknown or holding branch label is
// held: the Holder is asked for it, and the connection waits until deadline
// for an Apply that brings it up. Anything else unknown fails at once.
func (r *Router) lookup(ctx context.Context, label Label, deadline time.Time) (*routeHandle, error) {
	key := label.String()
	asked := false
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		r.mu.Lock()
		h, changed := r.labels[key], r.changed
		r.mu.Unlock()
		if r.Draining() {
			return nil, errShuttingDown
		}
		if h != nil && h.State() != RouteStateHolding {
			return h, nil
		}
		if label.Branch == "" || r.holder == nil {
			return nil, errUnknownRoute
		}
		if !asked {
			asked = true
			hctx, cancel := context.WithDeadline(ctx, deadline)
			err := r.holder.Hold(hctx, label)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, errUnknownRoute
			}
			timer = time.NewTimer(time.Until(deadline))
			continue
		}
		select {
		case <-changed:
		case <-r.draining:
		case <-timer.C:
			return nil, errUnknownRoute
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// register counts a session against its route and the router, and returns
// the context it runs under, which Drain and retire cancel.
func (r *Router) register(ctx context.Context, h *routeHandle) (*liveSession, context.Context, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.Draining():
		return nil, nil, errShuttingDown
	case h.isRemoved():
		return nil, nil, errRouteRetired
	}
	sctx, cancel := context.WithCancelCause(ctx)
	ls := &liveSession{h: h, cancel: cancel}
	if len(r.sessions) == 0 {
		r.idle = make(chan struct{})
	}
	r.sessions[ls] = struct{}{}
	if h.sessions == 0 {
		h.noSessions = make(chan struct{})
	}
	h.sessions++
	return ls, sctx, nil
}

func (r *Router) unregister(ls *liveSession) {
	ls.cancel(nil)
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, ls)
	if len(r.sessions) == 0 {
		close(r.idle)
	}
	ls.h.sessions--
	if ls.h.sessions == 0 {
		close(ls.h.noSessions)
	}
}

// cancelSessions cancels the live sessions of h, or every live session when
// h is nil.
func (r *Router) cancelSessions(h *routeHandle, cause error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for ls := range r.sessions {
		if h == nil || ls.h == h {
			ls.cancel(cause)
		}
	}
}

// closeOnDone closes conn closeGrace after ctx ends, unless stop is called
// first. The grace lets a cancelled session tell its client why; closing
// the connection is for one blocked writing to a client that stopped
// reading.
func closeOnDone(ctx context.Context, conn io.Closer, grace time.Duration) (stop func()) {
	if conn == nil {
		return func() {}
	}
	var (
		mu      sync.Mutex
		stopped bool
		timer   *time.Timer
	)
	stopCtx := context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		if !stopped {
			timer = time.AfterFunc(grace, func() { _ = conn.Close() })
		}
	})
	return func() {
		stopCtx()
		mu.Lock()
		defer mu.Unlock()
		stopped = true
		if timer != nil {
			timer.Stop()
		}
	}
}

// untilAwake runs try until it returns something other than ErrWaking,
// retrying every wakeRetry until deadline.
func (r *Router) untilAwake(ctx context.Context, deadline time.Time, try func() error) error {
	for {
		err := try()
		if !errors.Is(err, ErrWaking) {
			return err
		}
		wait := time.Until(deadline)
		if wait <= 0 {
			return err
		}
		t := time.NewTimer(min(r.wakeRetry, wait))
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
}
