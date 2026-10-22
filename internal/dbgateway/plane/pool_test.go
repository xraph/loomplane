package plane

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

// poolServer is the scripted backend behind one fake pool connection. It
// records every simple Query and answers CommandComplete plus ReadyForQuery,
// unless a failure rule matches.
type poolServer struct {
	mu      sync.Mutex
	queries []string
	fail    map[string]string // query substring -> SQLSTATE
	status  byte              // ReadyForQuery status; 0 means 'I'
	report  []pgproto3.ParameterStatus
	closed  chan struct{}
}

func (s *poolServer) failOn(substr, code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail == nil {
		s.fail = map[string]string{}
	}
	s.fail[substr] = code
}

func (s *poolServer) setStatus(st byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = st
}

func (s *poolServer) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

func (s *poolServer) serve(conn net.Conn) {
	defer close(s.closed)
	b := pgproto3.NewBackend(conn, conn)
	for {
		msg, err := b.Receive()
		if err != nil {
			return
		}
		q, ok := msg.(*pgproto3.Query)
		if !ok {
			continue
		}
		s.mu.Lock()
		s.queries = append(s.queries, q.String)
		code := ""
		for sub, c := range s.fail {
			if strings.Contains(q.String, sub) {
				code = c
			}
		}
		st := s.status
		report := append([]pgproto3.ParameterStatus(nil), s.report...)
		s.mu.Unlock()
		if st == 0 {
			st = 'I'
		}
		if code != "" {
			b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: code, Message: "scripted failure"})
		} else {
			b.Send(&pgproto3.CommandComplete{CommandTag: []byte("OK")})
			for i := range report {
				b.Send(&report[i])
			}
		}
		b.Send(&pgproto3.ReadyForQuery{TxStatus: st})
		if b.Flush() != nil {
			return
		}
	}
}

// poolDialer hands out net.Pipe connections served by poolServers.
type poolDialer struct {
	mu      sync.Mutex
	servers []*poolServer
	params  []map[string]string
	err     error
	onNew   func(*poolServer)
	block   chan struct{} // when set, Dial waits for it to close
}

func (d *poolDialer) Dial(ctx context.Context, _ Backend, _ BackendSecret, params map[string]string) (*BackendConn, error) {
	if d.block != nil {
		select {
		case <-d.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return nil, d.err
	}
	client, server := net.Pipe()
	s := &poolServer{closed: make(chan struct{})}
	if d.onNew != nil {
		d.onNew(s)
	}
	go s.serve(server)
	d.servers = append(d.servers, s)
	cp := make(map[string]string, len(params))
	for k, v := range params {
		cp[k] = v
	}
	d.params = append(d.params, cp)
	return &BackendConn{
		Frontend:   pgproto3.NewFrontend(client, client),
		Conn:       client,
		PID:        uint32(len(d.servers)),
		Statements: map[string]bool{},
	}, nil
}

func (d *poolDialer) dials() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.servers)
}

func (d *poolDialer) server(i int) *poolServer {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.servers[i]
}

func (d *poolDialer) setErr(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.err = err
}

func newTestPool(t *testing.T, cfg PoolConfig, limit int) (*Pool, *poolDialer) {
	t.Helper()
	d := &poolDialer{}
	p := NewPool(cfg, d)
	p.SetLimit(limit)
	t.Cleanup(p.Close)
	return p, d
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func isClosed(s *poolServer) bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
	}
}

func mustAcquire(t *testing.T, p *Pool, params map[string]string) *Lease {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	l, err := p.Acquire(ctx, params)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	return l
}

func TestPoolLimitAndWait(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{Wait: 80 * time.Millisecond}, 1)
	l1 := mustAcquire(t, p, nil)

	got := make(chan *Lease, 1)
	go func() {
		l, err := p.Acquire(t.Context(), nil)
		if err != nil {
			t.Errorf("second Acquire: %v", err)
		}
		got <- l
	}()
	waitFor(t, "second Acquire to queue", func() bool { return p.Stats().Waiting == 1 })
	select {
	case <-got:
		t.Fatal("second Acquire returned while the only connection was leased")
	default:
	}
	l1.Release(LeaseClean)
	l2 := <-got
	if l2 == nil {
		t.Fatal("no second lease")
	}
	if l2.Conn() != l1.Conn() {
		t.Error("the waiter should have been handed the released connection")
	}
	if n := d.dials(); n != 1 {
		t.Errorf("dials = %d, want 1", n)
	}

	start := time.Now()
	_, err := p.Acquire(t.Context(), nil)
	if !errors.Is(err, ErrPoolTimeout) {
		t.Fatalf("Acquire at the limit = %v, want ErrPoolTimeout", err)
	}
	if e := time.Since(start); e < 60*time.Millisecond || e > time.Second {
		t.Errorf("timed out after %v, want about 80ms", e)
	}
	if s := p.Stats(); s.Waiting != 0 || s.Open != 1 {
		t.Errorf("stats after timeout = %+v", s)
	}
	l2.Release(LeaseClean)
}

func TestPoolWaitBoundedByContext(t *testing.T) {
	p, _ := newTestPool(t, PoolConfig{Wait: 5 * time.Second}, 1)
	l := mustAcquire(t, p, nil)
	defer l.Release(LeaseClean)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, err := p.Acquire(ctx, nil)
	if !errors.Is(err, ErrPoolTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire past the context deadline = %v, want ErrPoolTimeout wrapping DeadlineExceeded", err)
	}

	ctx2, cancel2 := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() { _, err := p.Acquire(ctx2, nil); errc <- err }()
	waitFor(t, "Acquire to queue", func() bool { return p.Stats().Waiting == 1 })
	cancel2()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Acquire = %v, want context.Canceled", err)
	}
	if w := p.Stats().Waiting; w != 0 {
		t.Errorf("Waiting = %d after cancel, want 0", w)
	}
}

func TestPoolWaitersAreFIFO(t *testing.T) {
	p, _ := newTestPool(t, PoolConfig{Wait: 5 * time.Second}, 1)
	hold := mustAcquire(t, p, nil)

	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup
	for i := 1; i <= 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := p.Acquire(t.Context(), nil)
			if err != nil {
				t.Errorf("waiter %d: %v", i, err)
				return
			}
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			l.Release(LeaseClean)
		}()
		waitFor(t, "waiter to queue", func() bool { return p.Stats().Waiting == i })
	}
	hold.Release(LeaseClean)
	wg.Wait()
	if !reflect.DeepEqual(order, []int{1, 2, 3, 4}) {
		t.Fatalf("service order = %v, want 1 2 3 4", order)
	}
}

func TestPoolPrefersSameParams(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{}, 2)
	a := map[string]string{"application_name": "a"}
	b := map[string]string{"application_name": "b"}
	la := mustAcquire(t, p, a)
	lb := mustAcquire(t, p, b)
	ca, cb := la.Conn(), lb.Conn()
	la.Release(LeaseClean)
	lb.Release(LeaseClean) // b is now the most recently used

	got := mustAcquire(t, p, a)
	if got.Conn() != ca {
		t.Fatal("Acquire(A) did not get the connection already set to A")
	}
	got.Release(LeaseClean)
	got = mustAcquire(t, p, b)
	if got.Conn() != cb {
		t.Fatal("Acquire(B) did not get the connection already set to B")
	}
	got.Release(LeaseClean)

	if q := d.server(0).recorded(); !reflect.DeepEqual(q, []string{"SET application_name TO a"}) {
		t.Errorf("conn A queries = %q", q)
	}
	if q := d.server(1).recorded(); !reflect.DeepEqual(q, []string{"SET application_name TO b"}) {
		t.Errorf("conn B queries = %q", q)
	}
}

func TestPoolReplaysParams(t *testing.T) {
	cfg := PoolConfig{Params: map[string]string{"application_name": "gw", "options": "-c foo=1"}}
	p, d := newTestPool(t, cfg, 1)

	l := mustAcquire(t, p, nil)
	l.Release(LeaseClean)
	if got := d.params[0]; !reflect.DeepEqual(got, cfg.Params) {
		t.Errorf("startup params = %v, want %v", got, cfg.Params)
	}
	if q := d.server(0).recorded(); len(q) != 0 {
		t.Fatalf("route defaults needed replay: %q", q)
	}

	// Overrides are SET in sorted key order, before Acquire returns. Unknown
	// keys are ignored.
	l = mustAcquire(t, p, map[string]string{"search_path": "app, public", "application_name": "'b'", "bogus": "x"})
	want := []string{"SET application_name TO 'b'; SET search_path TO app, public"}
	if q := d.server(0).recorded(); !reflect.DeepEqual(q, want) {
		t.Fatalf("replay = %q, want %q", q, want)
	}
	l.Release(LeaseClean)

	// Overrides that are no longer wanted are RESET.
	l = mustAcquire(t, p, nil)
	want = append(want, "RESET application_name; RESET search_path")
	if q := d.server(0).recorded(); !reflect.DeepEqual(q, want) {
		t.Fatalf("replay = %q, want %q", q, want)
	}
	l.Release(LeaseClean)

	// Asking for exactly what is applied costs nothing.
	l = mustAcquire(t, p, nil)
	l.Release(LeaseClean)
	if q := d.server(0).recorded(); len(q) != len(want) {
		t.Fatalf("redundant replay: %q", q)
	}
}

func TestPoolResetsOverrideToRouteDefault(t *testing.T) {
	cfg := PoolConfig{Params: map[string]string{"timezone": "Europe/London"}}
	p, d := newTestPool(t, cfg, 1)

	l := mustAcquire(t, p, map[string]string{"timezone": "'UTC'"})
	conn := l.Conn()
	l.Release(LeaseClean)
	l = mustAcquire(t, p, nil)
	if l.Conn() != conn {
		t.Fatal("expected the same connection")
	}
	l.Release(LeaseClean)
	want := []string{"SET timezone TO 'UTC'", "RESET timezone"}
	if q := d.server(0).recorded(); !reflect.DeepEqual(q, want) {
		t.Fatalf("queries = %q, want %q", q, want)
	}
}

func TestPoolSetOverrides(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{}, 1)
	l := mustAcquire(t, p, nil)
	conn := l.Conn()
	l.SetOverrides(map[string]string{"search_path": "x", "bogus": "y"})
	l.Release(LeaseClean)
	l.SetOverrides(map[string]string{"timezone": "'UTC'"}) // after Release: ignored

	// The connection already carries search_path, so asking for it is free.
	l = mustAcquire(t, p, map[string]string{"search_path": "x"})
	if l.Conn() != conn {
		t.Fatal("expected the same connection")
	}
	l.Release(LeaseClean)
	if q := d.server(0).recorded(); len(q) != 0 {
		t.Fatalf("queries = %q, want no replay", q)
	}

	// Asking for none undoes it.
	l = mustAcquire(t, p, nil)
	l.Release(LeaseClean)
	if q := d.server(0).recorded(); !reflect.DeepEqual(q, []string{"RESET search_path"}) {
		t.Fatalf("queries = %q, want RESET search_path", q)
	}

	// SetOverrides copies its argument.
	m := map[string]string{"search_path": "x"}
	l = mustAcquire(t, p, nil)
	l.SetOverrides(m)
	m["search_path"] = "changed"
	l.Release(LeaseClean)
	l = mustAcquire(t, p, map[string]string{"search_path": "x"})
	l.Release(LeaseClean)
	if q := d.server(0).recorded(); len(q) != 1 {
		t.Fatalf("queries = %q: the pool kept the caller's map", q)
	}
}

func TestPoolRecordsParameterStatus(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{}, 1)
	d.onNew = func(s *poolServer) {
		s.report = []pgproto3.ParameterStatus{{Name: "TimeZone", Value: "UTC"}}
	}
	l := mustAcquire(t, p, map[string]string{"timezone": "'UTC'"})
	defer l.Release(LeaseClean)
	if got := l.Conn().Params["TimeZone"]; got != "UTC" {
		t.Fatalf("Params[TimeZone] = %q, want UTC", got)
	}
}

func TestPoolCancelAfterReplyClosesConn(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{}, 1)
	l := mustAcquire(t, p, nil)
	l.Release(LeaseClean)

	// Cancel the context after the reply is read and before roundTrip looks
	// at it: the connection may be left with a deadline in the past.
	ctx, cancel := context.WithCancel(t.Context())
	roundTripDone = cancel
	t.Cleanup(func() { roundTripDone = nil })
	_, err := p.Acquire(ctx, map[string]string{"timezone": "'UTC'"})
	roundTripDone = nil
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire = %v, want context.Canceled", err)
	}
	waitFor(t, "connection to close", func() bool { return isClosed(d.server(0)) })
	if s := p.Stats(); s.Open != 0 || s.Idle != 0 {
		t.Fatalf("stats = %+v, want the connection dropped", s)
	}
}

func TestPoolReplayUsesValueAsRawSQL(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{}, 1)
	l := mustAcquire(t, p, map[string]string{"search_path": `"$user", public`, "timezone": "'UTC'"})
	l.Release(LeaseClean)
	want := []string{`SET search_path TO "$user", public; SET timezone TO 'UTC'`}
	if q := d.server(0).recorded(); !reflect.DeepEqual(q, want) {
		t.Fatalf("replay = %q, want %q", q, want)
	}
}

func TestPoolReplayFailureClosesConn(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{}, 1)
	l := mustAcquire(t, p, nil)
	l.Release(LeaseClean)
	d.server(0).failOn("SET", "22023")

	_, err := p.Acquire(t.Context(), map[string]string{"timezone": "Nowhere"})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "22023" {
		t.Fatalf("Acquire = %v, want a PgError with SQLSTATE 22023", err)
	}
	waitFor(t, "failed connection to close", func() bool { return isClosed(d.server(0)) })
	if s := p.Stats(); s.Open != 0 || s.Idle != 0 {
		t.Errorf("stats after failed replay = %+v, want nothing open", s)
	}
	l = mustAcquire(t, p, nil)
	l.Release(LeaseClean)
	if d.dials() != 2 {
		t.Errorf("dials = %d, want a fresh dial after the failure", d.dials())
	}
}

func TestPoolReplayIOErrorClosesConn(t *testing.T) {
	p, _ := newTestPool(t, PoolConfig{}, 1)
	l := mustAcquire(t, p, nil)
	conn := l.Conn()
	l.Release(LeaseClean)
	_ = conn.Conn.Close() // the backend went away while idle

	if _, err := p.Acquire(t.Context(), map[string]string{"timezone": "UTC"}); err == nil {
		t.Fatal("Acquire on a dead connection succeeded")
	}
	if s := p.Stats(); s.Open != 0 {
		t.Errorf("Open = %d, want the dead connection's slot freed", s.Open)
	}
}

func TestPoolDialError(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{}, 1)
	boom := errors.New("backend refused")
	d.setErr(boom)
	if _, err := p.Acquire(t.Context(), nil); !errors.Is(err, boom) {
		t.Fatalf("Acquire = %v, want the dial error", err)
	}
	if s := p.Stats(); s.Open != 0 {
		t.Fatalf("Open = %d after a failed dial, want 0", s.Open)
	}
	d.setErr(nil)
	l := mustAcquire(t, p, nil)
	l.Release(LeaseClean)
}

func TestPoolDialErrorHandsSlotToWaiter(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{Wait: 5 * time.Second}, 1)
	d.block = make(chan struct{})
	boom := errors.New("backend refused")
	d.setErr(boom)

	errs := make(chan error, 2)
	go func() { _, err := p.Acquire(t.Context(), nil); errs <- err }()
	waitFor(t, "first dial to start", func() bool { return p.Stats().Open == 1 })
	go func() { _, err := p.Acquire(t.Context(), nil); errs <- err }()
	waitFor(t, "second Acquire to queue", func() bool { return p.Stats().Waiting == 1 })

	// The failed dial frees its slot, the waiter takes it and dials too, and
	// neither caller is left hanging.
	close(d.block)
	for i := 0; i < 2; i++ {
		if err := <-errs; !errors.Is(err, boom) {
			t.Fatalf("Acquire %d = %v, want the dial error", i, err)
		}
	}
	if s := p.Stats(); s.Open != 0 || s.Waiting != 0 {
		t.Fatalf("stats = %+v, want nothing open", s)
	}
}

func TestPoolDirtyTxn(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{}, 1)
	l := mustAcquire(t, p, nil)
	l.Release(LeaseDirtyTxn)
	if q := d.server(0).recorded(); !reflect.DeepEqual(q, []string{"ROLLBACK"}) {
		t.Fatalf("queries = %q, want ROLLBACK", q)
	}
	if s := p.Stats(); s.Idle != 1 || s.Open != 1 {
		t.Fatalf("stats = %+v, want the rolled-back connection idle", s)
	}

	// A failed ROLLBACK closes the connection.
	l = mustAcquire(t, p, nil)
	d.server(0).failOn("ROLLBACK", "XX000")
	l.Release(LeaseDirtyTxn)
	waitFor(t, "connection to close", func() bool { return isClosed(d.server(0)) })
	if s := p.Stats(); s.Open != 0 || s.Idle != 0 {
		t.Fatalf("stats after failed rollback = %+v", s)
	}

	// So does a ROLLBACK that leaves the backend inside a transaction.
	d.onNew = func(s *poolServer) { s.setStatus('T') }
	l = mustAcquire(t, p, nil)
	l.Release(LeaseDirtyTxn)
	waitFor(t, "connection to close", func() bool { return isClosed(d.server(1)) })
	if s := p.Stats(); s.Open != 0 {
		t.Fatalf("Open = %d after a ROLLBACK that left status T", s.Open)
	}
}

func TestPoolSessionRelease(t *testing.T) {
	cfg := PoolConfig{Params: map[string]string{"application_name": "gw"}}
	p, d := newTestPool(t, cfg, 1)
	l := mustAcquire(t, p, map[string]string{"search_path": "x"})
	l.Conn().Statements["s1"] = true
	l.Release(LeaseSession)

	q := d.server(0).recorded()
	if len(q) != 2 || q[1] != "DISCARD ALL" {
		t.Fatalf("queries = %q, want the SET replay then DISCARD ALL", q)
	}
	if n := len(l.Conn().Statements); n != 0 {
		t.Errorf("Statements = %v, want cleared", l.Conn().Statements)
	}
	if s := p.Stats(); s.Idle != 1 {
		t.Fatalf("stats = %+v, want the connection idle", s)
	}

	// DISCARD ALL reset everything to the route defaults, so asking for them
	// needs no replay, and asking for anything else does.
	l = mustAcquire(t, p, nil)
	if got := d.server(0).recorded(); len(got) != 2 {
		t.Fatalf("defaults after DISCARD ALL needed a replay: %q", got)
	}
	l.Release(LeaseClean)
	l = mustAcquire(t, p, map[string]string{"search_path": "x"})
	l.Release(LeaseClean)
	if got := d.server(0).recorded(); got[len(got)-1] != "SET search_path TO x" {
		t.Fatalf("queries = %q, want SET search_path after the reset", got)
	}
}

func TestPoolSessionReleaseFailureCloses(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{}, 1)
	l := mustAcquire(t, p, nil)
	d.server(0).failOn("DISCARD ALL", "XX000")
	l.Release(LeaseSession)
	waitFor(t, "connection to close", func() bool { return isClosed(d.server(0)) })
	if s := p.Stats(); s.Open != 0 {
		t.Fatalf("Open = %d, want 0", s.Open)
	}
}

func TestPoolBroken(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{}, 1)
	l := mustAcquire(t, p, nil)
	l.Release(LeaseBroken)
	waitFor(t, "connection to close", func() bool { return isClosed(d.server(0)) })
	if q := d.server(0).recorded(); len(q) != 0 {
		t.Fatalf("a broken release did I/O: %q", q)
	}
	if s := p.Stats(); s.Open != 0 || s.Idle != 0 {
		t.Fatalf("stats = %+v", s)
	}
	l = mustAcquire(t, p, nil)
	if d.dials() != 2 {
		t.Fatalf("dials = %d, want a new dial after a broken release", d.dials())
	}
	// A connection flagged Broken is not reused even when released clean.
	l.Conn().Broken = true
	l.Release(LeaseClean)
	waitFor(t, "connection to close", func() bool { return isClosed(d.server(1)) })
	if s := p.Stats(); s.Open != 0 {
		t.Fatalf("Open = %d", s.Open)
	}
}

func TestPoolBrokenReleaseLetsWaiterDial(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{Wait: 5 * time.Second}, 1)
	l := mustAcquire(t, p, nil)
	got := make(chan *Lease, 1)
	go func() {
		w, err := p.Acquire(t.Context(), nil)
		if err != nil {
			t.Errorf("waiter: %v", err)
		}
		got <- w
	}()
	waitFor(t, "waiter to queue", func() bool { return p.Stats().Waiting == 1 })
	l.Release(LeaseBroken)
	w := <-got
	if w == nil {
		t.Fatal("waiter got no lease")
	}
	if d.dials() != 2 || w.Conn() == l.Conn() {
		t.Errorf("dials = %d, want the waiter on a fresh connection", d.dials())
	}
	w.Release(LeaseClean)
}

func TestPoolReleaseIsIdempotent(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{}, 2)
	l := mustAcquire(t, p, nil)
	l.Release(LeaseClean)
	l.Release(LeaseClean)
	l.Release(LeaseDirtyTxn)
	l.Release(LeaseBroken)
	if s := p.Stats(); s.Open != 1 || s.Idle != 1 {
		t.Fatalf("stats = %+v, want one idle connection", s)
	}
	if q := d.server(0).recorded(); len(q) != 0 {
		t.Fatalf("later Release calls did I/O: %q", q)
	}
	// The pool must not hand the connection to two holders.
	a := mustAcquire(t, p, nil)
	b := mustAcquire(t, p, nil)
	if a.Conn() == b.Conn() {
		t.Fatal("one connection leased twice")
	}
	a.Release(LeaseClean)
	b.Release(LeaseClean)
}

func TestPoolIdle(t *testing.T) {
	base := time.Now()
	var offset atomic.Int64 // nanoseconds past base
	d := &poolDialer{}
	p := newPool(PoolConfig{Idle: time.Minute}, d, func() time.Time { return base.Add(time.Duration(offset.Load())) })
	t.Cleanup(p.Close)
	p.SetLimit(2)
	a := mustAcquire(t, p, nil)
	b := mustAcquire(t, p, nil)
	a.Release(LeaseClean)
	offset.Store(int64(40 * time.Second))
	b.Release(LeaseClean)

	p.reap(base.Add(59 * time.Second))
	if s := p.Stats(); s.Idle != 2 {
		t.Fatalf("stats = %+v, want both idle before the timeout", s)
	}
	p.reap(base.Add(61 * time.Second))
	if s := p.Stats(); s.Idle != 1 || s.Open != 1 {
		t.Fatalf("stats = %+v, want only the older connection reaped", s)
	}
	waitFor(t, "idle connection to close", func() bool { return isClosed(d.server(0)) })
	if isClosed(d.server(1)) {
		t.Fatal("a connection idle for 21s was closed")
	}
	p.reap(base.Add(101 * time.Second))
	if s := p.Stats(); s.Open != 0 {
		t.Fatalf("stats = %+v, want everything reaped", s)
	}
}

func TestPoolIdleReaperGoroutine(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{Idle: 30 * time.Millisecond}, 1)
	l := mustAcquire(t, p, nil)
	l.Release(LeaseClean)
	waitFor(t, "reaper to close the idle connection", func() bool { return p.Stats().Open == 0 })
	waitFor(t, "connection to close", func() bool { return isClosed(d.server(0)) })
}

func TestPoolDefaults(t *testing.T) {
	p := NewPool(PoolConfig{}, &poolDialer{})
	defer p.Close()
	if p.cfg.Wait != 5*time.Second || p.cfg.Idle != 5*time.Minute {
		t.Errorf("defaults = wait %v idle %v, want 5s and 5m", p.cfg.Wait, p.cfg.Idle)
	}
	if got := reapEvery(5 * time.Minute); got != 30*time.Second {
		t.Errorf("reapEvery(5m) = %v, want 30s", got)
	}
	if got := reapEvery(20 * time.Second); got != 10*time.Second {
		t.Errorf("reapEvery(20s) = %v, want 10s", got)
	}
}

func TestPoolShrink(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{}, 3)
	ls := []*Lease{mustAcquire(t, p, nil), mustAcquire(t, p, nil), mustAcquire(t, p, nil)}
	for _, l := range ls {
		l.Release(LeaseClean)
	}
	p.SetLimit(1)
	if s := p.Stats(); s.Open != 1 || s.Idle != 1 || s.Limit != 1 {
		t.Fatalf("stats = %+v, want one idle connection under limit 1", s)
	}
	waitFor(t, "two connections to close", func() bool {
		n := 0
		for i := 0; i < 3; i++ {
			if isClosed(d.server(i)) {
				n++
			}
		}
		return n == 2
	})
}

func TestPoolShrinkClosesBusyOnRelease(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{}, 2)
	a := mustAcquire(t, p, nil)
	b := mustAcquire(t, p, nil)
	p.SetLimit(1)
	if s := p.Stats(); s.Open != 2 || s.Limit != 1 {
		t.Fatalf("stats = %+v, busy connections must survive the shrink", s)
	}
	a.Release(LeaseClean)
	waitFor(t, "over-limit connection to close", func() bool { return isClosed(d.server(0)) })
	if s := p.Stats(); s.Open != 1 || s.Idle != 0 {
		t.Fatalf("stats = %+v", s)
	}
	b.Release(LeaseClean)
	if s := p.Stats(); s.Open != 1 || s.Idle != 1 {
		t.Fatalf("stats = %+v, want the last connection kept", s)
	}
}

func TestPoolGrowServesWaiters(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{Wait: 5 * time.Second}, 1)
	l := mustAcquire(t, p, nil)
	got := make(chan *Lease, 1)
	go func() { w, _ := p.Acquire(t.Context(), nil); got <- w }()
	waitFor(t, "waiter to queue", func() bool { return p.Stats().Waiting == 1 })
	p.SetLimit(2)
	w := <-got
	if w == nil || d.dials() != 2 {
		t.Fatalf("waiter lease %v, dials %d: growing the limit should let it dial", w, d.dials())
	}
	w.Release(LeaseClean)
	l.Release(LeaseClean)
}

func TestPoolLimitBelowOneIsOne(t *testing.T) {
	p, _ := newTestPool(t, PoolConfig{}, 0)
	if got := p.Stats().Limit; got != 1 {
		t.Fatalf("Limit = %d after SetLimit(0), want 1", got)
	}
	p.SetLimit(-4)
	if got := p.Stats().Limit; got != 1 {
		t.Fatalf("Limit = %d after SetLimit(-4), want 1", got)
	}
}

func TestPoolClose(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{Wait: 5 * time.Second}, 2)
	a := mustAcquire(t, p, nil)
	b := mustAcquire(t, p, nil)

	errc := make(chan error, 1)
	go func() { _, err := p.Acquire(t.Context(), nil); errc <- err }()
	waitFor(t, "waiter to queue", func() bool { return p.Stats().Waiting == 1 })

	p.Close()
	if err := <-errc; !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("pending Acquire = %v, want ErrPoolClosed", err)
	}
	if _, err := p.Acquire(t.Context(), nil); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Acquire after Close = %v, want ErrPoolClosed", err)
	}
	p.Close() // twice is fine

	// Leases out at Close are closed when they come back, without I/O.
	a.Release(LeaseDirtyTxn)
	b.Release(LeaseClean)
	waitFor(t, "leased connections to close", func() bool { return isClosed(d.server(0)) && isClosed(d.server(1)) })
	for i := 0; i < 2; i++ {
		if q := d.server(i).recorded(); len(q) != 0 {
			t.Errorf("conn %d did I/O after Close: %q", i, q)
		}
	}
	if s := p.Stats(); s.Open != 0 || s.Idle != 0 || s.Waiting != 0 {
		t.Errorf("stats after Close = %+v", s)
	}
}

func TestPoolCloseClosesIdle(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{}, 2)
	a := mustAcquire(t, p, nil)
	b := mustAcquire(t, p, nil)
	a.Release(LeaseClean)
	b.Release(LeaseClean)
	p.Close()
	waitFor(t, "idle connections to close", func() bool { return isClosed(d.server(0)) && isClosed(d.server(1)) })
}

func TestPoolClosedWhileDialing(t *testing.T) {
	p, d := newTestPool(t, PoolConfig{}, 1)
	d.block = make(chan struct{})
	errc := make(chan error, 1)
	go func() { _, err := p.Acquire(t.Context(), nil); errc <- err }()
	waitFor(t, "dial to start", func() bool { return p.Stats().Open == 1 })
	p.Close()
	close(d.block)
	if err := <-errc; !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Acquire = %v, want ErrPoolClosed", err)
	}
	waitFor(t, "late connection to close", func() bool { return d.dials() == 1 && isClosed(d.server(0)) })
}

func TestPoolStress(t *testing.T) {
	const limit = 3
	p, _ := newTestPool(t, PoolConfig{Wait: 5 * time.Second}, limit)
	var inUse, peak atomic.Int32
	var wg sync.WaitGroup
	for g := 0; g < 12; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				params := map[string]string{"application_name": []string{"a", "b"}[(g+i)%2]}
				l, err := p.Acquire(t.Context(), params)
				if err != nil {
					t.Errorf("Acquire: %v", err)
					return
				}
				n := inUse.Add(1)
				for {
					old := peak.Load()
					if n <= old || peak.CompareAndSwap(old, n) {
						break
					}
				}
				inUse.Add(-1)
				switch i % 4 {
				case 0:
					l.Release(LeaseDirtyTxn)
				case 1:
					l.Release(LeaseSession)
				default:
					l.Release(LeaseClean)
				}
			}
		}()
	}
	wg.Wait()
	if peak.Load() > limit {
		t.Errorf("peak concurrent leases = %d, limit %d", peak.Load(), limit)
	}
	if s := p.Stats(); s.Open > limit || s.Waiting != 0 || s.Open != s.Idle {
		t.Errorf("stats = %+v", s)
	}
}
