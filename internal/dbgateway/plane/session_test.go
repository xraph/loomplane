package plane

import (
	"context"
	"errors"
	"fmt"
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

// fakePG is a scripted Postgres backend for session tests. It speaks the
// simple and extended protocols closely enough to catch a gateway that sends
// a statement twice, drops one, or answers out of order: a duplicate Parse
// fails with 42P05, an unknown statement with 26000, and after an error in
// the extended protocol it skips everything until Sync, as Postgres does.
// Like Postgres it buffers its replies to extended messages until Sync or
// Flush.
//
// SQL is matched loosely: BEGIN, COMMIT and ROLLBACK move the transaction
// status, SELECT x returns one row holding x, anything containing "fail"
// errors, anything containing "die" closes the connection, COPY reads
// CopyData until CopyDone, and everything else completes with its first word
// as the tag.
type fakePG struct {
	mu     sync.Mutex
	log    []string
	closed chan struct{}
}

type fakeState struct {
	status   byte
	skipping bool
	stmts    map[string]string
	portals  map[string]string
}

func (f *fakePG) record(msg pgproto3.FrontendMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, showFrontend(msg))
}

// received is every message the backend read, in order.
func (f *fakePG) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

// queries is the text of every simple Query the backend read.
func (f *fakePG) queries() []string {
	var out []string
	for _, m := range f.received() {
		if q, ok := strings.CutPrefix(m, "Query "); ok {
			out = append(out, q)
		}
	}
	return out
}

func (f *fakePG) saw(prefix string) bool {
	for _, m := range f.received() {
		if strings.HasPrefix(m, prefix) {
			return true
		}
	}
	return false
}

func (f *fakePG) serve(conn net.Conn) {
	defer close(f.closed)
	defer conn.Close()
	b := pgproto3.NewBackend(conn, conn)
	st := &fakeState{status: 'I', stmts: map[string]string{}, portals: map[string]string{}}
	for {
		msg, err := b.Receive()
		if err != nil {
			return
		}
		f.record(msg)
		if st.skipping {
			switch msg.(type) {
			case *pgproto3.Sync:
				st.skipping = false
			case *pgproto3.Terminate:
				return
			default:
				continue
			}
		}
		switch m := msg.(type) {
		case *pgproto3.Query:
			if !f.run(b, st, m.String, true) {
				return
			}
			if strings.Contains(m.String, "partial") {
				// ReadyForQuery and the first bytes of a notice in one write;
				// the rest of the notice never comes.
				if b.Flush() != nil {
					return
				}
				buf, _ := (&pgproto3.ReadyForQuery{TxStatus: st.status}).Encode(nil)
				notice, _ := (&pgproto3.NoticeResponse{Severity: "NOTICE", Code: "00000", Message: "cut short"}).Encode(nil)
				if _, err := conn.Write(append(buf, notice[:8]...)); err != nil {
					return
				}
				continue
			}
			b.Send(&pgproto3.ReadyForQuery{TxStatus: st.status})
			if strings.Contains(m.String, "notice") {
				// An asynchronous message right behind ReadyForQuery, in the
				// same write.
				b.Send(&pgproto3.NoticeResponse{Severity: "NOTICE", Code: "00000", Message: "late notice"})
			}
			if b.Flush() != nil {
				return
			}
		case *pgproto3.Parse:
			if _, dup := st.stmts[m.Name]; dup && m.Name != "" {
				st.fail(b, "42P05", "prepared statement already exists")
				continue
			}
			if strings.Contains(m.Query, "syntax") {
				st.fail(b, "42601", "syntax error")
				continue
			}
			st.stmts[m.Name] = m.Query
			b.Send(&pgproto3.ParseComplete{})
		case *pgproto3.Bind:
			q, ok := st.stmts[m.PreparedStatement]
			if !ok {
				st.fail(b, "26000", "prepared statement does not exist")
				continue
			}
			st.portals[m.DestinationPortal] = q
			b.Send(&pgproto3.BindComplete{})
		case *pgproto3.Describe:
			var q string
			var ok bool
			if m.ObjectType == 'S' {
				if q, ok = st.stmts[m.Name]; !ok {
					st.fail(b, "26000", "prepared statement does not exist")
					continue
				}
				b.Send(&pgproto3.ParameterDescription{})
			} else if q, ok = st.portals[m.Name]; !ok {
				st.fail(b, "34000", "portal does not exist")
				continue
			}
			if isSelect(q) {
				b.Send(rowDescription())
			} else {
				b.Send(&pgproto3.NoData{})
			}
		case *pgproto3.Execute:
			q, ok := st.portals[m.Portal]
			if !ok {
				st.fail(b, "34000", "portal does not exist")
				continue
			}
			if !f.run(b, st, q, false) {
				return
			}
		case *pgproto3.Close:
			if m.ObjectType == 'S' {
				delete(st.stmts, m.Name)
			} else {
				delete(st.portals, m.Name)
			}
			b.Send(&pgproto3.CloseComplete{})
		case *pgproto3.Sync:
			if st.status == 'I' {
				st.portals = map[string]string{}
			}
			b.Send(&pgproto3.ReadyForQuery{TxStatus: st.status})
			if b.Flush() != nil {
				return
			}
		case *pgproto3.Flush:
			if b.Flush() != nil {
				return
			}
		case *pgproto3.FunctionCall:
			b.Send(&pgproto3.FunctionCallResponse{Result: []byte("42")})
			b.Send(&pgproto3.ReadyForQuery{TxStatus: st.status})
			if b.Flush() != nil {
				return
			}
		case *pgproto3.Terminate:
			return
		}
	}
}

// fail reports an extended-protocol error. Postgres flushes an error at once,
// whatever the protocol state.
func (st *fakeState) fail(b *pgproto3.Backend, code, msg string) {
	b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: code, Message: msg})
	_ = b.Flush()
	st.skipping = true
	if st.status == 'T' {
		st.status = 'E'
	}
}

func isSelect(q string) bool { return strings.HasPrefix(strings.ToUpper(q), "SELECT") }

func rowDescription() *pgproto3.RowDescription {
	return &pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("v"), DataTypeOID: 25, DataTypeSize: -1, TypeModifier: -1}}}
}

// run executes one statement. It returns false when the connection should
// close. In the simple protocol an error doesn't start skipping.
func (f *fakePG) run(b *pgproto3.Backend, st *fakeState, sql string, simple bool) bool {
	upper := strings.ToUpper(strings.TrimSpace(sql))
	fail := func(code, msg string) {
		b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: code, Message: msg})
		if !simple {
			st.skipping = true
		}
		if st.status == 'T' {
			st.status = 'E'
		}
	}
	tag := func(t string) { b.Send(&pgproto3.CommandComplete{CommandTag: []byte(t)}) }
	switch {
	case strings.Contains(sql, "die"):
		return false
	case st.status == 'E' && !strings.HasPrefix(upper, "ROLLBACK") && !strings.HasPrefix(upper, "COMMIT"):
		fail("25P02", "current transaction is aborted")
	case strings.Contains(sql, "fail"):
		fail("42000", "scripted failure")
	case strings.HasPrefix(upper, "BEGIN"):
		st.status = 'T'
		tag("BEGIN")
	case strings.HasPrefix(upper, "COMMIT"), strings.HasPrefix(upper, "ROLLBACK"):
		st.status = 'I'
		tag(strings.Fields(upper)[0])
	case strings.HasPrefix(upper, "COPY"):
		b.Send(&pgproto3.CopyInResponse{})
		if b.Flush() != nil {
			return false
		}
		rows := 0
		for {
			msg, err := b.Receive()
			if err != nil {
				return false
			}
			f.record(msg)
			switch msg.(type) {
			case *pgproto3.CopyData:
				rows++
				continue
			case *pgproto3.CopyDone:
				tag(fmt.Sprintf("COPY %d", rows))
			case *pgproto3.CopyFail:
				fail("57014", "COPY from stdin failed")
			default:
				fail("08P01", "unexpected message during COPY")
			}
			break
		}
	case isSelect(sql):
		if simple {
			b.Send(rowDescription())
		}
		b.Send(&pgproto3.DataRow{Values: [][]byte{[]byte(strings.TrimSpace(sql[len("SELECT"):]))}})
		tag("SELECT 1")
	case upper == "DISCARD ALL":
		st.stmts = map[string]string{}
		tag("DISCARD ALL")
	case strings.HasPrefix(upper, "DEALLOCATE ALL"):
		st.stmts = map[string]string{}
		tag("DEALLOCATE ALL")
	case strings.HasPrefix(upper, "DEALLOCATE"):
		name := strings.TrimSpace(sql[len("DEALLOCATE"):])
		if _, ok := st.stmts[name]; !ok {
			fail("26000", "prepared statement does not exist")
			break
		}
		delete(st.stmts, name)
		tag("DEALLOCATE")
	case strings.HasPrefix(upper, "SET APPLICATION_NAME TO "):
		// application_name is reported, so its new value comes back.
		tag("SET")
		b.Send(&pgproto3.ParameterStatus{Name: "application_name", Value: strings.Trim(sql[len("SET application_name TO "):], "'")})
	default:
		tag(strings.Fields(upper + " OK")[0])
	}
	return true
}

func showFrontend(msg pgproto3.FrontendMessage) string {
	switch m := msg.(type) {
	case *pgproto3.Query:
		return "Query " + m.String
	case *pgproto3.Parse:
		return "Parse " + m.Name + " " + m.Query
	case *pgproto3.Bind:
		return "Bind " + m.PreparedStatement
	case *pgproto3.Describe:
		return "Describe " + string(m.ObjectType) + " " + m.Name
	case *pgproto3.Close:
		return "Close " + string(m.ObjectType) + " " + m.Name
	default:
		return strings.TrimPrefix(reflect.TypeOf(msg).String(), "*pgproto3.")
	}
}

func showBackend(msg pgproto3.BackendMessage) string {
	switch m := msg.(type) {
	case *pgproto3.DataRow:
		vals := make([]string, len(m.Values))
		for i, v := range m.Values {
			vals[i] = string(v)
		}
		return "DataRow " + strings.Join(vals, ",")
	case *pgproto3.CommandComplete:
		return "CommandComplete " + string(m.CommandTag)
	case *pgproto3.ReadyForQuery:
		return "ReadyForQuery " + string(m.TxStatus)
	case *pgproto3.ErrorResponse:
		return "ErrorResponse " + m.Severity + " " + m.Code
	case *pgproto3.ParameterStatus:
		return "ParameterStatus " + m.Name + "=" + m.Value
	default:
		return strings.TrimPrefix(reflect.TypeOf(msg).String(), "*pgproto3.")
	}
}

// fakeDialer hands out connections served by fakePG backends. err fails every
// dial, and once failWrites is set the gateway's writes to any connection it
// handed out fail.
type fakeDialer struct {
	mu         sync.Mutex
	servers    []*fakePG
	err        error
	failWrites atomic.Bool
}

func (d *fakeDialer) Dial(_ context.Context, _ Backend, _ BackendSecret, _ map[string]string) (*BackendConn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return nil, d.err
	}
	client, server := net.Pipe()
	f := &fakePG{closed: make(chan struct{})}
	go f.serve(server)
	d.servers = append(d.servers, f)
	conn := &flakyConn{Conn: client, fail: &d.failWrites}
	return &BackendConn{
		Frontend:   pgproto3.NewFrontend(conn, conn),
		Conn:       conn,
		PID:        uint32(len(d.servers)),
		Params:     map[string]string{},
		Statements: map[string]bool{},
	}, nil
}

func (d *fakeDialer) setErr(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.err = err
}

// flakyConn fails writes on demand.
type flakyConn struct {
	net.Conn
	fail *atomic.Bool
}

func (c *flakyConn) Write(p []byte) (int, error) {
	if c.fail.Load() {
		return 0, errors.New("scripted write failure")
	}
	return c.Conn.Write(p)
}

func (d *fakeDialer) server(i int) *fakePG {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.servers[i]
}

func (d *fakeDialer) dials() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.servers)
}

// fakeRoute is a SessionRoute over one pool. gateErr and params are set
// before the session starts; removed may be swapped while it runs.
type fakeRoute struct {
	pool    *Pool
	gateErr error
	params  map[string]string

	mu      sync.Mutex
	removed chan struct{}
}

func (r *fakeRoute) Pool() *Pool                     { return r.pool }
func (r *fakeRoute) Gate(context.Context) error      { return r.gateErr }
func (r *fakeRoute) ServerParams() map[string]string { return r.params }

func (r *fakeRoute) Removed() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.removed
}

func (r *fakeRoute) remove() {
	r.mu.Lock()
	defer r.mu.Unlock()
	close(r.removed)
}

func (r *fakeRoute) setRemoved(ch chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removed = ch
}

type harness struct {
	t      *testing.T
	pool   *Pool
	dialer *fakeDialer
	route  *fakeRoute
}

func newHarness(t *testing.T, limit int, wait time.Duration) *harness {
	t.Helper()
	d := &fakeDialer{}
	p := NewPool(PoolConfig{Wait: wait}, d)
	p.SetLimit(limit)
	t.Cleanup(p.Close)
	return &harness{t: t, pool: p, dialer: d, route: &fakeRoute{
		pool:    p,
		removed: make(chan struct{}),
		params: map[string]string{
			"application_name": "",
			"client_encoding":  "UTF8",
			"server_version":   "16.4",
			"TimeZone":         "UTC",
		},
	}}
}

// testClient is the client end of one session.
type testClient struct {
	t        *testing.T
	conn     net.Conn
	fe       *pgproto3.Frontend
	done     chan error
	greeting []string
}

// start runs ServeSession on a fresh pipe without reading anything.
func (h *harness) start(startup map[string]string) *testClient {
	h.t.Helper()
	cc, sc := net.Pipe()
	c := &testClient{t: h.t, conn: cc, fe: pgproto3.NewFrontend(cc, cc), done: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { c.done <- ServeSession(ctx, pgproto3.NewBackend(sc, sc), h.route, startup) }()
	h.t.Cleanup(func() {
		_ = cc.Close()
		select {
		case <-c.done:
		case <-time.After(2 * time.Second):
			h.t.Error("ServeSession did not return after the client closed")
		}
		cancel()
		_ = sc.Close()
	})
	return c
}

// connect starts a session and reads its greeting up to ReadyForQuery.
func (h *harness) connect(startup map[string]string) *testClient {
	h.t.Helper()
	c := h.start(startup)
	c.greeting = c.readUntilReady()
	return c
}

func (c *testClient) send(msgs ...pgproto3.FrontendMessage) {
	c.t.Helper()
	for _, m := range msgs {
		c.fe.Send(m)
	}
	if err := c.fe.Flush(); err != nil {
		c.t.Fatalf("client send: %v", err)
	}
}

func (c *testClient) recv() string {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	msg, err := c.fe.Receive()
	if err != nil {
		c.t.Fatalf("client receive: %v", err)
	}
	if e, ok := msg.(*pgproto3.ErrorResponse); ok {
		return showBackend(e) + " " + e.Message
	}
	return showBackend(msg)
}

func (c *testClient) read(n int) []string {
	c.t.Helper()
	out := make([]string, n)
	for i := range out {
		out[i] = c.recv()
	}
	return out
}

func (c *testClient) readUntilReady() []string {
	c.t.Helper()
	var out []string
	for {
		m := c.recv()
		out = append(out, m)
		if strings.HasPrefix(m, "ReadyForQuery") {
			return out
		}
	}
}

// query runs one simple Query and returns everything up to ReadyForQuery.
func (c *testClient) query(sql string) []string {
	c.t.Helper()
	c.send(&pgproto3.Query{String: sql})
	return c.readUntilReady()
}

// wait returns ServeSession's result.
func (c *testClient) wait() error {
	c.t.Helper()
	select {
	case err := <-c.done:
		c.done <- err
		return err
	case <-time.After(2 * time.Second):
		c.t.Fatal("ServeSession did not return")
		return nil
	}
}

func (c *testClient) terminate() {
	c.t.Helper()
	c.send(&pgproto3.Terminate{})
	if err := c.wait(); err != nil {
		c.t.Fatalf("ServeSession after Terminate = %v, want nil", err)
	}
}

func want(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s:\n got  %q\n want %q", what, got, want)
	}
}

var selectOne = []string{"RowDescription", "DataRow 1", "CommandComplete SELECT 1", "ReadyForQuery I"}

func TestSessionGreeting(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(map[string]string{
		"user": "app", "database": "orders",
		"application_name": "billing", "TimeZone": "Europe/London", "client_encoding": "utf-8",
	})
	if len(c.greeting) != 6 {
		t.Fatalf("greeting = %q", c.greeting)
	}
	want(t, "parameter status", c.greeting[:4], []string{
		"ParameterStatus TimeZone=Europe/London",
		"ParameterStatus application_name=billing",
		"ParameterStatus client_encoding=UTF8",
		"ParameterStatus server_version=16.4",
	})
	want(t, "greeting tail", c.greeting[4:], []string{"BackendKeyData", "ReadyForQuery I"})
	if h.dialer.dials() != 0 {
		t.Fatal("the greeting dialed a backend")
	}
	c.terminate()
}

func TestSessionStartupParamsBecomeOverrides(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(map[string]string{
		"user": "app", "database": "orders", "replication": "false",
		"application_name": "it's",
		"TimeZone":         "Europe/London",
		"options":          `-c search_path=app,public endpoint=orders -cstatement_timeout=5s --DateStyle=ISO`,
	})
	want(t, "query", c.query("SELECT 1"), selectOne)
	want(t, "backend queries", h.dialer.server(0).queries(), []string{
		"SET application_name TO 'it''s'; SET datestyle TO 'ISO'; SET search_path TO app,public; " +
			"SET statement_timeout TO '5s'; SET timezone TO 'Europe/London'",
		"SELECT 1",
	})
	c.terminate()

	// A backslash would read differently under standard_conforming_strings =
	// off, so such a value goes in as an E'' literal.
	h = newHarness(t, 1, time.Second)
	c = h.connect(map[string]string{"application_name": `a\b`})
	c.query("SELECT 1")
	want(t, "backend queries", h.dialer.server(0).queries(), []string{`SET application_name TO E'a\\b'`, "SELECT 1"})
	c.terminate()
}

func TestSessionStartupRejects(t *testing.T) {
	cases := []struct {
		name    string
		startup map[string]string
		msg     string
		secret  string
	}{
		{"encoding", map[string]string{"client_encoding": "LATIN1"}, "the database gateway only supports UTF8 client_encoding", ""},
		{"unknown", map[string]string{"geqo": "s3cret"}, "startup parameter geqo is not supported through the database gateway; set it with SET after connecting", "s3cret"},
		{"search path", map[string]string{"search_path": "app; DROP TABLE x"}, "startup parameter search_path is not supported", "DROP"},
		{"options pair", map[string]string{"options": "-c geqo=s3cret"}, "startup parameter geqo is not supported", "s3cret"},
		{"options junk", map[string]string{"options": "-x s3cret"}, "startup parameter options is not supported", "s3cret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, 1, time.Second)
			c := h.start(tc.startup)
			got := c.recv()
			if !strings.HasPrefix(got, "ErrorResponse FATAL 08P01 "+tc.msg) {
				t.Fatalf("first message = %q, want FATAL 08P01 %q", got, tc.msg)
			}
			if tc.secret != "" && strings.Contains(got, tc.secret) {
				t.Fatalf("error %q echoes the parameter value", got)
			}
			if err := c.wait(); err == nil {
				t.Fatal("ServeSession = nil, want an error")
			}
		})
	}
}

func TestSessionSimpleQueryReleasesOnIdle(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	want(t, "query", c.query("SELECT 1"), selectOne)
	if s := h.pool.Stats(); s.Idle != 1 || s.Open != 1 {
		t.Fatalf("stats after ReadyForQuery I = %+v, want the lease back in the pool", s)
	}
	want(t, "query", c.query("SELECT 1"), selectOne)
	if h.dialer.dials() != 1 {
		t.Fatalf("dials = %d, want the idle connection reused", h.dialer.dials())
	}
	c.terminate()
	if s := h.pool.Stats(); s.Idle != 1 {
		t.Fatalf("stats after Terminate = %+v", s)
	}
}

func TestSessionHoldsLeaseInTxn(t *testing.T) {
	h := newHarness(t, 2, time.Second)
	c := h.connect(nil)
	want(t, "BEGIN", c.query("BEGIN"), []string{"CommandComplete BEGIN", "ReadyForQuery T"})
	if s := h.pool.Stats(); s.Idle != 0 || s.Open != 1 {
		t.Fatalf("stats inside a transaction = %+v, want the lease held", s)
	}
	c.query("SELECT 1")
	if s := h.pool.Stats(); s.Idle != 0 {
		t.Fatalf("stats inside a transaction = %+v, want the lease held", s)
	}
	want(t, "COMMIT", c.query("COMMIT"), []string{"CommandComplete COMMIT", "ReadyForQuery I"})
	if s := h.pool.Stats(); s.Idle != 1 || s.Open != 1 {
		t.Fatalf("stats after COMMIT = %+v, want the lease back", s)
	}
	if n := h.dialer.dials(); n != 1 {
		t.Fatalf("dials = %d, want the whole transaction on one connection", n)
	}
	c.terminate()
}

func TestSessionPipelinedExtended(t *testing.T) {
	h := newHarness(t, 2, time.Second)
	c := h.connect(nil)
	// Everything goes out before the client reads a byte.
	c.send(
		&pgproto3.Parse{Query: "SELECT 1"}, &pgproto3.Bind{}, &pgproto3.Execute{}, &pgproto3.Sync{},
		&pgproto3.Parse{Name: "two", Query: "SELECT 2"}, &pgproto3.Bind{PreparedStatement: "two"}, &pgproto3.Execute{}, &pgproto3.Sync{},
		&pgproto3.Bind{PreparedStatement: "two"}, &pgproto3.Execute{}, &pgproto3.Sync{},
	)
	want(t, "pipelined results", c.read(14), []string{
		"ParseComplete", "BindComplete", "DataRow 1", "CommandComplete SELECT 1", "ReadyForQuery I",
		"ParseComplete", "BindComplete", "DataRow 2", "CommandComplete SELECT 1", "ReadyForQuery I",
		"BindComplete", "DataRow 2", "CommandComplete SELECT 1", "ReadyForQuery I",
	})
	if n := h.dialer.dials(); n != 1 {
		t.Fatalf("dials = %d, want one lease for the pipeline", n)
	}
	c.terminate()
}

func TestSessionPreparedAcrossLeases(t *testing.T) {
	h := newHarness(t, 2, time.Second)
	c := h.connect(nil)
	c.send(&pgproto3.Parse{Name: "s", Query: "SELECT 7"}, &pgproto3.Describe{ObjectType: 'S', Name: "s"}, &pgproto3.Sync{})
	want(t, "prepare", c.read(4), []string{"ParseComplete", "ParameterDescription", "RowDescription", "ReadyForQuery I"})

	// Keep conn 1 busy, so the next transaction lands on conn 2.
	hold := mustAcquire(t, h.pool, nil)
	c.send(&pgproto3.Bind{PreparedStatement: "s"}, &pgproto3.Execute{}, &pgproto3.Sync{})
	want(t, "execute on conn 2", c.read(4), []string{"BindComplete", "DataRow 7", "CommandComplete SELECT 1", "ReadyForQuery I"})
	hold.Release(LeaseClean)

	first := h.dialer.server(0).received()
	if len(first) < 1 || !strings.HasPrefix(first[0], "Parse lp_") {
		t.Fatalf("conn 1 got %q, want the statement under its lp_ name", first)
	}
	name := strings.Fields(first[0])[1]
	if len(name) != len("lp_")+40 || name != statementName("SELECT 7", nil) {
		t.Fatalf("backend name %q, want lp_ and 40 hex characters of the statement hash", name)
	}
	want(t, "conn 2", h.dialer.server(1).received(), []string{
		"Parse " + name + " SELECT 7", "Bind " + name, "Execute", "Sync",
	})

	// Closing the statement never reaches a backend.
	c.send(&pgproto3.Close{ObjectType: 'S', Name: "s"}, &pgproto3.Sync{})
	want(t, "close", c.read(2), []string{"CloseComplete", "ReadyForQuery I"})
	for i := 0; i < 2; i++ {
		if h.dialer.server(i).saw("Close") {
			t.Fatalf("conn %d saw the client's Close", i+1)
		}
	}
	c.terminate()
}

func TestSessionUnnamedStatementAcrossLeases(t *testing.T) {
	h := newHarness(t, 2, time.Second)
	c := h.connect(nil)
	// lib/pq and pgx's describe-exec mode prepare the unnamed statement, sync,
	// and only then bind it.
	c.send(&pgproto3.Parse{Query: "SELECT 3"}, &pgproto3.Describe{ObjectType: 'S'}, &pgproto3.Sync{})
	want(t, "prepare", c.read(4), []string{"ParseComplete", "ParameterDescription", "RowDescription", "ReadyForQuery I"})
	hold := mustAcquire(t, h.pool, nil)
	c.send(&pgproto3.Bind{}, &pgproto3.Execute{}, &pgproto3.Sync{})
	want(t, "execute", c.read(4), []string{"BindComplete", "DataRow 3", "CommandComplete SELECT 1", "ReadyForQuery I"})
	hold.Release(LeaseClean)
	want(t, "conn 2", h.dialer.server(1).received(), []string{"Parse  SELECT 3", "Bind ", "Execute", "Sync"})

	// A simple Query destroys the unnamed statement, as in Postgres.
	c.query("SELECT 1")
	c.send(&pgproto3.Bind{}, &pgproto3.Execute{}, &pgproto3.Sync{})
	got := c.read(2)
	if !strings.HasPrefix(got[0], "ErrorResponse ERROR 26000") || got[1] != "ReadyForQuery I" {
		t.Fatalf("Bind after a Query = %q, want 26000", got)
	}
	c.terminate()
}

func TestSessionPinOnAdvisoryLock(t *testing.T) {
	h := newHarness(t, 2, time.Second)
	c := h.connect(nil)
	want(t, "lock", c.query("SELECT pg_advisory_lock(1)"),
		[]string{"RowDescription", "DataRow pg_advisory_lock(1)", "CommandComplete SELECT 1", "ReadyForQuery I"})
	if s := h.pool.Stats(); s.Idle != 0 || s.Open != 1 {
		t.Fatalf("stats after the lock = %+v, want the lease pinned", s)
	}
	c.query("SELECT 1")
	if n := h.dialer.dials(); n != 1 {
		t.Fatalf("dials = %d, want the pinned connection reused", n)
	}
	c.terminate()
	q := h.dialer.server(0).queries()
	if q[len(q)-1] != "DISCARD ALL" {
		t.Fatalf("queries = %q, want DISCARD ALL on release", q)
	}
	if s := h.pool.Stats(); s.Idle != 1 {
		t.Fatalf("stats after the session = %+v", s)
	}
}

func TestSessionDisconnectMidTxn(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	c.query("BEGIN")
	c.query("SELECT 1")
	_ = c.conn.Close()
	if err := c.wait(); err != nil {
		t.Fatalf("ServeSession after the client left = %v, want nil", err)
	}
	q := h.dialer.server(0).queries()
	if q[len(q)-1] != "ROLLBACK" {
		t.Fatalf("queries = %q, want ROLLBACK on release", q)
	}
	if s := h.pool.Stats(); s.Idle != 1 {
		t.Fatalf("stats = %+v, want the rolled-back connection idle", s)
	}
}

func TestSessionDisconnectMidQueryBreaksLease(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	// The backend answers Parse at once but holds the rest for a Sync that
	// never comes, so the client leaves with replies still owed.
	c.send(&pgproto3.Parse{Query: "SELECT 1"}, &pgproto3.Flush{})
	want(t, "parse", c.read(1), []string{"ParseComplete"})
	c.send(&pgproto3.Bind{}, &pgproto3.Execute{})
	_ = c.conn.Close()
	if err := c.wait(); err != nil {
		t.Fatalf("ServeSession = %v", err)
	}
	waitFor(t, "the connection to close", func() bool { return isClosedPG(h.dialer.server(0)) })
	if s := h.pool.Stats(); s.Open != 0 {
		t.Fatalf("stats = %+v, want the connection dropped", s)
	}
}

func isClosedPG(f *fakePG) bool {
	select {
	case <-f.closed:
		return true
	default:
		return false
	}
}

func TestSessionPoolTimeout(t *testing.T) {
	h := newHarness(t, 1, 40*time.Millisecond)
	hold := mustAcquire(t, h.pool, nil)
	c := h.connect(nil)

	got := c.query("SELECT 1")
	if len(got) != 2 || !strings.HasPrefix(got[0], "ErrorResponse ERROR 53300 the database gateway's connection pool is busy, try again") || got[1] != "ReadyForQuery I" {
		t.Fatalf("simple query at the limit = %q, want 53300 then ReadyForQuery I", got)
	}

	// In the extended protocol everything up to Sync is skipped.
	c.send(&pgproto3.Parse{Query: "SELECT 1"}, &pgproto3.Bind{}, &pgproto3.Execute{}, &pgproto3.Sync{})
	got = c.read(2)
	if !strings.HasPrefix(got[0], "ErrorResponse ERROR 53300") || got[1] != "ReadyForQuery I" {
		t.Fatalf("pipeline at the limit = %q, want 53300 then ReadyForQuery I", got)
	}

	hold.Release(LeaseClean)
	want(t, "query once a connection is free", c.query("SELECT 1"), selectOne)
	c.terminate()
}

func TestSessionBackendDies(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	c.send(&pgproto3.Query{String: "SELECT die"})
	if got := c.recv(); got != "ErrorResponse FATAL 08006 the database gateway lost its connection to the database" {
		t.Fatalf("reply = %q, want FATAL 08006", got)
	}
	if s := h.pool.Stats(); s.Open != 0 || s.Idle != 0 {
		t.Fatalf("stats = %+v, want the connection released broken", s)
	}
	if err := c.wait(); err == nil {
		t.Fatal("ServeSession = nil after the backend died")
	}
}

func TestSessionCopyPinsStatement(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	c.send(&pgproto3.Query{String: "COPY t FROM STDIN"})
	want(t, "copy start", c.read(1), []string{"CopyInResponse"})
	if s := h.pool.Stats(); s.Idle != 0 {
		t.Fatalf("stats during COPY = %+v, want the lease held", s)
	}
	c.send(&pgproto3.CopyData{Data: []byte("1\n")}, &pgproto3.CopyData{Data: []byte("2\n")})
	if s := h.pool.Stats(); s.Idle != 0 {
		t.Fatalf("stats during COPY = %+v, want the lease held", s)
	}
	c.send(&pgproto3.CopyDone{})
	want(t, "copy end", c.readUntilReady(), []string{"CommandComplete COPY 2", "ReadyForQuery I"})
	if s := h.pool.Stats(); s.Idle != 1 {
		t.Fatalf("stats after COPY = %+v, want the lease released", s)
	}
	c.terminate()
}

func TestSessionSetInAutocommitUpdatesOverrides(t *testing.T) {
	h := newHarness(t, 2, time.Second)
	c := h.connect(nil)
	want(t, "SET", c.query("SET search_path TO app"), []string{"CommandComplete SET", "ReadyForQuery I"})
	if s := h.pool.Stats(); s.Idle != 1 {
		t.Fatalf("stats after SET = %+v, want the lease released, not pinned", s)
	}
	// The released connection already carries the override, so this session
	// gets it back with no replay, and another session gets a RESET.
	c.query("SELECT 1")
	want(t, "conn 1", h.dialer.server(0).queries(), []string{"SET search_path TO app", "SELECT 1"})

	hold := mustAcquire(t, h.pool, nil)
	c.query("SELECT 2")
	hold.Release(LeaseClean)
	want(t, "conn 2", h.dialer.server(1).queries(), []string{"SET search_path TO app", "SELECT 2"})
	want(t, "conn 1", h.dialer.server(0).queries(), []string{"SET search_path TO app", "SELECT 1", "RESET search_path"})

	// RESET drops the override again.
	c.query("RESET search_path")
	hold = mustAcquire(t, h.pool, nil) // conn 2, with no override
	c.query("SELECT 3")                // conn 1
	hold.Release(LeaseClean)
	want(t, "conn 1", h.dialer.server(0).queries(), []string{"SET search_path TO app", "SELECT 1", "RESET search_path", "SELECT 3"})
	c.terminate()
}

func TestSessionExtendedSetUpdatesOverrides(t *testing.T) {
	h := newHarness(t, 2, time.Second)
	c := h.connect(nil)
	// pgx prepares even a SET as a named statement.
	c.send(&pgproto3.Parse{Name: "set", Query: "SET timezone TO 'UTC'"}, &pgproto3.Sync{},
		&pgproto3.Bind{PreparedStatement: "set"}, &pgproto3.Execute{}, &pgproto3.Sync{})
	want(t, "set", c.read(5), []string{"ParseComplete", "ReadyForQuery I", "BindComplete", "CommandComplete SET", "ReadyForQuery I"})
	if s := h.pool.Stats(); s.Idle != 1 {
		t.Fatalf("stats = %+v, want the lease released", s)
	}
	hold := mustAcquire(t, h.pool, map[string]string{"timezone": "'UTC'"}) // conn 1, already set
	c.query("SELECT 1")
	hold.Release(LeaseClean)
	want(t, "conn 2", h.dialer.server(1).queries(), []string{"SET timezone TO 'UTC'", "SELECT 1"})
	c.terminate()
}

func TestSessionSetInTransactionPins(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	c.query("BEGIN")
	c.query("SET search_path TO app")
	want(t, "COMMIT", c.query("COMMIT"), []string{"CommandComplete COMMIT", "ReadyForQuery I"})
	if s := h.pool.Stats(); s.Idle != 0 {
		t.Fatalf("stats = %+v: a SET inside a transaction should pin the session", s)
	}
	c.terminate()
	q := h.dialer.server(0).queries()
	if q[len(q)-1] != "DISCARD ALL" {
		t.Fatalf("queries = %q, want DISCARD ALL", q)
	}
}

func TestSessionDeallocate(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	c.send(&pgproto3.Parse{Name: "s", Query: "SELECT 1"}, &pgproto3.Parse{Name: "t", Query: "SELECT 2"}, &pgproto3.Sync{})
	c.read(3)

	// A statement in the registry is dropped by the gateway itself.
	want(t, "deallocate s", c.query("DEALLOCATE s"), []string{"CommandComplete DEALLOCATE", "ReadyForQuery I"})
	if h.dialer.server(0).saw("Query DEALLOCATE") {
		t.Fatal("DEALLOCATE of a cached statement reached the backend")
	}
	c.send(&pgproto3.Bind{PreparedStatement: "s"}, &pgproto3.Execute{}, &pgproto3.Sync{})
	got := c.read(2)
	if !strings.HasPrefix(got[0], "ErrorResponse ERROR 26000") {
		t.Fatalf("Bind of a deallocated statement = %q, want 26000", got)
	}
	if !h.dialer.server(0).saw("Bind s") {
		t.Fatal("Bind of an unknown statement should go to the backend under the client's name")
	}

	// A name the gateway doesn't know is the backend's business.
	got = c.query("DEALLOCATE nope")
	if !strings.HasPrefix(got[0], "ErrorResponse ERROR 26000") {
		t.Fatalf("DEALLOCATE nope = %q, want the backend's 26000", got)
	}

	// DEALLOCATE ALL, unpinned, is answered here too.
	want(t, "deallocate all", c.query("DEALLOCATE ALL"), []string{"CommandComplete DEALLOCATE ALL", "ReadyForQuery I"})
	if h.dialer.server(0).saw("Query DEALLOCATE ALL") {
		t.Fatal("DEALLOCATE ALL reached the backend of an unpinned session")
	}
	c.send(&pgproto3.Bind{PreparedStatement: "t"}, &pgproto3.Execute{}, &pgproto3.Sync{})
	if got = c.read(2); !strings.HasPrefix(got[0], "ErrorResponse ERROR 26000") {
		t.Fatalf("Bind after DEALLOCATE ALL = %q, want 26000", got)
	}

	// In a failed transaction Postgres refuses DEALLOCATE, and so does the
	// gateway.
	c.send(&pgproto3.Parse{Name: "u", Query: "SELECT 1"}, &pgproto3.Sync{})
	c.read(2)
	c.query("BEGIN")
	c.query("SELECT fail")
	got = c.query("DEALLOCATE u")
	if len(got) != 2 || !strings.HasPrefix(got[0], "ErrorResponse ERROR 25P02") || got[1] != "ReadyForQuery E" {
		t.Fatalf("DEALLOCATE in a failed transaction = %q, want 25P02", got)
	}
	c.query("ROLLBACK")
	c.send(&pgproto3.Bind{PreparedStatement: "u"}, &pgproto3.Execute{}, &pgproto3.Sync{})
	want(t, "u survives", c.read(4), []string{"BindComplete", "DataRow 1", "CommandComplete SELECT 1", "ReadyForQuery I"})
	c.terminate()
}

func TestSessionDeallocateAllWhenPinned(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	c.query("SELECT pg_advisory_lock(1)")
	c.send(&pgproto3.Parse{Name: "s", Query: "SELECT 1"}, &pgproto3.Sync{})
	c.read(2)
	want(t, "deallocate all", c.query("DEALLOCATE ALL"), []string{"CommandComplete DEALLOCATE ALL", "ReadyForQuery I"})
	if !h.dialer.server(0).saw("Query DEALLOCATE ALL") {
		t.Fatal("a pinned session's DEALLOCATE ALL should reach the backend")
	}
	// The backend dropped the lp_ statement, so preparing it again must send
	// a fresh Parse rather than trust the cache.
	c.send(&pgproto3.Parse{Name: "s", Query: "SELECT 1"}, &pgproto3.Bind{PreparedStatement: "s"}, &pgproto3.Execute{}, &pgproto3.Sync{})
	want(t, "prepare again", c.read(5), []string{"ParseComplete", "BindComplete", "DataRow 1", "CommandComplete SELECT 1", "ReadyForQuery I"})
	c.terminate()
}

func TestSessionDiscardPlansKeepsStatements(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	c.send(&pgproto3.Parse{Name: "s", Query: "SELECT 1"}, &pgproto3.Sync{})
	c.read(2)
	want(t, "discard plans", c.query("DISCARD PLANS"), []string{"CommandComplete DISCARD", "ReadyForQuery I"})
	c.send(&pgproto3.Bind{PreparedStatement: "s"}, &pgproto3.Execute{}, &pgproto3.Sync{})
	want(t, "s survives", c.read(4), []string{"BindComplete", "DataRow 1", "CommandComplete SELECT 1", "ReadyForQuery I"})
	c.terminate()
}

func TestSessionDiscardAll(t *testing.T) {
	h := newHarness(t, 2, time.Second)
	c := h.connect(map[string]string{"application_name": "billing"})
	c.send(&pgproto3.Parse{Name: "s", Query: "SELECT 1"}, &pgproto3.Sync{})
	c.read(2)
	c.query("SET search_path TO app")
	want(t, "discard", c.query("DISCARD ALL"), []string{"CommandComplete DISCARD ALL", "ReadyForQuery I"})
	if s := h.pool.Stats(); s.Idle != 1 {
		t.Fatalf("stats = %+v, want DISCARD ALL to release, not pin", s)
	}
	// The statement is gone.
	c.send(&pgproto3.Bind{PreparedStatement: "s"}, &pgproto3.Execute{}, &pgproto3.Sync{})
	if got := c.read(2); !strings.HasPrefix(got[0], "ErrorResponse ERROR 26000") {
		t.Fatalf("Bind after DISCARD ALL = %q, want 26000", got)
	}
	// So is the SET. Parameters return to their startup values, as in
	// Postgres, so application_name is billing again while the connection
	// itself went back to the route default and has to be told.
	want(t, "conn 1", h.dialer.server(0).queries(), []string{
		"SET application_name TO 'billing'", "SET search_path TO app", "DISCARD ALL", "SET application_name TO 'billing'",
	})
	hold := mustAcquire(t, h.pool, nil)
	c.query("SELECT 1")
	hold.Release(LeaseClean)
	want(t, "conn 2", h.dialer.server(1).queries(), []string{"SET application_name TO 'billing'", "SELECT 1"})
	c.terminate()
}

func TestSessionResetRestoresStartupValue(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(map[string]string{"search_path": "app"})
	c.query("SET search_path TO other")
	c.query("RESET search_path")
	c.query("SELECT 1")
	want(t, "conn 1", h.dialer.server(0).queries(), []string{
		"SET search_path TO app", "SET search_path TO other", "RESET search_path",
		// The backend's RESET went to the route default; the client's
		// session resets to its startup value.
		"SET search_path TO app", "SELECT 1",
	})
	c.terminate()
}

func TestSessionExtendedErrorSkipsToSync(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	c.send(&pgproto3.Parse{Name: "s1", Query: "SELECT 1"}, &pgproto3.Sync{})
	c.read(2)

	// The Bind fails, so Postgres skips the Execute and the Close. The gateway
	// must not answer the Close it would otherwise answer itself, nor drop s1.
	c.send(
		&pgproto3.Bind{PreparedStatement: "missing"}, &pgproto3.Execute{},
		&pgproto3.Close{ObjectType: 'S', Name: "s1"}, &pgproto3.Sync{},
	)
	got := c.read(2)
	if !strings.HasPrefix(got[0], "ErrorResponse ERROR 26000") || got[1] != "ReadyForQuery I" {
		t.Fatalf("replies = %q, want the Bind error then ReadyForQuery", got)
	}
	c.send(&pgproto3.Bind{PreparedStatement: "s1"}, &pgproto3.Execute{}, &pgproto3.Sync{})
	want(t, "s1 survives the skipped Close", c.read(4), []string{"BindComplete", "DataRow 1", "CommandComplete SELECT 1", "ReadyForQuery I"})
	if s := h.pool.Stats(); s.Idle != 1 {
		t.Fatalf("stats = %+v, want the lease released", s)
	}
	c.terminate()
}

func TestSessionExtendedErrorSkipsLaterMessages(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	// The error arrives before the client has sent the rest of the pipeline,
	// so the gateway has to skip those messages itself.
	c.send(&pgproto3.Parse{Query: "SELECT syntax"}, &pgproto3.Flush{})
	if got := c.recv(); !strings.HasPrefix(got, "ErrorResponse ERROR 42601") {
		t.Fatalf("reply = %q, want the Parse error", got)
	}
	c.send(&pgproto3.Bind{}, &pgproto3.Execute{}, &pgproto3.Close{ObjectType: 'S', Name: "x"}, &pgproto3.Sync{})
	want(t, "after the error", c.read(1), []string{"ReadyForQuery I"})
	want(t, "query after the error", c.query("SELECT 1"), selectOne)
	for _, m := range h.dialer.server(0).received() {
		if strings.HasPrefix(m, "Bind") || strings.HasPrefix(m, "Execute") {
			t.Fatalf("backend got %q after the error, want it skipped", m)
		}
	}
	c.terminate()
}

func TestSessionRemovedWhileIdle(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	c.query("SELECT 1")
	h.route.remove()
	if got := c.recv(); got != "ErrorResponse FATAL 57P01 the database route was removed" {
		t.Fatalf("message = %q, want FATAL 57P01", got)
	}
	if err := c.wait(); err == nil {
		t.Fatal("ServeSession = nil after the route was removed")
	}
}

func TestSessionRemovedAtBoundary(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	c.query("BEGIN")
	h.route.remove()
	// The open transaction runs to its end first.
	want(t, "inside", c.query("SELECT 1"), []string{"RowDescription", "DataRow 1", "CommandComplete SELECT 1", "ReadyForQuery T"})
	want(t, "COMMIT", c.query("COMMIT"), []string{"CommandComplete COMMIT", "ReadyForQuery I"})
	if got := c.recv(); got != "ErrorResponse FATAL 57P01 the database route was removed" {
		t.Fatalf("after COMMIT = %q, want FATAL 57P01", got)
	}
	if err := c.wait(); err == nil {
		t.Fatal("ServeSession = nil after the route was removed")
	}
	if s := h.pool.Stats(); s.Idle != 1 {
		t.Fatalf("stats = %+v, want the connection released clean", s)
	}
}

func TestSessionRemovedBeforeAcquire(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	// A nil channel never fires, so the idle session doesn't notice the swap
	// below and only the check before acquiring can see it.
	h.route.setRemoved(nil)
	c := h.connect(nil)
	removed := make(chan struct{})
	close(removed)
	h.route.setRemoved(removed)
	c.send(&pgproto3.Query{String: "SELECT 1"})
	if got := c.recv(); got != "ErrorResponse FATAL 57P01 the database route was removed" {
		t.Fatalf("message = %q, want FATAL 57P01", got)
	}
	if h.dialer.dials() != 0 {
		t.Fatal("a removed route dialed a backend")
	}
}

func TestSessionGateFailure(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	h.route.gateErr = errors.New("the database route stayed paused for too long")
	c := h.connect(nil)
	c.send(&pgproto3.Query{String: "SELECT 1"})
	if got := c.recv(); got != "ErrorResponse FATAL 57P01 the database route stayed paused for too long" {
		t.Fatalf("message = %q, want FATAL 57P01 with the gate's message", got)
	}
	if err := c.wait(); err == nil {
		t.Fatal("ServeSession = nil after the gate failed")
	}
	if h.dialer.dials() != 0 {
		t.Fatal("a failed gate dialed a backend")
	}
}

func TestSessionContextCancel(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	cc, sc := net.Pipe()
	defer cc.Close()
	defer sc.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeSession(ctx, pgproto3.NewBackend(sc, sc), h.route, nil) }()
	c := &testClient{t: t, conn: cc, fe: pgproto3.NewFrontend(cc, cc)}
	c.readUntilReady()
	c.query("BEGIN")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ServeSession = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeSession did not return on cancel")
	}
	q := h.dialer.server(0).queries()
	if q[len(q)-1] != "ROLLBACK" {
		t.Fatalf("queries = %q, want ROLLBACK", q)
	}
}

func TestSessionAcquireErrors(t *testing.T) {
	// A replay or dial error that carries a Postgres error reaches the client
	// as that error, and the session carries on.
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	h.dialer.setErr(&pgconn.PgError{Severity: "FATAL", Code: "53300", Message: "too many connections for role"})
	got := c.query("SELECT 1")
	if len(got) != 2 || got[0] != "ErrorResponse ERROR 53300 too many connections for role" || got[1] != "ReadyForQuery I" {
		t.Fatalf("query = %q, want the backend's error as a non-fatal ERROR", got)
	}

	// Any other dial error is reported without its detail, which names the
	// backend's address.
	h.dialer.setErr(errors.New("dial tcp 10.0.0.7:5432: connection refused"))
	c.send(&pgproto3.Parse{Query: "SELECT 1"}, &pgproto3.Bind{}, &pgproto3.Execute{}, &pgproto3.Sync{})
	got = c.read(2)
	if got[0] != "ErrorResponse ERROR 08006 the database gateway could not connect to the database" || got[1] != "ReadyForQuery I" {
		t.Fatalf("pipeline = %q, want 08006 then ReadyForQuery", got)
	}

	h.dialer.setErr(nil)
	want(t, "query once the backend is back", c.query("SELECT 1"), selectOne)

	// A closed pool means the route is gone.
	h.pool.Close()
	c.send(&pgproto3.Query{String: "SELECT 1"})
	if got := c.recv(); got != "ErrorResponse FATAL 57P01 the database route was removed" {
		t.Fatalf("query on a closed pool = %q, want FATAL 57P01", got)
	}
	if err := c.wait(); err == nil {
		t.Fatal("ServeSession = nil on a closed pool")
	}
}

func TestSessionBackendWriteFails(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	c.query("BEGIN")
	h.dialer.failWrites.Store(true)
	c.send(&pgproto3.Query{String: "SELECT 1"})
	if got := c.recv(); got != "ErrorResponse FATAL 08006 the database gateway lost its connection to the database" {
		t.Fatalf("reply = %q, want FATAL 08006", got)
	}
	if err := c.wait(); err == nil {
		t.Fatal("ServeSession = nil after a failed write")
	}
	if s := h.pool.Stats(); s.Open != 0 {
		t.Fatalf("stats = %+v, want the connection released broken", s)
	}
}

func TestSessionFunctionCallPins(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	c.send(&pgproto3.FunctionCall{Function: 2508})
	want(t, "call", c.read(2), []string{"FunctionCallResponse", "ReadyForQuery I"})
	if s := h.pool.Stats(); s.Idle != 0 {
		t.Fatalf("stats = %+v: a FunctionCall should pin", s)
	}
	c.terminate()
}

func TestSessionClosePortalAndUnnamed(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	// With no lease, there is no portal or unnamed statement to close.
	c.send(&pgproto3.Close{ObjectType: 'P', Name: "p"}, &pgproto3.Close{ObjectType: 'S'}, &pgproto3.Sync{})
	want(t, "close without a lease", c.read(3), []string{"CloseComplete", "CloseComplete", "ReadyForQuery I"})
	if h.dialer.dials() != 0 {
		t.Fatal("closing nothing dialed a backend")
	}

	// Inside a transaction they are the backend's.
	c.query("BEGIN")
	c.send(
		&pgproto3.Parse{Query: "SELECT 5"}, &pgproto3.Bind{DestinationPortal: "p"},
		&pgproto3.Describe{ObjectType: 'P', Name: "p"}, &pgproto3.Execute{Portal: "p"},
		&pgproto3.Close{ObjectType: 'P', Name: "p"}, &pgproto3.Close{ObjectType: 'S'}, &pgproto3.Sync{},
	)
	want(t, "portal", c.read(8), []string{
		"ParseComplete", "BindComplete", "RowDescription", "DataRow 5", "CommandComplete SELECT 1",
		"CloseComplete", "CloseComplete", "ReadyForQuery T",
	})
	if got := h.dialer.server(0).received(); !reflect.DeepEqual(got[len(got)-3:], []string{"Close P p", "Close S ", "Sync"}) {
		t.Fatalf("backend got %q, want both Closes forwarded", got)
	}
	// The unnamed statement is gone, so binding it fails.
	c.send(&pgproto3.Bind{}, &pgproto3.Sync{})
	if got := c.read(2); !strings.HasPrefix(got[0], "ErrorResponse ERROR 26000") {
		t.Fatalf("Bind of a closed unnamed statement = %q, want 26000", got)
	}
	c.query("ROLLBACK")
	c.terminate()
}

func TestSessionForwardsParameterStatus(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	want(t, "SET", c.query("SET application_name TO 'billing'"),
		[]string{"CommandComplete SET", "ParameterStatus application_name=billing", "ReadyForQuery I"})
	l := mustAcquire(t, h.pool, map[string]string{"application_name": "'billing'"})
	defer l.Release(LeaseClean)
	if got := l.Conn().Params["application_name"]; got != "billing" {
		t.Fatalf("Params[application_name] = %q, want billing", got)
	}
}

func TestSessionPipelinedSimpleQueries(t *testing.T) {
	h := newHarness(t, 2, time.Second)
	c := h.connect(nil)
	c.send(&pgproto3.Query{String: "SET search_path TO app"}, &pgproto3.Query{String: "SELECT 1"}, &pgproto3.Query{String: "SELECT 2"})
	want(t, "results", c.read(10), []string{
		"CommandComplete SET", "ReadyForQuery I",
		"RowDescription", "DataRow 1", "CommandComplete SELECT 1", "ReadyForQuery I",
		"RowDescription", "DataRow 2", "CommandComplete SELECT 1", "ReadyForQuery I",
	})
	hold := mustAcquire(t, h.pool, map[string]string{"search_path": "app"})
	c.query("SELECT 3")
	hold.Release(LeaseClean)
	want(t, "conn 2", h.dialer.server(1).queries(), []string{"SET search_path TO app", "SELECT 3"})
	c.terminate()
}

// TestSessionDeepPipeline sends many statements before reading any reply. The
// backend blocks writing replies while the gateway is still forwarding, which
// deadlocks a reader that waits for the forwarding to stop.
func TestSessionDeepPipeline(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	const n = 40
	var msgs []pgproto3.FrontendMessage
	var wantRows []string
	for i := 0; i < n; i++ {
		v := fmt.Sprint(i)
		if i%2 == 0 {
			msgs = append(msgs, &pgproto3.Query{String: "SELECT " + v})
		} else {
			msgs = append(msgs, &pgproto3.Parse{Query: "SELECT " + v}, &pgproto3.Bind{}, &pgproto3.Execute{}, &pgproto3.Sync{})
		}
		wantRows = append(wantRows, "DataRow "+v)
	}
	c.send(msgs...)
	var rows []string
	for len(rows) < n {
		if m := c.recv(); strings.HasPrefix(m, "DataRow") {
			rows = append(rows, m)
		}
	}
	want(t, "rows in order", rows, wantRows)
	c.recv() // the last ReadyForQuery
	c.terminate()
}

func TestSessionSetInsideExtendedTransactionPins(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	// One Sync, three statements, and the SET was rolled back. The final
	// status alone can't show that, so the gateway pins.
	c.send(
		&pgproto3.Parse{Query: "BEGIN"}, &pgproto3.Bind{}, &pgproto3.Execute{},
		&pgproto3.Parse{Query: "SET search_path TO app"}, &pgproto3.Bind{}, &pgproto3.Execute{},
		&pgproto3.Parse{Query: "ROLLBACK"}, &pgproto3.Bind{}, &pgproto3.Execute{},
		&pgproto3.Sync{},
	)
	if got := c.read(10); got[9] != "ReadyForQuery I" {
		t.Fatalf("replies = %q", got)
	}
	if s := h.pool.Stats(); s.Idle != 0 {
		t.Fatalf("stats = %+v, want the session pinned", s)
	}
	c.terminate()
}

func TestSessionProtocolViolation(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	// 'Z' is a backend message type; no client may send it.
	if _, err := c.conn.Write([]byte{'Z', 0, 0, 0, 5, 'I'}); err != nil {
		t.Fatal(err)
	}
	if got := c.recv(); !strings.HasPrefix(got, "ErrorResponse FATAL 08P01") {
		t.Fatalf("reply = %q, want FATAL 08P01", got)
	}
	if err := c.wait(); err == nil {
		t.Fatal("ServeSession = nil after a protocol violation")
	}
}

func TestSessionDeallocateInternalName(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	c.query("SELECT pg_advisory_lock(1)")
	c.send(&pgproto3.Parse{Name: "s", Query: "SELECT 1"}, &pgproto3.Sync{})
	c.read(2)
	// A client that names the gateway's own statement drops it from the
	// connection, so the gateway must prepare it again before the next use.
	name := statementName("SELECT 1", nil)
	want(t, "deallocate", c.query("DEALLOCATE "+name), []string{"CommandComplete DEALLOCATE", "ReadyForQuery I"})
	c.send(&pgproto3.Bind{PreparedStatement: "s"}, &pgproto3.Execute{}, &pgproto3.Sync{})
	want(t, "execute", c.read(4), []string{"BindComplete", "DataRow 1", "CommandComplete SELECT 1", "ReadyForQuery I"})
	c.terminate()
}

// TestSessionPinnedMidTxnDisconnectClosesConn: a pinned session that leaves
// inside a transaction can't be cleaned up. ROLLBACK would leave its session
// state behind, and DISCARD ALL can't run inside a transaction, so the
// connection is closed.
func TestSessionPinnedMidTxnDisconnectClosesConn(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	c.query("SELECT pg_advisory_lock(1)")
	c.query("BEGIN")
	_ = c.conn.Close()
	if err := c.wait(); err != nil {
		t.Fatalf("ServeSession = %v", err)
	}
	waitFor(t, "the pinned connection to close", func() bool { return isClosedPG(h.dialer.server(0)) })
	if s := h.pool.Stats(); s.Open != 0 || s.Idle != 0 {
		t.Fatalf("stats = %+v, want the connection dropped", s)
	}
	for _, q := range h.dialer.server(0).queries() {
		if q == "ROLLBACK" || q == "DISCARD ALL" {
			t.Fatalf("the pool reset the connection with %s instead of closing it", q)
		}
	}
}

func TestSessionSetInTxnPinThenDisconnectMidTxn(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	c.query("BEGIN")
	c.query("SET search_path TO other")
	c.query("COMMIT") // pinned: the SET is now session state
	c.query("BEGIN")
	c.query("SELECT 1")
	_ = c.conn.Close()
	if err := c.wait(); err != nil {
		t.Fatalf("ServeSession = %v", err)
	}
	// The next holder must not inherit search_path = other.
	l := mustAcquire(t, h.pool, nil)
	defer l.Release(LeaseClean)
	if n := h.dialer.dials(); n != 2 {
		t.Fatalf("dials = %d, want a fresh connection for the next client", n)
	}
	if q := h.dialer.server(1).queries(); len(q) != 0 {
		t.Fatalf("fresh connection got %q", q)
	}
}

// TestSessionUnregisteredInternalNameIsMissing: a client can't reach another
// session's cached statement by guessing its lp_ name.
func TestSessionUnregisteredInternalNameIsMissing(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	a := h.connect(nil)
	a.send(&pgproto3.Parse{Name: "s", Query: "SELECT 42"}, &pgproto3.Sync{})
	a.read(2)
	name := statementName("SELECT 42", nil)

	b := h.connect(nil)
	b.send(&pgproto3.Bind{PreparedStatement: name}, &pgproto3.Execute{}, &pgproto3.Sync{})
	if got := b.read(2); !strings.HasPrefix(got[0], "ErrorResponse ERROR 26000") || got[1] != "ReadyForQuery I" {
		t.Fatalf("Bind of another session's statement = %q, want 26000", got)
	}
	b.send(&pgproto3.Describe{ObjectType: 'S', Name: name}, &pgproto3.Sync{})
	if got := b.read(2); !strings.HasPrefix(got[0], "ErrorResponse ERROR 26000") {
		t.Fatalf("Describe of another session's statement = %q, want 26000", got)
	}
	for _, m := range h.dialer.server(0).received() {
		if m == "Bind "+name || m == "Describe S "+name {
			t.Fatalf("backend got %q from a session that never prepared it", m)
		}
	}
	a.terminate()
	b.terminate()
}

// TestSessionAsyncAfterReadyStaysWithItsClient: a notice that arrives right
// behind ReadyForQuery goes to the client it belongs to, not to whichever
// session leases the connection next.
func TestSessionAsyncAfterReadyStaysWithItsClient(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	a := h.connect(nil)
	a.send(&pgproto3.Query{String: "SELECT notice"})
	want(t, "A", a.read(5), []string{"RowDescription", "DataRow notice", "CommandComplete SELECT 1", "ReadyForQuery I", "NoticeResponse"})
	if s := h.pool.Stats(); s.Idle != 1 {
		t.Fatalf("stats = %+v, want the lease released once the notice is through", s)
	}
	b := h.connect(nil)
	want(t, "B", b.query("SELECT 1"), selectOne)
	a.terminate()
	b.terminate()
}

// TestSessionHalfReadConnNeverPooled: the client leaves while the backend
// reader holds part of an asynchronous message. The stop deadline cuts the
// read short, and the connection must be closed: returned to the pool, the
// next holder's reader would finish the message and hand it to another
// client.
func TestSessionHalfReadConnNeverPooled(t *testing.T) {
	h := newHarness(t, 1, time.Second)
	c := h.connect(nil)
	c.send(&pgproto3.Query{String: "SELECT partial"})
	// The partial notice keeps the lease: the reader can't release with bytes
	// buffered, and can't finish reading them.
	want(t, "query", c.read(4), []string{"RowDescription", "DataRow partial", "CommandComplete SELECT 1", "ReadyForQuery I"})
	if s := h.pool.Stats(); s.Idle != 0 {
		t.Fatalf("stats = %+v, want the lease held while a message is half read", s)
	}
	c.terminate()
	waitFor(t, "the half-read connection to close", func() bool { return isClosedPG(h.dialer.server(0)) })
	if s := h.pool.Stats(); s.Open != 0 || s.Idle != 0 {
		t.Fatalf("stats = %+v, want the connection dropped", s)
	}
}

// TestSessionTwoClientsOneConnection is Review Focus 5 across clients: A
// pipelines two transactions without reading while B waits for the only
// connection. B's query reaches the backend only between A's transactions,
// and nobody sees anyone else's rows.
func TestSessionTwoClientsOneConnection(t *testing.T) {
	h := newHarness(t, 1, 2*time.Second)
	a := h.connect(nil)
	b := h.connect(nil)
	a.send(
		&pgproto3.Parse{Query: "SELECT a1"}, &pgproto3.Bind{}, &pgproto3.Execute{}, &pgproto3.Sync{},
		&pgproto3.Parse{Query: "SELECT a2"}, &pgproto3.Bind{}, &pgproto3.Execute{}, &pgproto3.Sync{},
	)
	waitFor(t, "A's first transaction to reach the backend", func() bool { return h.dialer.dials() == 1 && h.dialer.server(0).saw("Sync") })
	b.send(&pgproto3.Query{String: "SELECT b1"})
	waitFor(t, "B to queue for the connection or run", func() bool {
		return h.pool.Stats().Waiting == 1 || h.dialer.server(0).saw("Query SELECT b1")
	})

	want(t, "A", a.read(10), []string{
		"ParseComplete", "BindComplete", "DataRow a1", "CommandComplete SELECT 1", "ReadyForQuery I",
		"ParseComplete", "BindComplete", "DataRow a2", "CommandComplete SELECT 1", "ReadyForQuery I",
	})
	want(t, "B", b.readUntilReady(), []string{"RowDescription", "DataRow b1", "CommandComplete SELECT 1", "ReadyForQuery I"})

	log := h.dialer.server(0).received()
	at := -1
	for i, m := range log {
		if m == "Query SELECT b1" {
			at = i
		}
	}
	if at < 1 || log[at-1] != "Sync" {
		t.Fatalf("backend log %q: B's query must land between A's transactions", log)
	}
	if h.dialer.dials() != 1 {
		t.Fatalf("dials = %d, want everything on the one connection", h.dialer.dials())
	}
	a.terminate()
	b.terminate()
}
