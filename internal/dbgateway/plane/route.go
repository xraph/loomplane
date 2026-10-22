package plane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

// mockSaltContext seeds the salt of the verifier a role without one is
// checked against. See routeHandle.verifier.
const mockSaltContext = "loomplane/dbgateway/mock\x00"

// routeHandle is the router's state for one route: its pool and hub, the
// gate that holds transactions while it is paused, and what the first
// session learned about the backend. It is the SessionRoute its sessions
// run on.
type routeHandle struct {
	r      *Router
	route  Route // as built: its ID, and the fields sameBackend compares
	secret BackendSecret
	pool   *Pool // nil when the credential wouldn't open
	hub    *Hub

	removed    chan struct{}
	removeOnce sync.Once
	pinned     atomic.Int64

	// Guarded by r.mu.
	sessions   int
	noSessions chan struct{} // closed while sessions is 0

	mu           sync.Mutex
	state        RouteState
	stateChanged chan struct{} // closed and replaced when state changes
	verifiers    []Verifier
	probed       bool
	flight       *probeFlight // the probe in progress, if any
	serverParams map[string]string
	maxConns     int
	reachable    bool
	hubListener  bool
}

var _ SessionRoute = (*routeHandle)(nil)

// backendCredential is the JSON the control plane seals for each route.
type backendCredential struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

// build makes the handle for a route. A route whose credential won't open
// gets no pool or hub and reports unreachable. Called with r.mu held.
func (r *Router) build(rt Route) *routeHandle {
	rt.Params = maps.Clone(rt.Params)
	h := &routeHandle{
		r:            r,
		route:        rt,
		removed:      make(chan struct{}),
		noSessions:   closedChan(),
		state:        rt.State,
		stateChanged: make(chan struct{}),
		verifiers:    slices.Clone(rt.Verifiers),
	}
	secret, err := r.openCredential(rt)
	if err != nil {
		return h
	}
	h.secret = secret
	h.reachable = true
	h.pool = NewPool(PoolConfig{Backend: rt.Backend, Secret: secret, Params: rt.Params, Idle: rt.IdleTimeout}, r.dialer)
	h.hub = NewHub(h.dialListener)
	return h
}

// openCredential opens the route's sealed backend credential. The error
// says nothing about the contents.
func (r *Router) openCredential(rt Route) (BackendSecret, error) {
	plain, err := Open(r.key, rt.Backend.Credential, []byte("backend-credential:"+rt.ID))
	if err != nil {
		return BackendSecret{}, err
	}
	var c backendCredential
	if err := json.Unmarshal(plain, &c); err != nil || c.User == "" {
		return BackendSecret{}, errors.New("backend credential is malformed")
	}
	return BackendSecret(c), nil
}

// update applies the parts of a route that change without a new pool: its
// state and its verifiers.
func (h *routeHandle) update(rt Route) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.verifiers = slices.Clone(rt.Verifiers)
	if rt.State != h.state {
		h.state = rt.State
		close(h.stateChanged)
		h.stateChanged = make(chan struct{})
	}
}

// State is the route's current state.
func (h *routeHandle) State() RouteState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

func (h *routeHandle) remove() {
	h.removeOnce.Do(func() { close(h.removed) })
}

func (h *routeHandle) isRemoved() bool {
	select {
	case <-h.removed:
		return true
	default:
		return false
	}
}

// Pool is the route's backend pool.
func (h *routeHandle) Pool() *Pool { return h.pool }

// Hub is the route's LISTEN hub.
func (h *routeHandle) Hub() *Hub { return h.hub }

// Removed is closed when the route is retired or the router drains.
func (h *routeHandle) Removed() <-chan struct{} { return h.removed }

// ServerParams are the parameters the backend reported to the first
// session's probe.
func (h *routeHandle) ServerParams() map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.serverParams
}

// PinChanged counts the route's pinned sessions.
func (h *routeHandle) PinChanged(pinned bool) {
	if pinned {
		h.pinned.Add(1)
	} else {
		h.pinned.Add(-1)
	}
}

// Gate lets a new transaction start. While the route is paused it waits
// for the route to resume, up to the pause limit, after which the session
// ends with 57P01.
func (h *routeHandle) Gate(ctx context.Context) error {
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		h.mu.Lock()
		state, changed := h.state, h.stateChanged
		h.mu.Unlock()
		if state != RouteStatePaused {
			return nil
		}
		if timer == nil {
			timer = time.NewTimer(h.r.pauseLimit)
		}
		select {
		case <-changed:
		case <-h.removed:
			return errRouteRemoved
		case <-timer.C:
			return errRoutePaused
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// verifier returns the route's verifier for role. A role without one gets
// a mock verifier, so the exchange and its cost are those of a wrong
// password and a client can't tell which roles exist. Its salt is derived
// from the route and role, so it is the same on every attempt, and its keys
// are random per router, so no password matches.
func (h *routeHandle) verifier(role string) Verifier {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, v := range h.verifiers {
		if v.Role == role {
			return v
		}
	}
	sum := sha256.Sum256([]byte(mockSaltContext + h.route.ID + "\x00" + role))
	return Verifier{Role: role, Salt: sum[:16], Iterations: 4096, StoredKey: h.r.mockStored, ServerKey: h.r.mockServer}
}

// probeFlight is a probe in progress. Sessions that arrive while it runs
// wait for it and share its result; one that arrives after it ends probes
// again.
type probeFlight struct {
	done chan struct{}
	err  error // set before done closes
}

// probe learns the backend's parameters and max_connections from one
// pooled connection, the first time a session needs them. A failure isn't
// remembered past the sessions waiting on it: the next session probes
// again. Everything, waiting for another session's probe included, ends at
// deadline, the end of the session's hold, and a backend that is waking is
// retried until then.
func (h *routeHandle) probe(ctx context.Context, deadline time.Time) error {
	pctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	for {
		h.mu.Lock()
		if h.probed {
			h.mu.Unlock()
			return nil
		}
		f := h.flight
		if f == nil {
			f = &probeFlight{done: make(chan struct{})}
			h.flight = f
			h.mu.Unlock()
			return h.runProbe(pctx, f)
		}
		h.mu.Unlock()
		select {
		case <-f.done:
		case <-pctx.Done():
			return pctx.Err()
		}
		// A probe cut short because its own client left says nothing about
		// the backend: try again.
		if !errors.Is(f.err, context.Canceled) {
			return f.err
		}
	}
}

// runProbe runs the probe f stands for and publishes its result.
func (h *routeHandle) runProbe(ctx context.Context, f *probeFlight) error {
	var (
		params   map[string]string
		maxConns int
	)
	deadline, _ := ctx.Deadline()
	err := h.r.untilAwake(ctx, deadline, func() error {
		l, err := h.pool.Acquire(ctx, nil)
		if err != nil {
			return err
		}
		c := l.Conn()
		params = maps.Clone(c.Params)
		maxConns, err = MaxConnections(ctx, c)
		state := LeaseClean
		if c.Broken {
			state = LeaseBroken
		}
		l.Release(state)
		return err
	})

	h.mu.Lock()
	h.flight = nil
	f.err = err
	switch {
	case err == nil:
		h.probed, h.serverParams, h.maxConns, h.reachable = true, params, maxConns, true
		h.setLimitLocked()
	case !errors.Is(err, context.Canceled):
		h.reachable = false
	}
	h.mu.Unlock()
	close(f.done)
	return err
}

// dialListener opens the hub's listener connection. The first one takes a
// connection out of the pool's share.
func (h *routeHandle) dialListener(ctx context.Context) (*BackendConn, error) {
	c, err := h.r.dialer.Dial(ctx, h.route.Backend, h.secret, h.route.Params)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	if !h.hubListener {
		h.hubListener = true
		h.setLimitLocked()
	}
	h.mu.Unlock()
	return c, nil
}

// applyLimit resizes the pool after the instance count changed.
func (h *routeHandle) applyLimit() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.setLimitLocked()
}

// setLimitLocked sizes the pool from what the probe found. Before the
// probe the pool keeps its starting limit of one. Called with h.mu held, so
// the last change to any input is the last limit set.
func (h *routeHandle) setLimitLocked() {
	if h.pool == nil || !h.probed {
		return
	}
	h.pool.SetLimit(poolLimit(h.maxConns, int(h.r.instances.Load()), h.hubListener))
}

// poolLimit is one instance's pool size for a backend allowing maxConns
// connections: its Budget share, capped at 2 until the instance count is
// known (instances is 0), less one for the hub's listener when there is
// one, never below one.
func poolLimit(maxConns, instances int, hubListener bool) int {
	n := Budget(maxConns, instances)
	if instances < 1 {
		n = min(n, 2)
	}
	if hubListener {
		n--
	}
	return max(n, 1)
}

// RouteStats is what the gateway reports about one route. WaitP95 encodes as
// wait_p95_ms, whole milliseconds.
type RouteStats struct {
	RouteID        string        `json:"route_id"`
	BackendConns   int           `json:"backend_conns"`
	Pinned         int           `json:"pinned"`
	PinRate        float64       `json:"pin_rate"`
	WaitP95        time.Duration `json:"-"`
	Reachable      bool          `json:"reachable"`
	MaxConnections int           `json:"max_connections"`
}

type routeStatsJSON struct {
	RouteID        string  `json:"route_id"`
	BackendConns   int     `json:"backend_conns"`
	Pinned         int     `json:"pinned"`
	PinRate        float64 `json:"pin_rate"`
	WaitP95Millis  int64   `json:"wait_p95_ms"`
	Reachable      bool    `json:"reachable"`
	MaxConnections int     `json:"max_connections"`
}

// MarshalJSON writes WaitP95 as integer milliseconds.
func (s RouteStats) MarshalJSON() ([]byte, error) {
	return json.Marshal(routeStatsJSON{
		RouteID: s.RouteID, BackendConns: s.BackendConns, Pinned: s.Pinned, PinRate: s.PinRate,
		WaitP95Millis: s.WaitP95.Milliseconds(), Reachable: s.Reachable, MaxConnections: s.MaxConnections,
	})
}

// UnmarshalJSON reads what MarshalJSON writes.
func (s *RouteStats) UnmarshalJSON(b []byte) error {
	var j routeStatsJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	*s = RouteStats{
		RouteID: j.RouteID, BackendConns: j.BackendConns, Pinned: j.Pinned, PinRate: j.PinRate,
		WaitP95: time.Duration(j.WaitP95Millis) * time.Millisecond, Reachable: j.Reachable, MaxConnections: j.MaxConnections,
	}
	return nil
}

// stats reports the handle, apart from PinRate, which needs the router's
// session count.
func (h *routeHandle) stats() RouteStats {
	s := RouteStats{RouteID: h.route.ID, Pinned: int(h.pinned.Load())}
	if h.pool != nil {
		ps := h.pool.Stats()
		s.BackendConns, s.WaitP95 = ps.Open, ps.WaitP95
	}
	h.mu.Lock()
	s.Reachable, s.MaxConnections = h.reachable, h.maxConns
	h.mu.Unlock()
	return s
}

// replicate serves a replication connection. It gets a backend connection
// of its own, opened with the client's startup parameters, and the router
// relays messages both ways until either side ends. These connections are
// never pooled and count against the router's replication limit.
func (r *Router) replicate(ctx context.Context, be *pgproto3.Backend, client io.Closer, h *routeHandle, startup map[string]string, deadline time.Time) error {
	r.mu.Lock()
	full := r.replicas >= replicationLimit
	if !full {
		r.replicas++
	}
	r.mu.Unlock()
	if full {
		SendError(be, "53300", msgReplicationBusy)
		return errReplicationBusy
	}
	defer func() {
		r.mu.Lock()
		r.replicas--
		r.mu.Unlock()
	}()

	// The backend credential decides the user and the route the database.
	// options goes too: it may carry the router's endpoint= setting, which
	// Postgres would refuse.
	params := maps.Clone(h.route.Params)
	if params == nil {
		params = map[string]string{}
	}
	for k, v := range startup {
		switch k {
		case "user", "database", "options":
		default:
			params[k] = v
		}
	}
	// The dial, retries for a waking backend included, ends with the hold.
	dctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	var c *BackendConn
	err := r.untilAwake(dctx, deadline, func() (err error) {
		c, err = r.dialer.Dial(dctx, h.route.Backend, h.secret, params)
		return err
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		SendError(be, "08006", msgNoBackend)
		return err
	}
	defer c.Close()

	// The backend's own greeting went to the dialer, so the client gets one
	// from what it reported. Cancel requests aren't supported, so the key
	// is random.
	names := make([]string, 0, len(c.Params))
	for name := range c.Params {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		be.Send(&pgproto3.ParameterStatus{Name: name, Value: c.Params[name]})
	}
	var key [8]byte
	_, _ = rand.Read(key[:])
	be.Send(&pgproto3.BackendKeyData{ProcessID: binary.BigEndian.Uint32(key[:4]), SecretKey: key[4:]})
	be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if err := be.Flush(); err != nil {
		return err
	}
	return relay(ctx, be, client, c)
}

// relay copies messages between the client and a dedicated backend
// connection, COPY BOTH streams included, until either side ends or ctx
// does. Then it closes both connections. A client Terminate ends it with
// nil.
func relay(ctx context.Context, be *pgproto3.Backend, client io.Closer, c *BackendConn) error {
	errs := make(chan error, 2)
	go func() {
		for {
			msg, err := be.Receive()
			if err != nil {
				errs <- err
				return
			}
			c.Frontend.Send(msg)
			if err := c.Frontend.Flush(); err != nil {
				errs <- err
				return
			}
			if _, ok := msg.(*pgproto3.Terminate); ok {
				errs <- nil
				return
			}
		}
	}()
	go func() {
		for {
			msg, err := c.Frontend.Receive()
			if err != nil {
				errs <- err
				return
			}
			be.Send(msg)
			// A stream of WAL comes in bursts: flush once the burst is read.
			if c.Frontend.ReadBufferLen() > 0 {
				continue
			}
			if err := be.Flush(); err != nil {
				errs <- err
				return
			}
		}
	}()

	var err error
	pending := 2
	select {
	case err = <-errs:
		pending--
	case <-ctx.Done():
		err = ctx.Err()
	}
	_ = c.Close()
	if client != nil {
		_ = client.Close()
	}
	for ; pending > 0; pending-- {
		<-errs
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil
	}
	return err
}
