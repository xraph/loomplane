package plane

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

const testSuffix Suffix = "us-east-1.db.example.com"

const (
	testGateway = "gw-1"
	testRole    = "app"
	testPass    = "app-secret"
)

var testRouteSalt = []byte("fedcba9876543210")

// routerHarness is a Router over a fakeDialer, with short limits so tests
// that wait for one don't take 30 seconds.
type routerHarness struct {
	t       *testing.T
	r       *Router
	key     *ecdh.PrivateKey
	dialer  *fakeDialer
	version uint64
}

func newRouterHarness(t *testing.T, d Dialer, holder Holder) *routerHarness {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fd, _ := d.(*fakeDialer)
	if d == nil {
		fd = &fakeDialer{}
		d = fd
	}
	r := NewRouter(&Store{}, key, testGateway, d, holder, testSuffix)
	r.drainLimit = 2 * time.Second
	r.retireLimit = 2 * time.Second
	r.closeGrace = 100 * time.Millisecond
	h := &routerHarness{t: t, r: r, key: key, dialer: fd}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r.Drain(ctx)
	})
	return h
}

// route is an active route for label whose backend credential is sealed to
// the router's key, with a verifier for testRole.
func (h *routerHarness) route(id, label string) Route {
	h.t.Helper()
	l, err := ParseLabel(label)
	if err != nil {
		h.t.Fatal(err)
	}
	cred, err := Seal(h.key.PublicKey(), []byte(`{"user":"gateway","password":"backend-pw"}`), []byte("backend-credential:"+id))
	if err != nil {
		h.t.Fatal(err)
	}
	return Route{
		ID:    id,
		Label: l,
		State: RouteStateActive,
		Backend: Backend{
			Kind: "postgres", Host: "10.0.0.1", Port: 5432, Database: "orders", SSLMode: "disable",
			Credential: cred,
		},
		Verifiers: []Verifier{NewVerifier(testRole, testPass, testRouteSalt, 4096)},
	}
}

func (h *routerHarness) apply(routes ...Route) {
	h.t.Helper()
	h.version++
	if err := h.r.Apply(Snapshot{GatewayID: testGateway, Version: h.version, Routes: routes}); err != nil {
		h.t.Fatalf("Apply: %v", err)
	}
}

func (h *routerHarness) handle(id string) *routeHandle {
	h.r.mu.Lock()
	defer h.r.mu.Unlock()
	return h.r.routes[id]
}

func hostFor(label string) string { return label + "." + string(testSuffix) }

// start runs Serve on a fresh pipe without reading anything.
func (h *routerHarness) start(host string, startup map[string]string, password string) *testClient {
	h.t.Helper()
	cc, sc := net.Pipe()
	c := &testClient{t: h.t, conn: cc, fe: pgproto3.NewFrontend(cc, cc), done: make(chan error, 1)}
	var pw *pgproto3.PasswordMessage
	if password != "" {
		pw = &pgproto3.PasswordMessage{Password: password}
	}
	if startup == nil {
		startup = map[string]string{"user": testRole, "database": "orders"}
	}
	go func() { c.done <- h.r.Serve(context.Background(), pgproto3.NewBackend(sc, sc), sc, host, startup, pw) }()
	h.t.Cleanup(func() {
		_ = cc.Close()
		select {
		case <-c.done:
		case <-time.After(3 * time.Second):
			h.t.Error("Serve did not return after the client closed")
		}
		_ = sc.Close()
	})
	return c
}

// connect authenticates as testRole and reads the greeting.
func (h *routerHarness) connect(label string) *testClient {
	h.t.Helper()
	c := h.start(hostFor(label), nil, testPass)
	if got := c.recv(); got != "AuthenticationOk" {
		h.t.Fatalf("first message = %q, want AuthenticationOk", got)
	}
	c.greeting = c.readUntilReady()
	return c
}

func poolIsClosed(p *Pool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

type fakeHolder struct {
	mu    sync.Mutex
	calls []Label
	err   error
}

func (f *fakeHolder) Hold(_ context.Context, l Label) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, l)
	return f.err
}

func (f *fakeHolder) called() []Label {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Label(nil), f.calls...)
}

func TestRouterUnknownRoute08004(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))

	c := h.start("db.elsewhere.example.org", map[string]string{"user": testRole}, testPass)
	got := c.recv()
	if !strings.HasPrefix(got, "ErrorResponse FATAL 08004 ") {
		t.Fatalf("message = %q, want FATAL 08004", got)
	}
	for _, form := range []string{"<label>." + string(testSuffix), "options=endpoint=<label>", "user=<role>@<label>"} {
		if !strings.Contains(got, form) {
			t.Errorf("message %q does not name %q", got, form)
		}
	}
	if err := c.wait(); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("Serve = %v, want ErrNoRoute", err)
	}

	// A well-formed label with no route is 08004 too.
	c = h.start(hostFor("billing"), nil, testPass)
	got = c.recv()
	if !strings.HasPrefix(got, "ErrorResponse FATAL 08004 ") || !strings.Contains(got, "billing") {
		t.Fatalf("unknown label = %q, want FATAL 08004 naming it", got)
	}
	for _, form := range []string{"<label>." + string(testSuffix), "options=endpoint=<label>", "user=<role>@<label>"} {
		if !strings.Contains(got, form) {
			t.Errorf("unknown-label message %q does not name %q", got, form)
		}
	}
	if h.dialer.dials() != 0 {
		t.Fatal("an unknown route dialed a backend")
	}
}

func TestRouterRoutesByEndpointOptionAndUser(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))
	for _, startup := range []map[string]string{
		{"user": testRole, "database": "orders", "options": "endpoint=orders"},
		{"user": testRole + "@orders", "database": "orders"},
	} {
		c := h.start("10.1.2.3:5432", startup, testPass)
		if got := c.recv(); got != "AuthenticationOk" {
			t.Fatalf("startup %v: first message = %q, want AuthenticationOk", startup, got)
		}
		c.readUntilReady()
		want(t, "query", c.query("SELECT 1"), selectOne)
		c.terminate()
	}
}

func TestRouterHoldsBranchLabel(t *testing.T) {
	holder := &fakeHolder{}
	h := newRouterHarness(t, nil, holder)
	h.apply(h.route("r1", "orders"))

	c := h.start(hostFor("orders--feat-x"), nil, testPass)
	waitFor(t, "the hold request", func() bool { return len(holder.called()) == 1 })
	if got := holder.called()[0]; got != (Label{DB: "orders", Branch: "feat-x"}) {
		t.Fatalf("Hold label = %+v", got)
	}
	h.apply(h.route("r1", "orders"), h.route("r2", "orders--feat-x"))
	if got := c.recv(); got != "AuthenticationOk" {
		t.Fatalf("held connection got %q, want AuthenticationOk", got)
	}
	c.readUntilReady()
	want(t, "query", c.query("SELECT 1"), selectOne)
	c.terminate()

	// A plain label is never held.
	c = h.start(hostFor("billing"), nil, testPass)
	if got := c.recv(); !strings.HasPrefix(got, "ErrorResponse FATAL 08004") {
		t.Fatalf("plain unknown label = %q, want 08004", got)
	}
	if n := len(holder.called()); n != 1 {
		t.Fatalf("Hold calls = %d, want the plain label never held", n)
	}

	// A refused hold fails fast.
	holder.err = errors.New("no such branch")
	start := time.Now()
	c = h.start(hostFor("orders--feat-y"), nil, testPass)
	if got := c.recv(); !strings.HasPrefix(got, "ErrorResponse FATAL 08004") {
		t.Fatalf("refused hold = %q, want 08004", got)
	}
	if time.Since(start) > time.Second {
		t.Fatal("a refused hold waited")
	}
}

func TestRouterHoldWithoutHolderFailsFast(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))
	start := time.Now()
	c := h.start(hostFor("orders--feat-x"), nil, testPass)
	if got := c.recv(); !strings.HasPrefix(got, "ErrorResponse FATAL 08004") {
		t.Fatalf("message = %q, want 08004", got)
	}
	if time.Since(start) > time.Second {
		t.Fatal("an unknown branch waited without a Holder")
	}
}

func TestRouterHoldTimesOut(t *testing.T) {
	holder := &fakeHolder{}
	h := newRouterHarness(t, nil, holder)
	h.r.holdLimit = 150 * time.Millisecond
	h.apply(h.route("r1", "orders"))
	start := time.Now()
	c := h.start(hostFor("orders--feat-x"), nil, testPass)
	if got := c.recv(); !strings.HasPrefix(got, "ErrorResponse FATAL 08004") {
		t.Fatalf("message = %q, want 08004", got)
	}
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Fatalf("gave up after %v, want the hold limit", d)
	}
}

func TestRouterHoldingRouteIsHeld(t *testing.T) {
	holder := &fakeHolder{}
	h := newRouterHarness(t, nil, holder)
	held := h.route("r2", "orders--feat-x")
	held.State = RouteStateHolding
	h.apply(h.route("r1", "orders"), held)

	c := h.start(hostFor("orders--feat-x"), nil, testPass)
	waitFor(t, "the hold request", func() bool { return len(holder.called()) == 1 })
	held.State = RouteStateActive
	h.apply(h.route("r1", "orders"), held)
	if got := c.recv(); got != "AuthenticationOk" {
		t.Fatalf("held connection got %q, want AuthenticationOk", got)
	}
}

func TestRouterPauseQueues(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.r.pauseLimit = 300 * time.Millisecond
	paused := h.route("r1", "orders")
	paused.State = RouteStatePaused
	h.apply(paused)

	c := h.connect("orders")
	c.send(&pgproto3.Query{String: "SELECT 7"})
	time.Sleep(100 * time.Millisecond)
	if h.dialer.server(0).saw("Query SELECT 7") {
		t.Fatal("a paused route ran a new transaction")
	}
	h.apply(h.route("r1", "orders"))
	want(t, "released", c.readUntilReady(), []string{"RowDescription", "DataRow 7", "CommandComplete SELECT 1", "ReadyForQuery I"})

	h.apply(paused)
	start := time.Now()
	c.send(&pgproto3.Query{String: "SELECT 8"})
	if got := c.recv(); got != "ErrorResponse FATAL 57P01 the database route is paused" {
		t.Fatalf("message = %q, want FATAL 57P01", got)
	}
	if d := time.Since(start); d < 300*time.Millisecond {
		t.Fatalf("gave up after %v, want the pause limit", d)
	}
}

// TestRouterRouteRemoved is Review Focus 3: a session in a transaction when
// its route goes away finishes the transaction, then gets 57P01, and the
// route's pool closes once nobody uses it.
func TestRouterRouteRemoved(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))
	c := h.connect("orders")
	c.query("BEGIN")
	pool := h.handle("r1").pool

	h.apply()
	want(t, "inside", c.query("SELECT 1"), []string{"RowDescription", "DataRow 1", "CommandComplete SELECT 1", "ReadyForQuery T"})
	if poolIsClosed(pool) {
		t.Fatal("the pool closed under a live transaction")
	}
	want(t, "COMMIT", c.query("COMMIT"), []string{"CommandComplete COMMIT", "ReadyForQuery I"})
	if got := c.recv(); got != "ErrorResponse FATAL 57P01 the database route was removed" {
		t.Fatalf("after COMMIT = %q, want FATAL 57P01", got)
	}
	if err := c.wait(); err == nil {
		t.Fatal("Serve = nil after the route was removed")
	}
	waitFor(t, "the pool to close", func() bool { return poolIsClosed(pool) })
	if len(h.r.Stats()) != 0 {
		t.Fatalf("stats = %+v, want the route gone", h.r.Stats())
	}

	// New connections to the label are unknown now.
	c = h.start(hostFor("orders"), nil, testPass)
	if got := c.recv(); !strings.HasPrefix(got, "ErrorResponse FATAL 08004") {
		t.Fatalf("after removal = %q, want 08004", got)
	}
}

func TestRouterRetireLimitEndsSession(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.r.retireLimit = 150 * time.Millisecond
	h.apply(h.route("r1", "orders"))
	c := h.connect("orders")
	c.query("BEGIN")
	pool := h.handle("r1").pool

	h.apply()
	if got := c.recv(); got != "ErrorResponse FATAL 57P01 the database route was removed" {
		t.Fatalf("message = %q, want FATAL 57P01", got)
	}
	if err := c.wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve = %v, want context.Canceled", err)
	}
	waitFor(t, "the pool to close", func() bool { return poolIsClosed(pool) })
	q := h.dialer.server(0).queries()
	if q[len(q)-1] != "ROLLBACK" {
		t.Fatalf("queries = %q, want the open transaction rolled back", q)
	}
}

func TestRouterApplyReplacesChangedBackend(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))
	c := h.connect("orders")
	c.query("SELECT 1")
	old := h.handle("r1")

	// A state change alone keeps the handle and its pool.
	paused := h.route("r1", "orders")
	paused.State = RouteStatePaused
	h.apply(paused)
	if h.handle("r1") != old {
		t.Fatal("a state change replaced the route handle")
	}

	moved := h.route("r1", "orders")
	moved.Backend.Host = "10.0.0.2"
	h.apply(moved)
	if h.handle("r1") == old {
		t.Fatal("a changed backend kept the old handle")
	}
	if got := c.recv(); got != "ErrorResponse FATAL 57P01 the database route was removed" {
		t.Fatalf("session on the old backend = %q, want FATAL 57P01", got)
	}
	waitFor(t, "the old pool to close", func() bool { return poolIsClosed(old.pool) })

	c = h.connect("orders")
	want(t, "query", c.query("SELECT 1"), selectOne)
	h.dialer.mu.Lock()
	last := h.dialer.dialed[len(h.dialer.dialed)-1]
	h.dialer.mu.Unlock()
	if last.backend.Host != "10.0.0.2" {
		t.Fatalf("new connection dialed %q, want the new backend", last.backend.Host)
	}
	if last.secret != (BackendSecret{User: "gateway", Password: "backend-pw"}) {
		t.Fatal("dial did not use the opened backend credential")
	}
	c.terminate()
}

func TestRouterDrain(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))
	busy := h.connect("orders")
	busy.query("BEGIN")
	idle := h.connect("orders")
	idle.query("SELECT 1")
	pool := h.handle("r1").pool

	drained := make(chan struct{})
	go func() {
		h.r.Drain(context.Background())
		close(drained)
	}()
	if got := idle.recv(); !strings.HasPrefix(got, "ErrorResponse FATAL 57P01") {
		t.Fatalf("idle session = %q, want FATAL 57P01", got)
	}
	waitFor(t, "the router to drain", h.r.Draining)

	// The open transaction runs to its end first.
	want(t, "inside", busy.query("SELECT 2"), []string{"RowDescription", "DataRow 2", "CommandComplete SELECT 1", "ReadyForQuery T"})
	want(t, "COMMIT", busy.query("COMMIT"), []string{"CommandComplete COMMIT", "ReadyForQuery I"})
	if got := busy.recv(); !strings.HasPrefix(got, "ErrorResponse FATAL 57P01") {
		t.Fatalf("busy session after COMMIT = %q, want FATAL 57P01", got)
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("Drain did not return once every session ended")
	}
	if !poolIsClosed(pool) {
		t.Fatal("Drain left the pool open")
	}
	// Drain is idempotent.
	h.r.Drain(context.Background())
}

func TestRouterDrainRefusesNewSessions(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))
	h.r.Drain(context.Background())
	c := h.start(hostFor("orders"), nil, testPass)
	if got := c.recv(); got != "ErrorResponse FATAL 57P01 the gateway is shutting down" {
		t.Fatalf("message = %q, want FATAL 57P01", got)
	}
	if err := h.r.Apply(Snapshot{GatewayID: testGateway, Version: 99}); err == nil {
		t.Fatal("Apply after Drain = nil, want an error")
	}
}

func TestRouterDrainLimit(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.r.drainLimit = 150 * time.Millisecond
	h.apply(h.route("r1", "orders"))
	c := h.connect("orders")
	c.query("BEGIN")
	start := time.Now()
	// net.Pipe doesn't buffer, so the client reads while Drain runs.
	drained := make(chan time.Duration, 1)
	go func() {
		h.r.Drain(context.Background())
		drained <- time.Since(start)
	}()
	if got := c.recv(); got != "ErrorResponse FATAL 57P01 the gateway is shutting down" {
		t.Fatalf("message = %q, want FATAL 57P01", got)
	}
	if err := c.wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve = %v, want context.Canceled", err)
	}
	if d := <-drained; d < 150*time.Millisecond {
		t.Fatalf("Drain returned after %v, want the drain limit", d)
	}
	if q := h.dialer.server(0).queries(); q[len(q)-1] != "ROLLBACK" {
		t.Fatalf("queries = %q, want the open transaction rolled back", q)
	}
}

func TestRouterResealedCredentialKeepsPool(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))
	old := h.handle("r1")
	h.apply(h.route("r1", "orders")) // the same secret, sealed afresh
	if h.handle("r1") != old {
		t.Fatal("a re-sealed credential replaced the pool")
	}
	rt := h.route("r1", "orders")
	rt.Backend.Credential, _ = Seal(h.key.PublicKey(), []byte(`{"user":"gateway","password":"rotated"}`), []byte("backend-credential:r1"))
	h.apply(rt)
	if h.handle("r1") == old {
		t.Fatal("a rotated password kept the old pool")
	}
}

// A client that stops reading leaves its session blocked on a write. When
// the session is cancelled, the router closes the connection so the write
// returns.
func TestRouterClosesConnOfStuckClient(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.r.drainLimit = 50 * time.Millisecond
	h.apply(h.route("r1", "orders"))
	c := h.connect("orders")
	c.query("BEGIN")
	c.send(&pgproto3.Query{String: "SELECT 1"})
	h.r.Drain(context.Background())
	c.wait() // fails the test if Serve stays blocked
}

func TestRouterSetInstances(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))
	c := h.connect("orders")
	pool := h.handle("r1").pool
	if got := pool.Stats().Limit; got != 2 {
		t.Fatalf("limit before SetInstances = %d, want min(Budget(100,1), 2) = 2", got)
	}
	h.r.SetInstances(4)
	if got := pool.Stats().Limit; got != 22 {
		t.Fatalf("limit with 4 instances = %d, want 22", got)
	}
	if got := h.r.Stats()[0].MaxConnections; got != 100 {
		t.Fatalf("MaxConnections = %d, want 100", got)
	}
	c.terminate()
}

func TestRouterHubListenerTakesOneConnection(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))
	h.r.SetInstances(4)
	c := h.connect("orders")
	pool := h.handle("r1").pool
	if got := pool.Stats().Limit; got != 22 {
		t.Fatalf("limit = %d, want 22", got)
	}
	want(t, "LISTEN", c.query("LISTEN jobs"), []string{"CommandComplete LISTEN", "ReadyForQuery I"})
	if got := pool.Stats().Limit; got != 21 {
		t.Fatalf("limit with a hub listener = %d, want 21", got)
	}
	c.terminate()
}

func TestPoolLimit(t *testing.T) {
	for _, tc := range []struct {
		max, instances int
		hub            bool
		want           int
	}{
		{100, 0, false, 2},
		{100, 0, true, 1},
		{6, 0, false, 1},
		{6, 0, true, 1},
		{20, 0, true, 1},
		{100, 4, false, 22},
		{100, 4, true, 21},
		{6, 10, true, 1},
	} {
		if got := poolLimit(tc.max, tc.instances, tc.hub); got != tc.want {
			t.Errorf("poolLimit(%d, %d, %v) = %d, want %d", tc.max, tc.instances, tc.hub, got, tc.want)
		}
	}
}

// wakingDialer returns ErrWaking for its first n dials.
type wakingDialer struct {
	*fakeDialer
	n atomic.Int32
}

func (d *wakingDialer) Dial(ctx context.Context, b Backend, s BackendSecret, params map[string]string) (*BackendConn, error) {
	if d.n.Add(-1) >= 0 {
		return nil, ErrWaking
	}
	return d.fakeDialer.Dial(ctx, b, s, params)
}

func TestRouterWaking(t *testing.T) {
	d := &wakingDialer{fakeDialer: &fakeDialer{}}
	d.n.Store(2)
	h := newRouterHarness(t, d, nil)
	h.dialer = d.fakeDialer
	h.apply(h.route("r1", "orders"))
	start := time.Now()
	c := h.connect("orders")
	want(t, "first query", c.query("SELECT 1"), selectOne)
	if el := time.Since(start); el < 2*wakeRetry || el > holdLimit {
		t.Fatalf("first query after %v, want two retries within the hold", el)
	}
	if got := h.r.Stats()[0]; !got.Reachable || got.MaxConnections != 100 {
		t.Fatalf("stats = %+v, want reachable with max_connections", got)
	}
	c.terminate()
}

func TestRouterUnreachableBackend(t *testing.T) {
	d := &fakeDialer{}
	d.setErr(errors.New("dial tcp 10.0.0.1:5432: connection refused"))
	h := newRouterHarness(t, d, nil)
	h.apply(h.route("r1", "orders"))
	c := h.start(hostFor("orders"), nil, testPass)
	if got := c.recv(); got != "AuthenticationOk" {
		t.Fatalf("first message = %q", got)
	}
	got := c.recv()
	if got != "ErrorResponse FATAL 08006 the database gateway could not connect to the database" {
		t.Fatalf("message = %q, want FATAL 08006 without the address", got)
	}
	if s := h.r.Stats()[0]; s.Reachable {
		t.Fatalf("stats = %+v, want unreachable", s)
	}

	// The probe isn't cached as failed: once the backend answers, it works.
	d.setErr(nil)
	c = h.connect("orders")
	want(t, "query", c.query("SELECT 1"), selectOne)
	if s := h.r.Stats()[0]; !s.Reachable {
		t.Fatalf("stats = %+v, want reachable again", s)
	}
	c.terminate()
}

func TestRouterCredentialThatWontOpen(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	rt := h.route("r1", "orders")
	other, _ := ecdh.X25519().GenerateKey(rand.Reader)
	rt.Backend.Credential, _ = Seal(other.PublicKey(), []byte(`{"user":"gateway","password":"backend-pw"}`), []byte("backend-credential:r1"))
	h.apply(rt)
	stats := h.r.Stats()
	if len(stats) != 1 || stats[0].Reachable {
		t.Fatalf("stats = %+v, want r1 unreachable", stats)
	}
	c := h.start(hostFor("orders"), nil, testPass)
	if got := c.recv(); got != "AuthenticationOk" {
		t.Fatalf("first message = %q", got)
	}
	if got := c.recv(); got != "ErrorResponse FATAL 08006 the database gateway could not connect to the database" {
		t.Fatalf("message = %q, want FATAL 08006", got)
	}
	if h.dialer.dials() != 0 {
		t.Fatal("a route without a credential dialed")
	}

	// A credential sealed for another route doesn't open either.
	rt = h.route("r1", "orders")
	rt.Backend.Credential, _ = Seal(h.key.PublicKey(), []byte(`{"user":"gateway","password":"backend-pw"}`), []byte("backend-credential:r2"))
	h.apply(rt)
	if s := h.r.Stats()[0]; s.Reachable {
		t.Fatalf("stats = %+v, want unreachable", s)
	}
}

func TestRouterWrongPassword28P01(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))
	for _, tc := range []struct{ user, password string }{
		{testRole, "wrong"},
		{"ghost", testPass}, // no verifier for this role: the mock answers
	} {
		c := h.start(hostFor("orders"), map[string]string{"user": tc.user, "database": "orders"}, tc.password)
		want := `ErrorResponse FATAL 28P01 password authentication failed for user "` + tc.user + `"`
		if got := c.recv(); got != want {
			t.Fatalf("user %s: message = %q, want %q", tc.user, got, want)
		}
		if err := c.wait(); !errors.Is(err, ErrAuth) {
			t.Fatalf("user %s: Serve = %v, want ErrAuth", tc.user, err)
		}
	}
	if h.dialer.dials() != 0 {
		t.Fatal("a failed login dialed the backend")
	}
}

func TestRouterMockVerifier(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))
	hd := h.handle("r1")
	v := hd.verifier("ghost")
	sum := sha256.Sum256([]byte("loomplane/dbgateway/mock\x00r1\x00ghost"))
	if !reflect.DeepEqual(v.Salt, sum[:16]) || v.Iterations != 4096 || v.Role != "ghost" {
		t.Fatalf("mock verifier = %+v, want the derived salt and 4096 iterations", v)
	}
	if len(v.StoredKey) != sha256.Size || len(v.ServerKey) != sha256.Size {
		t.Fatal("mock verifier has no keys")
	}
	if !reflect.DeepEqual(hd.verifier("ghost"), v) {
		t.Fatal("mock verifier is not stable for one role")
	}
	if reflect.DeepEqual(hd.verifier("other").Salt, v.Salt) {
		t.Fatal("mock salts don't differ by role")
	}
	if VerifyPassword(v, "") == nil || VerifyPassword(v, testPass) == nil {
		t.Fatal("a mock verifier accepted a password")
	}
	if got := hd.verifier(testRole); !reflect.DeepEqual(got.Salt, testRouteSalt) {
		t.Fatal("the route's own verifier was not used")
	}
}

func TestRouterDrainingRoute(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	rt := h.route("r1", "orders")
	rt.State = RouteStateDraining
	h.apply(rt)
	c := h.start(hostFor("orders"), nil, testPass)
	if got := c.recv(); got != "ErrorResponse FATAL 57P01 the database route is draining" {
		t.Fatalf("message = %q, want FATAL 57P01", got)
	}
}

func TestRouterReplicationPassThrough(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))
	startup := map[string]string{"user": testRole, "database": "orders", "replication": "database", "application_name": "walreceiver"}
	open := func() *testClient {
		c := h.start(hostFor("orders"), startup, testPass)
		if got := c.recv(); got != "AuthenticationOk" {
			t.Fatalf("first message = %q", got)
		}
		return c
	}

	a := open()
	want(t, "greeting", a.readUntilReady(), []string{"BackendKeyData", "ReadyForQuery I"})
	want(t, "relayed", a.query("IDENTIFY_SYSTEM"), []string{"CommandComplete IDENTIFY_SYSTEM", "ReadyForQuery I"})
	h.dialer.mu.Lock()
	params := h.dialer.dialed[0].params
	h.dialer.mu.Unlock()
	if params["replication"] != "database" || params["application_name"] != "walreceiver" {
		t.Fatalf("dial params = %v, want the client's startup parameters", params)
	}
	for _, k := range []string{"user", "database", "options"} {
		if _, ok := params[k]; ok {
			t.Fatalf("dial params = %v, must not carry %s", params, k)
		}
	}
	if s := h.handle("r1").pool.Stats(); s.Open != 0 {
		t.Fatalf("pool stats = %+v, want the replication connection outside the pool", s)
	}

	b := open()
	b.readUntilReady()
	c := h.start(hostFor("orders"), startup, testPass)
	c.recv() // AuthenticationOk
	if got := c.recv(); !strings.HasPrefix(got, "ErrorResponse FATAL 53300") {
		t.Fatalf("third replication connection = %q, want FATAL 53300", got)
	}

	// Ending one frees its slot and closes its backend connection.
	a.terminate()
	waitFor(t, "the backend connection to close", func() bool { return isClosedPG(h.dialer.server(0)) })
	d := open()
	d.readUntilReady()
	want(t, "relayed", d.query("IDENTIFY_SYSTEM"), []string{"CommandComplete IDENTIFY_SYSTEM", "ReadyForQuery I"})
}

func TestRouterStats(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r2", "billing"), h.route("r1", "orders"))
	pinned := h.connect("orders")
	pinned.query("SELECT pg_advisory_lock(1)")
	idle := h.connect("orders")
	idle.query("SELECT 1")
	other := h.connect("billing")
	other.query("SELECT 1")
	if h.handle("r2").pool == h.handle("r1").pool {
		t.Fatal("two routes share a pool")
	}

	stats := h.r.Stats()
	if len(stats) != 2 || stats[1].RouteID != "r2" || stats[1].BackendConns != 1 || stats[1].Pinned != 0 {
		t.Fatalf("stats = %+v, want r1 then r2", stats)
	}
	s := stats[0]
	if s.RouteID != "r1" || s.BackendConns != 2 || s.Pinned != 1 || s.PinRate != 0.5 ||
		!s.Reachable || s.MaxConnections != 100 {
		t.Fatalf("stats = %+v", s)
	}
	pinned.terminate()
	waitFor(t, "the pin count to drop", func() bool { return h.r.Stats()[0].Pinned == 0 })
	if got := h.r.Stats()[0].PinRate; got != 0 {
		t.Fatalf("PinRate = %v, want 0", got)
	}

	s.WaitP95 = 1500 * time.Millisecond
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"route_id", "backend_conns", "pinned", "pin_rate", "wait_p95_ms", "reachable", "max_connections"} {
		if _, ok := fields[k]; !ok {
			t.Errorf("JSON %s has no %q", raw, k)
		}
	}
	if fields["wait_p95_ms"] != float64(1500) {
		t.Errorf("wait_p95_ms = %v, want 1500", fields["wait_p95_ms"])
	}
	var back RouteStats
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, s) {
		t.Fatalf("round trip = %+v, want %+v", back, s)
	}
}

func TestRouterApplyRejects(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	if err := h.r.Apply(Snapshot{GatewayID: "gw-2", Version: 1}); err == nil {
		t.Fatal("Apply took a snapshot for another gateway")
	}
	h.apply(h.route("r1", "orders"))
	if err := h.r.Apply(Snapshot{GatewayID: testGateway, Version: h.version}); !errors.Is(err, ErrStaleSnapshot) {
		t.Fatalf("Apply of the same version = %v, want ErrStaleSnapshot", err)
	}
	if h.r.store.Current().Version != h.version {
		t.Fatal("Apply did not make the snapshot current in the store")
	}
}

// R51: only a branch of a database the gateway routes is held, so a client
// can't make it ask the control plane about any label it likes.
func TestRouterHoldNeedsParentRoute(t *testing.T) {
	holder := &fakeHolder{}
	h := newRouterHarness(t, nil, holder)
	h.apply(h.route("r1", "orders"))
	start := time.Now()
	c := h.start(hostFor("billing--feat-x"), nil, testPass)
	if got := c.recv(); !strings.HasPrefix(got, "ErrorResponse FATAL 08004") {
		t.Fatalf("branch without a parent = %q, want 08004", got)
	}
	if time.Since(start) > time.Second {
		t.Fatal("a branch without a parent route waited")
	}
	if n := len(holder.called()); n != 0 {
		t.Fatalf("Hold calls = %d, want none", n)
	}
}

// blockingHolder answers every Hold with what is sent on answer.
type blockingHolder struct {
	calls  atomic.Int32
	answer chan error
}

func (b *blockingHolder) Hold(ctx context.Context, _ Label) error {
	b.calls.Add(1)
	select {
	case err := <-b.answer:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *routerHarness) holdWaiters(label string) int {
	h.r.mu.Lock()
	defer h.r.mu.Unlock()
	if c := h.r.holds[label]; c != nil {
		return c.waiters
	}
	return 0
}

func TestRouterHoldCoalesces(t *testing.T) {
	holder := &blockingHolder{answer: make(chan error)}
	h := newRouterHarness(t, nil, holder)
	h.apply(h.route("r1", "orders"))

	// Three connections for one branch share one Hold, and its answer.
	var cs []*testClient
	for range 3 {
		cs = append(cs, h.start(hostFor("orders--feat-x"), nil, testPass))
	}
	waitFor(t, "three connections on one hold", func() bool { return h.holdWaiters("orders--feat-x") == 3 })
	holder.answer <- nil
	h.apply(h.route("r1", "orders"), h.route("r2", "orders--feat-x"))
	for i, c := range cs {
		if got := c.recv(); got != "AuthenticationOk" {
			t.Fatalf("connection %d got %q, want AuthenticationOk", i, got)
		}
	}
	if n := holder.calls.Load(); n != 1 {
		t.Fatalf("Hold calls = %d, want 1", n)
	}

	// A refusal is shared too.
	cs = cs[:0]
	for range 3 {
		cs = append(cs, h.start(hostFor("orders--feat-y"), nil, testPass))
	}
	waitFor(t, "three connections on one hold", func() bool { return h.holdWaiters("orders--feat-y") == 3 })
	holder.answer <- errors.New("no such branch")
	for i, c := range cs {
		if got := c.recv(); !strings.HasPrefix(got, "ErrorResponse FATAL 08004") {
			t.Fatalf("connection %d got %q, want 08004", i, got)
		}
	}
	if n := holder.calls.Load(); n != 2 {
		t.Fatalf("Hold calls = %d, want one per branch", n)
	}
}

// stallDialer blocks every Dial until its context ends.
type stallDialer struct{ dials atomic.Int32 }

func (d *stallDialer) Dial(ctx context.Context, _ Backend, _ BackendSecret, _ map[string]string) (*BackendConn, error) {
	d.dials.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

// Important 1: sessions queued behind a probe whose dial hangs give up at
// the end of their own hold, not one hold after another.
func TestRouterProbeBoundedByHold(t *testing.T) {
	const hold = 400 * time.Millisecond
	d := &stallDialer{}
	h := newRouterHarness(t, d, nil)
	h.dialer = &fakeDialer{}
	h.r.holdLimit = hold
	h.apply(h.route("r1", "orders"))

	type result struct {
		msg string
		in  time.Duration
	}
	results := make(chan result, 3)
	for range 3 {
		start := time.Now()
		c := h.start(hostFor("orders"), nil, testPass)
		go func() {
			_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			var msgs []string
			for range 2 {
				msg, err := c.fe.Receive()
				if err != nil {
					break
				}
				msgs = append(msgs, showBackend(msg))
				if e, ok := msg.(*pgproto3.ErrorResponse); ok {
					msgs[len(msgs)-1] += " " + e.Message
				}
			}
			results <- result{strings.Join(msgs, " | "), time.Since(start)}
		}()
	}
	for range 3 {
		r := <-results
		if r.msg != "AuthenticationOk | ErrorResponse FATAL 08006 the database gateway could not connect to the database" {
			t.Fatalf("session got %q, want 08006 after auth", r.msg)
		}
		if r.in > hold+hold/2 {
			t.Fatalf("a session gave up after %v, want about the %v hold", r.in, hold)
		}
	}
}

func TestRouterReplicationDialBoundedByHold(t *testing.T) {
	const hold = 300 * time.Millisecond
	d := &stallDialer{}
	h := newRouterHarness(t, d, nil)
	h.dialer = &fakeDialer{}
	h.r.holdLimit = hold
	h.apply(h.route("r1", "orders"))
	start := time.Now()
	c := h.start(hostFor("orders"), map[string]string{"user": testRole, "database": "orders", "replication": "database"}, testPass)
	c.recv() // AuthenticationOk
	if got := c.recv(); got != "ErrorResponse FATAL 08006 the database gateway could not connect to the database" {
		t.Fatalf("message = %q, want 08006", got)
	}
	if el := time.Since(start); el > hold+hold/2 {
		t.Fatalf("the replication dial gave up after %v, want about the %v hold", el, hold)
	}
}

// countWriter counts what is written to it.
type countWriter struct{ n atomic.Int64 }

func (w *countWriter) Write(p []byte) (int, error) {
	w.n.Add(int64(len(p)))
	return len(p), nil
}

func TestRouterServeNeedsConn(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))
	w := &countWriter{}
	be := pgproto3.NewBackend(strings.NewReader(""), w)
	err := h.r.Serve(context.Background(), be, nil, hostFor("orders"), map[string]string{"user": testRole}, &pgproto3.PasswordMessage{Password: testPass})
	if !errors.Is(err, errNoConn) {
		t.Fatalf("Serve with no conn = %v, want errNoConn", err)
	}
	if w.n.Load() != 0 {
		t.Fatal("Serve wrote to a client it had no conn for")
	}
}

// gatedDialer passes dials to a fakeDialer until stall is set; from then on
// each Dial blocks, ignoring its context, until release closes.
type gatedDialer struct {
	*fakeDialer
	stall   atomic.Bool
	stalled atomic.Int32
	release chan struct{}
}

func (d *gatedDialer) Dial(ctx context.Context, b Backend, s BackendSecret, params map[string]string) (*BackendConn, error) {
	if d.stall.Load() {
		d.stalled.Add(1)
		<-d.release
		return nil, errors.New("released")
	}
	return d.fakeDialer.Dial(ctx, b, s, params)
}

// R52.4: retiring a route doesn't make Apply wait for its hub to wind down.
func TestRouterApplyDoesNotWaitForHubClose(t *testing.T) {
	d := &gatedDialer{fakeDialer: &fakeDialer{}, release: make(chan struct{})}
	h := newRouterHarness(t, d, nil)
	h.dialer = d.fakeDialer
	h.apply(h.route("r1", "orders"))
	c := h.connect("orders")
	d.stall.Store(true)
	// The hub's listener dial now hangs, so Hub.Close waits for it.
	c.send(&pgproto3.Query{String: "LISTEN jobs"})
	waitFor(t, "the hub to dial", func() bool { return d.stalled.Load() == 1 })
	start := time.Now()
	h.apply()
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Fatalf("Apply took %v, want it not to wait for the hub", el)
	}
	close(d.release)
}

func TestRouterSameSealedCredentialKeepsPool(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	rt := h.route("r1", "orders")
	h.apply(rt)
	old := h.handle("r1")
	h.apply(rt)
	if h.handle("r1") != old {
		t.Fatal("an unchanged route replaced its pool")
	}
}

func TestRouterAuthTimeout(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))
	h.r.authLimit = 200 * time.Millisecond

	// A client that sends its startup and never a password is cut off.
	c := h.start(hostFor("orders"), nil, "")
	if got := c.recv(); got != "AuthenticationCleartextPassword" {
		t.Fatalf("first message = %q, want AuthenticationCleartextPassword", got)
	}
	start := time.Now()
	if err := c.wait(); err == nil {
		t.Fatal("Serve returned nil for a client that never logged in")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v to close a silent client, want about 200ms", d)
	}
	if h.dialer.dials() != 0 {
		t.Fatal("a client that never logged in dialed the backend")
	}
}

func TestRouterAuthTimeoutStopsAfterLogin(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))
	h.r.authLimit = 200 * time.Millisecond

	c := h.connect("orders")
	// Idle well past the limit: a logged-in session isn't on the clock.
	time.Sleep(600 * time.Millisecond)
	want(t, "query after idling", c.query("SELECT 1"), selectOne)
	c.terminate()
}
