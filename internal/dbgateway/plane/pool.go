package plane

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

var (
	// ErrPoolTimeout means no backend connection came free within the pool's
	// wait limit. The session maps it to SQLSTATE 53300.
	ErrPoolTimeout = errors.New("timed out waiting for a backend connection")
	// ErrPoolClosed means the pool was closed, so no connection will come.
	ErrPoolClosed = errors.New("backend pool is closed")
)

const (
	defaultPoolWait = 5 * time.Second
	defaultPoolIdle = 5 * time.Minute
	maxReapInterval = 30 * time.Second
	// resetTimeout bounds the ROLLBACK or DISCARD ALL that Release sends.
	resetTimeout = 10 * time.Second
)

// LeaseState tells the pool what shape a connection is in when it comes back.
type LeaseState int

const (
	// LeaseClean: the connection is idle and holds only the parameters the
	// lease asked for. It goes straight back to the pool.
	LeaseClean LeaseState = iota
	// LeaseDirtyTxn: a transaction may still be open. The pool rolls it back.
	LeaseDirtyTxn
	// LeaseSession: the client created session state the pool can't replay.
	// The pool runs DISCARD ALL.
	LeaseSession
	// LeaseBroken: the connection can't be trusted. The pool closes it.
	LeaseBroken
)

// PoolConfig describes the backend one Pool serves. Params are the route's
// default session parameters, sent as startup parameters on every dial. Wait
// bounds how long Acquire queues for a connection (default 5s) and Idle how
// long a connection may sit unused before it is closed (default 5m).
type PoolConfig struct {
	Backend Backend
	Secret  BackendSecret
	Params  map[string]string
	Wait    time.Duration
	Idle    time.Duration
}

// PoolStats is a snapshot of a pool. Open counts every connection the pool
// owns, including ones being dialed and ones leased out.
type PoolStats struct{ Open, Idle, Waiting, Limit int }

// pooled is a backend connection with the pool's own bookkeeping. applied is
// the set of KnownParams values in effect on the connection.
type pooled struct {
	conn      *BackendConn
	applied   map[string]string
	idleSince time.Time
}

// waiter is one queued Acquire. The pool fills in conn (an idle connection
// handed over), slot (permission to dial) or err, then closes ready. All
// fields besides ready are guarded by Pool.mu.
type waiter struct {
	ready chan struct{}
	conn  *pooled
	slot  bool
	err   error
	elem  *list.Element
}

// Pool holds the connections to one backend for transaction pooling. It never
// holds its lock across I/O.
type Pool struct {
	cfg    PoolConfig
	dialer Dialer
	now    func() time.Time

	mu      sync.Mutex
	limit   int
	open    int
	idle    []*pooled // oldest first
	waiters list.List // of *waiter, oldest first
	closed  bool

	stop     chan struct{}
	stopOnce sync.Once
	reaped   chan struct{}
}

// NewPool returns a pool for cfg and starts its idle reaper. The limit starts
// at 1; the router sets the real one with SetLimit.
func NewPool(cfg PoolConfig, d Dialer) *Pool {
	if cfg.Wait <= 0 {
		cfg.Wait = defaultPoolWait
	}
	if cfg.Idle <= 0 {
		cfg.Idle = defaultPoolIdle
	}
	cfg.Params = copyParams(cfg.Params)
	p := &Pool{
		cfg:    cfg,
		dialer: d,
		now:    time.Now,
		limit:  1,
		stop:   make(chan struct{}),
		reaped: make(chan struct{}),
	}
	go p.reapLoop(reapEvery(cfg.Idle))
	return p
}

func reapEvery(idle time.Duration) time.Duration {
	d := idle / 2
	if d > maxReapInterval {
		d = maxReapInterval
	}
	if d <= 0 {
		d = time.Millisecond
	}
	return d
}

func copyParams(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// onlyKnown copies the KnownParams entries of m.
func onlyKnown(m map[string]string) map[string]string {
	out := make(map[string]string)
	for _, k := range KnownParams {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

func sameParams(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// desired is what a lease asked for: the route defaults overlaid with params,
// restricted to KnownParams.
func (p *Pool) desired(params map[string]string) map[string]string {
	out := onlyKnown(p.cfg.Params)
	for k, v := range onlyKnown(params) {
		out[k] = v
	}
	return out
}

// replaySQL is the simple Query that moves a connection from applied to
// want: SET for each new or differing key and RESET for each key no longer
// wanted, in sorted key order. Values go in as raw SQL text.
func replaySQL(applied, want map[string]string) string {
	keys := make([]string, 0, len(applied)+len(want))
	for k := range want {
		keys = append(keys, k)
	}
	for k := range applied {
		if _, ok := want[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		v, wanted := want[k]
		switch {
		case !wanted:
			parts = append(parts, "RESET "+k)
		case applied[k] != v || !hasKey(applied, k):
			parts = append(parts, "SET "+k+" TO "+v)
		}
	}
	return strings.Join(parts, "; ")
}

func hasKey(m map[string]string, k string) bool { _, ok := m[k]; return ok }

// SetLimit sets how many connections the pool may own. Values below 1 count
// as 1. Shrinking closes idle connections at once, oldest first. Busy ones
// over the new limit are closed when released.
func (p *Pool) SetLimit(n int) {
	if n < 1 {
		n = 1
	}
	p.mu.Lock()
	p.limit = n
	var drop []*pooled
	for p.open > p.limit && len(p.idle) > 0 {
		drop = append(drop, p.idle[0])
		p.idle = p.idle[1:]
		p.open--
	}
	p.dispatchLocked()
	p.mu.Unlock()
	closeAll(drop)
}

// Stats reports the pool's current counts.
func (p *Pool) Stats() PoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return PoolStats{Open: p.open, Idle: len(p.idle), Waiting: p.waiters.Len(), Limit: p.limit}
}

// Lease is one connection checked out of a Pool.
type Lease struct {
	p    *Pool
	pc   *pooled
	done atomic.Bool
}

// Conn returns the leased connection.
func (l *Lease) Conn() *BackendConn { return l.pc.conn }

// Acquire leases a connection whose session parameters match the route
// defaults overlaid with params (unknown keys are ignored). It prefers an idle
// connection that already matches, then any idle one, then dials if the pool
// is under its limit, and otherwise queues behind earlier callers. The queue
// is bounded by ctx and the pool's wait limit; at the limit Acquire returns
// ErrPoolTimeout.
func (p *Pool) Acquire(ctx context.Context, params map[string]string) (*Lease, error) {
	want := p.desired(params)

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrPoolClosed
	}
	var (
		pc   *pooled
		slot bool
	)
	switch {
	case len(p.idle) > 0 && p.waiters.Len() == 0:
		pc = p.takeIdleLocked(want)
	case p.open < p.limit && p.waiters.Len() == 0:
		p.open++
		slot = true
	default:
		w := &waiter{ready: make(chan struct{})}
		w.elem = p.waiters.PushBack(w)
		p.mu.Unlock()
		var err error
		pc, slot, err = p.wait(ctx, w)
		if err != nil {
			return nil, err
		}
		return p.ready(ctx, pc, slot, want)
	}
	p.mu.Unlock()
	return p.ready(ctx, pc, slot, want)
}

// takeIdleLocked removes and returns an idle connection, preferring one whose
// applied parameters equal want and otherwise the most recently used.
func (p *Pool) takeIdleLocked(want map[string]string) *pooled {
	i := len(p.idle) - 1
	for j := len(p.idle) - 1; j >= 0; j-- {
		if sameParams(p.idle[j].applied, want) {
			i = j
			break
		}
	}
	pc := p.idle[i]
	p.idle = append(p.idle[:i], p.idle[i+1:]...)
	return pc
}

// wait blocks until the waiter is served, ctx ends or the wait limit passes.
func (p *Pool) wait(ctx context.Context, w *waiter) (*pooled, bool, error) {
	timer := time.NewTimer(p.cfg.Wait)
	defer timer.Stop()
	var expired error
	select {
	case <-w.ready:
	case <-timer.C:
		expired = ErrPoolTimeout
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			expired = fmt.Errorf("%w: %w", ErrPoolTimeout, ctx.Err())
		} else {
			expired = ctx.Err()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if w.conn == nil && !w.slot && w.err == nil {
		p.waiters.Remove(w.elem)
		return nil, false, expired
	}
	// Served, or failed by Close, in the same instant the wait ran out. Take
	// what was handed over rather than hand it on.
	if w.err != nil {
		return nil, false, w.err
	}
	return w.conn, w.slot, nil
}

// ready turns what Acquire got (an idle connection or a slot to dial in) into
// a lease whose connection carries the wanted parameters.
func (p *Pool) ready(ctx context.Context, pc *pooled, slot bool, want map[string]string) (*Lease, error) {
	if slot {
		var err error
		if pc, err = p.dial(ctx); err != nil {
			return nil, err
		}
	}
	if sql := replaySQL(pc.applied, want); sql != "" {
		if _, err := roundTrip(ctx, pc.conn, sql); err != nil {
			p.discard(pc)
			return nil, fmt.Errorf("set backend session parameters: %w", err)
		}
		pc.applied = copyParams(want)
	}
	return &Lease{p: p, pc: pc}, nil
}

// dial opens a connection in a slot already counted in p.open. Any failure
// frees the slot.
func (p *Pool) dial(ctx context.Context) (*pooled, error) {
	conn, err := p.dialer.Dial(ctx, p.cfg.Backend, p.cfg.Secret, p.cfg.Params)
	if err != nil {
		p.freeSlot()
		return nil, err
	}
	if conn.Statements == nil {
		conn.Statements = map[string]bool{}
	}
	pc := &pooled{conn: conn, applied: onlyKnown(p.cfg.Params)}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		p.discard(pc)
		return nil, ErrPoolClosed
	}
	return pc, nil
}

// freeSlot gives up a slot for a connection that no longer exists.
func (p *Pool) freeSlot() {
	p.mu.Lock()
	p.open--
	p.dispatchLocked()
	p.mu.Unlock()
}

// discard closes pc and frees its slot.
func (p *Pool) discard(pc *pooled) {
	_ = pc.conn.Close()
	p.freeSlot()
}

func closeAll(conns []*pooled) {
	for _, pc := range conns {
		_ = pc.conn.Close()
	}
}

// dispatchLocked offers free slots to waiters, oldest first. A waiter given a
// slot dials on its own goroutine, outside the lock.
func (p *Pool) dispatchLocked() {
	for !p.closed && p.open < p.limit {
		front := p.waiters.Front()
		if front == nil {
			return
		}
		w := p.waiters.Remove(front).(*waiter)
		p.open++
		w.slot = true
		close(w.ready)
	}
}

// Release returns the lease. Only the first call has any effect. state says
// what shape the connection is in; the pool cleans it up as needed and closes
// it if that fails. Release does not wait for other callers.
func (l *Lease) Release(state LeaseState) {
	if !l.done.CompareAndSwap(false, true) {
		return
	}
	l.p.release(l.pc, state)
}

func (p *Pool) release(pc *pooled, state LeaseState) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()

	keep := !closed && !pc.conn.Broken
	if keep {
		switch state {
		case LeaseClean:
		case LeaseDirtyTxn:
			keep = resetConn(pc, "ROLLBACK")
		case LeaseSession:
			if keep = resetConn(pc, "DISCARD ALL"); keep {
				// DISCARD ALL includes RESET ALL, which returns every
				// parameter to its startup value.
				pc.conn.Statements = map[string]bool{}
				pc.applied = onlyKnown(p.cfg.Params)
			}
		default: // LeaseBroken, or a state this pool doesn't know.
			keep = false
		}
	}

	p.mu.Lock()
	if keep && !p.closed && p.open <= p.limit {
		if front := p.waiters.Front(); front != nil {
			w := p.waiters.Remove(front).(*waiter)
			w.conn = pc
			close(w.ready)
		} else {
			pc.idleSince = p.now()
			p.idle = append(p.idle, pc)
		}
		p.mu.Unlock()
		return
	}
	p.open--
	p.dispatchLocked()
	p.mu.Unlock()
	_ = pc.conn.Close()
}

// resetConn runs one statement that must leave the backend idle.
func resetConn(pc *pooled, sql string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), resetTimeout)
	defer cancel()
	status, err := roundTrip(ctx, pc.conn, sql)
	return err == nil && status == 'I'
}

// roundTrip sends one simple Query and reads until ReadyForQuery, returning
// the transaction status. A backend error comes back as a wrapped
// *pgconn.PgError. Any I/O failure marks the connection broken.
func roundTrip(ctx context.Context, c *BackendConn, sql string) (byte, error) {
	if dl, ok := ctx.Deadline(); ok {
		_ = c.Conn.SetDeadline(dl)
	}
	// Cancelling ctx must unblock a read that is waiting on the backend.
	stop := context.AfterFunc(ctx, func() { _ = c.Conn.SetDeadline(time.Unix(1, 0)) })
	defer func() {
		if stop() {
			_ = c.Conn.SetDeadline(time.Time{})
		}
	}()

	c.Frontend.Send(&pgproto3.Query{String: sql})
	if err := c.Frontend.Flush(); err != nil {
		c.Broken = true
		return 0, ctxOr(ctx, err)
	}
	var pgErr error
	for {
		msg, err := c.Frontend.Receive()
		if err != nil {
			c.Broken = true
			return 0, ctxOr(ctx, err)
		}
		switch m := msg.(type) {
		case *pgproto3.ErrorResponse:
			pgErr = pgconn.ErrorResponseToPgError(m)
		case *pgproto3.ReadyForQuery:
			if pgErr != nil {
				return m.TxStatus, pgErr
			}
			return m.TxStatus, nil
		}
	}
}

// reapLoop closes connections that have sat idle too long.
func (p *Pool) reapLoop(every time.Duration) {
	defer close(p.reaped)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.reap(p.now())
		}
	}
}

// reap closes idle connections unused for longer than the idle timeout.
func (p *Pool) reap(now time.Time) {
	p.mu.Lock()
	var drop []*pooled
	keep := p.idle[:0:0]
	for _, pc := range p.idle {
		if now.Sub(pc.idleSince) > p.cfg.Idle {
			drop = append(drop, pc)
		} else {
			keep = append(keep, pc)
		}
	}
	p.idle = keep
	p.open -= len(drop)
	p.dispatchLocked()
	p.mu.Unlock()
	closeAll(drop)
}

// Close stops the reaper, closes the idle connections and fails every queued
// Acquire with ErrPoolClosed. Later Acquire calls fail the same way, and
// leases still out are closed when released.
func (p *Pool) Close() {
	p.stopOnce.Do(func() { close(p.stop) })
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		<-p.reaped
		return
	}
	p.closed = true
	drop := p.idle
	p.idle = nil
	p.open -= len(drop)
	for e := p.waiters.Front(); e != nil; e = e.Next() {
		w := e.Value.(*waiter)
		w.err = ErrPoolClosed
		close(w.ready)
	}
	p.waiters.Init()
	p.mu.Unlock()
	closeAll(drop)
	<-p.reaped
}
