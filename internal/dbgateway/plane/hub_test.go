package plane

import (
	"context"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

// listenPG is a scripted backend for a hub's listener connections. It
// records every simple Query per connection, answers each with
// CommandComplete and ReadyForQuery, tracks which channels each connection
// listens on, and sends a notification when the test asks, but only on a
// channel the connection listens on, as Postgres would. A LISTEN of the
// channel named by refuse fails. An empty query gets EmptyQueryResponse. The
// first deafDials connections, and any marked with setDeaf, record queries
// but never answer, like a half-open connection. A query matching holdQuery
// waits for hold to close before it takes effect and is answered.
type listenPG struct {
	mu        sync.Mutex
	conns     []*listenConn
	dials     int
	dialErr   error
	refuse    string
	deafDials int
	holdQuery string
	hold      chan struct{}
}

type listenConn struct {
	mu        sync.Mutex // serialises writes to be
	be        *pgproto3.Backend
	conn      net.Conn
	pid       uint32
	queries   []string // guarded by listenPG.mu
	listening map[string]bool
	deaf      bool // guarded by listenPG.mu
	closed    chan struct{}
}

func (p *listenPG) dial(context.Context) (*BackendConn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dials++
	if p.dialErr != nil {
		return nil, p.dialErr
	}
	client, server := net.Pipe()
	lc := &listenConn{
		be:        pgproto3.NewBackend(server, server),
		conn:      server,
		pid:       uint32(100 + len(p.conns)),
		listening: map[string]bool{},
		deaf:      len(p.conns) < p.deafDials,
		closed:    make(chan struct{}),
	}
	p.conns = append(p.conns, lc)
	go p.serve(lc)
	return &BackendConn{
		Frontend:   pgproto3.NewFrontend(client, client),
		Conn:       client,
		PID:        lc.pid,
		Params:     map[string]string{},
		Statements: map[string]bool{},
	}, nil
}

func (p *listenPG) serve(lc *listenConn) {
	defer close(lc.closed)
	defer lc.conn.Close()
	for {
		msg, err := lc.be.Receive()
		if err != nil {
			return
		}
		q, ok := msg.(*pgproto3.Query)
		if !ok {
			continue
		}
		p.mu.Lock()
		lc.queries = append(lc.queries, q.String)
		deaf := lc.deaf
		var hold chan struct{}
		if q.String == p.holdQuery {
			hold = p.hold
		}
		p.mu.Unlock()
		if deaf {
			continue
		}
		if hold != nil {
			<-hold
		}
		var reply pgproto3.BackendMessage = &pgproto3.EmptyQueryResponse{}
		if q.String != "" {
			verb, quoted, _ := strings.Cut(q.String, " ")
			channel := strings.ReplaceAll(quoted[1:len(quoted)-1], `""`, `"`)
			p.mu.Lock()
			refused := p.refuse != "" && verb == "LISTEN" && channel == p.refuse
			if !refused {
				lc.listening[channel] = verb == "LISTEN"
			}
			p.mu.Unlock()
			reply = &pgproto3.CommandComplete{CommandTag: []byte(verb)}
			if refused {
				reply = &pgproto3.ErrorResponse{Severity: "ERROR", Code: "55000", Message: "cannot listen"}
			}
		}
		lc.mu.Lock()
		lc.be.Send(reply)
		lc.be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		err = lc.be.Flush()
		lc.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (p *listenPG) conn(i int) *listenConn {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conns[i]
}

func (p *listenPG) dialCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dials
}

// queries is what connection i was asked, in order.
func (p *listenPG) queries(i int) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i >= len(p.conns) {
		return nil
	}
	return slices.Clone(p.conns[i].queries)
}

// notify sends a notification on the newest connection if it listens on
// channel, and reports whether it did.
func (p *listenPG) notify(t *testing.T, channel, payload string) bool {
	t.Helper()
	p.mu.Lock()
	lc := p.conns[len(p.conns)-1]
	on := lc.listening[channel]
	p.mu.Unlock()
	if !on {
		return false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.be.Send(&pgproto3.NotificationResponse{PID: 9999, Channel: channel, Payload: payload})
	if err := lc.be.Flush(); err != nil {
		t.Fatalf("notify: %v", err)
	}
	return true
}

// setDeaf makes connection i stop answering, as if it had gone half-open.
func (p *listenPG) setDeaf(i int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.conns[i].deaf = true
}

// holdReply makes the next query equal to q wait until the returned channel
// is closed. The test must close it.
func (p *listenPG) holdReply(t *testing.T, q string) chan struct{} {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.holdQuery, p.hold = q, make(chan struct{})
	return p.hold
}

func (p *listenPG) setRefuse(channel string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refuse = channel
}

func newTestHub(t *testing.T, p *listenPG) *Hub {
	t.Helper()
	h := NewHub(p.dial)
	t.Cleanup(h.Close)
	return h
}

func waitListening(t *testing.T, h *Hub, channel string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := h.Listening(ctx, channel); err != nil {
		t.Fatalf("Listening(%q) = %v", channel, err)
	}
}

func recvNote(t *testing.T, s *Subscriber) *pgproto3.NotificationResponse {
	t.Helper()
	select {
	case n := <-s.C:
		return n
	case <-time.After(2 * time.Second):
		t.Fatal("no notification arrived")
		return nil
	}
}

func TestHubSingleListenerFanOut(t *testing.T) {
	p := &listenPG{}
	h := newTestHub(t, p)
	a, b := NewSubscriber(), NewSubscriber()
	h.Subscribe("jobs", a)
	h.Subscribe("jobs", b)
	waitListening(t, h, "jobs")
	want(t, "listener queries", p.queries(0), []string{`LISTEN "jobs"`})
	if !p.notify(t, "jobs", "42") {
		t.Fatal("the listener is not listening on jobs")
	}
	for _, s := range []*Subscriber{a, b} {
		n := recvNote(t, s)
		if n.Channel != "jobs" || n.Payload != "42" || n.PID != 100 {
			t.Fatalf("notification = %+v, want jobs/42 from the listener's PID 100", *n)
		}
	}
	if n := p.dialCount(); n != 1 {
		t.Fatalf("dials = %d, want one listener connection", n)
	}
}

func TestHubQuotesChannelNames(t *testing.T) {
	p := &listenPG{}
	h := newTestHub(t, p)
	s := NewSubscriber()
	h.Subscribe(`Mixed "Case"`, s)
	waitListening(t, h, `Mixed "Case"`)
	want(t, "listener queries", p.queries(0), []string{`LISTEN "Mixed ""Case"""`})
	p.notify(t, `Mixed "Case"`, "x")
	if n := recvNote(t, s); n.Channel != `Mixed "Case"` {
		t.Fatalf("channel = %q", n.Channel)
	}
}

func TestChannelKeyTruncatesLikePostgres(t *testing.T) {
	short := strings.Repeat("a", 63)
	if got := channelKey(short); got != short {
		t.Fatalf("63 bytes cut to %q", got)
	}
	if got := channelKey(short + "b"); got != short {
		t.Fatalf("64 bytes kept %d", len(got))
	}
	// é is two bytes; the cut must not split it.
	multi := strings.Repeat("a", 62) + "é"
	if got := channelKey(multi); got != strings.Repeat("a", 62) {
		t.Fatalf("multibyte cut = %q", got)
	}
}

func TestHubRefcount(t *testing.T) {
	p := &listenPG{}
	h := newTestHub(t, p)
	a, b := NewSubscriber(), NewSubscriber()
	h.Subscribe("jobs", a)
	h.Subscribe("jobs", b)
	waitListening(t, h, "jobs")

	h.Unsubscribe("jobs", a)
	if !p.notify(t, "jobs", "still") {
		t.Fatal("the listener stopped listening while b still subscribes")
	}
	if n := recvNote(t, b); n.Payload != "still" {
		t.Fatalf("payload = %q", n.Payload)
	}
	h.Unsubscribe("jobs", b)
	waitFor(t, "UNLISTEN jobs", func() bool { return len(p.queries(0)) == 2 })
	want(t, "listener queries", p.queries(0), []string{`LISTEN "jobs"`, `UNLISTEN "jobs"`})
	if len(a.C) != 0 {
		t.Fatal("an unsubscribed subscriber got a notification")
	}
}

func TestHubUnsubscribeAll(t *testing.T) {
	p := &listenPG{}
	h := newTestHub(t, p)
	a, b := NewSubscriber(), NewSubscriber()
	h.Subscribe("one", a)
	h.Subscribe("two", a)
	h.Subscribe("two", b)
	waitListening(t, h, "one")
	waitListening(t, h, "two")
	h.UnsubscribeAll(a)
	waitFor(t, "UNLISTEN one", func() bool { return slices.Contains(p.queries(0), `UNLISTEN "one"`) })
	p.notify(t, "two", "b only")
	if n := recvNote(t, b); n.Payload != "b only" {
		t.Fatalf("payload = %q", n.Payload)
	}
	if slices.Contains(p.queries(0), `UNLISTEN "two"`) {
		t.Fatal("two was dropped while b still subscribes")
	}
	if len(a.C) != 0 {
		t.Fatal("a got a notification after UnsubscribeAll")
	}
}

func TestHubRedialRelistens(t *testing.T) {
	p := &listenPG{}
	h := newTestHub(t, p)
	a, b := NewSubscriber(), NewSubscriber()
	h.Subscribe("jobs", a)
	h.Subscribe("Other", b)
	waitListening(t, h, "jobs")
	waitListening(t, h, "Other")

	start := time.Now()
	_ = p.conn(0).conn.Close()
	waitFor(t, "the redial", func() bool { return len(p.queries(1)) == 2 })
	if d := time.Since(start); d < 90*time.Millisecond {
		t.Fatalf("redialled after %v, want at least the 100ms backoff", d)
	}
	got := p.queries(1)
	slices.Sort(got)
	want(t, "second connection's queries", got, []string{`LISTEN "Other"`, `LISTEN "jobs"`})
	waitListening(t, h, "jobs")
	p.notify(t, "jobs", "after")
	if n := recvNote(t, a); n.Payload != "after" || n.PID != 101 {
		t.Fatalf("notification = %+v, want the new listener's PID 101", *n)
	}
}

func TestHubFullSubscriberDrops(t *testing.T) {
	p := &listenPG{}
	h := newTestHub(t, p)
	slow, fast := NewSubscriber(), NewSubscriber()
	h.Subscribe("jobs", slow)
	h.Subscribe("jobs", fast)
	waitListening(t, h, "jobs")
	const sent = subscriberBuffer + 6
	for i := range sent {
		p.notify(t, "jobs", strconv.Itoa(i))
		if n := recvNote(t, fast); n.Payload != strconv.Itoa(i) {
			t.Fatalf("fast got %q, want %d", n.Payload, i)
		}
	}
	if len(slow.C) != subscriberBuffer {
		t.Fatalf("slow holds %d, want a full buffer of %d", len(slow.C), subscriberBuffer)
	}
	if d := slow.Drops(); d != sent-subscriberBuffer {
		t.Fatalf("slow drops = %d, want %d", d, sent-subscriberBuffer)
	}
	if d := fast.Drops(); d != 0 {
		t.Fatalf("fast drops = %d, want 0", d)
	}
	if first := <-slow.C; first.Payload != "0" {
		t.Fatalf("slow kept %q first, want the oldest", first.Payload)
	}
}

func TestHubCloseStopsRedial(t *testing.T) {
	p := &listenPG{dialErr: errors.New("backend down")}
	h := NewHub(p.dial)
	s := NewSubscriber()
	h.Subscribe("jobs", s)
	waitFor(t, "a redial", func() bool { return p.dialCount() >= 2 })

	closed := make(chan struct{})
	go func() { h.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not return")
	}
	n := p.dialCount()
	time.Sleep(150 * time.Millisecond)
	if got := p.dialCount(); got != n {
		t.Fatalf("dials went from %d to %d after Close", n, got)
	}
	if err := h.Listening(t.Context(), "jobs"); !errors.Is(err, ErrHubClosed) {
		t.Fatalf("Listening after Close = %v, want ErrHubClosed", err)
	}
	h.Close() // a second Close is harmless
}

func TestHubCloseClosesListener(t *testing.T) {
	p := &listenPG{}
	h := NewHub(p.dial)
	h.Subscribe("jobs", NewSubscriber())
	waitListening(t, h, "jobs")
	h.Close()
	select {
	case <-p.conn(0).closed:
	case <-time.After(time.Second):
		t.Fatal("the listener connection is still open after Close")
	}
	if n := p.dialCount(); n != 1 {
		t.Fatalf("dials = %d after Close, want 1", n)
	}
}

func TestHubListenRefused(t *testing.T) {
	p := &listenPG{}
	p.setRefuse("bad")
	h := newTestHub(t, p)
	s := NewSubscriber()
	h.Subscribe("bad", s)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err := h.Listening(ctx, "bad")
	var pgErr interface{ SQLState() string }
	if !errors.As(err, &pgErr) || pgErr.SQLState() != "55000" {
		t.Fatalf("Listening = %v, want the backend's 55000", err)
	}
	// The refused channel isn't retried, so the hub carries on with others.
	h.Subscribe("good", s)
	waitListening(t, h, "good")
	h.Unsubscribe("bad", s)
}

// TestHubResubscribeDuringUnlisten subscribes again while the listener's
// UNLISTEN of the channel is still in flight. Listening must wait for the
// LISTEN that follows, or the client would be told it listens while the
// backend is dropping the channel.
func TestHubResubscribeDuringUnlisten(t *testing.T) {
	p := &listenPG{}
	h := newTestHub(t, p)
	a := NewSubscriber()
	h.Subscribe("jobs", a)
	waitListening(t, h, "jobs")

	release := p.holdReply(t, `UNLISTEN "jobs"`)
	h.Unsubscribe("jobs", a)
	waitFor(t, "the UNLISTEN to reach the backend", func() bool { return len(p.queries(0)) == 2 })
	b := NewSubscriber()
	h.Subscribe("jobs", b)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	got := make(chan error, 1)
	go func() { got <- h.Listening(ctx, "jobs") }()
	select {
	case err := <-got:
		close(release)
		t.Fatalf("Listening returned %v with the UNLISTEN still in flight", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-got; err != nil {
		t.Fatalf("Listening = %v", err)
	}
	// Listening returned only once the backend acknowledged the LISTEN.
	want(t, "listener queries", p.queries(0), []string{`LISTEN "jobs"`, `UNLISTEN "jobs"`, `LISTEN "jobs"`})
	if !p.notify(t, "jobs", "back") {
		t.Fatal("the backend is not listening on jobs")
	}
	if n := recvNote(t, b); n.Payload != "back" {
		t.Fatalf("payload = %q", n.Payload)
	}
}

func TestHubRoundTripTimeoutRedials(t *testing.T) {
	p := &listenPG{deafDials: 1}
	h := NewHub(p.dial)
	h.roundTrip = 50 * time.Millisecond
	t.Cleanup(h.Close)
	h.Subscribe("jobs", NewSubscriber())
	waitListening(t, h, "jobs")
	if n := p.dialCount(); n != 2 {
		t.Fatalf("dials = %d, want a redial after the first listener never answered", n)
	}
	want(t, "first connection", p.queries(0), []string{`LISTEN "jobs"`})
	want(t, "second connection", p.queries(1), []string{`LISTEN "jobs"`})
	waitFor(t, "the silent connection to close", func() bool {
		select {
		case <-p.conn(0).closed:
			return true
		default:
			return false
		}
	})
}

func TestHubProbeReplacesDeadListener(t *testing.T) {
	p := &listenPG{}
	h := NewHub(p.dial)
	h.roundTrip = 50 * time.Millisecond
	h.probeEvery = 30 * time.Millisecond
	t.Cleanup(h.Close)
	s := NewSubscriber()
	h.Subscribe("jobs", s)
	waitListening(t, h, "jobs")
	p.setDeaf(0)
	waitFor(t, "a new listener", func() bool { return slices.Contains(p.queries(1), `LISTEN "jobs"`) })
	if !slices.Contains(p.queries(0), "") {
		t.Fatalf("first connection's queries = %q, want an empty probe", p.queries(0))
	}
	waitListening(t, h, "jobs")
	p.notify(t, "jobs", "alive")
	if n := recvNote(t, s); n.Payload != "alive" || n.PID != 101 {
		t.Fatalf("notification = %+v", *n)
	}
}

func TestHubProbeKeepsLiveListener(t *testing.T) {
	p := &listenPG{}
	h := NewHub(p.dial)
	h.probeEvery = 20 * time.Millisecond
	t.Cleanup(h.Close)
	h.Subscribe("jobs", NewSubscriber())
	waitListening(t, h, "jobs")
	waitFor(t, "two probes", func() bool {
		n := 0
		for _, q := range p.queries(0) {
			if q == "" {
				n++
			}
		}
		return n >= 2
	})
	if n := p.dialCount(); n != 1 {
		t.Fatalf("dials = %d, want the answering listener kept", n)
	}
}

// newHubHarness is a session harness whose route has a hub.
func newHubHarness(t *testing.T) (*harness, *listenPG, *Hub) {
	t.Helper()
	h := newHarness(t, 1, time.Second)
	p := &listenPG{}
	hub := newTestHub(t, p)
	h.route.hub = hub
	return h, p, hub
}

func TestSessionListenUsesHub(t *testing.T) {
	h, p, _ := newHubHarness(t)
	c := h.connect(nil)
	want(t, "LISTEN", c.query("LISTEN Jobs"), []string{"CommandComplete LISTEN", "ReadyForQuery I"})
	want(t, "LISTEN quoted", c.query(`LISTEN "Mixed"`), []string{"CommandComplete LISTEN", "ReadyForQuery I"})
	want(t, "LISTEN again", c.query("LISTEN jobs"), []string{"CommandComplete LISTEN", "ReadyForQuery I"})
	if n := h.dialer.dials(); n != 0 {
		t.Fatalf("pool dials = %d, want LISTEN answered without a lease", n)
	}
	want(t, "listener queries", p.queries(0), []string{`LISTEN "jobs"`, `LISTEN "Mixed"`})

	p.notify(t, "jobs", "hello")
	want(t, "notification", c.read(1), []string{"NotificationResponse jobs hello 100"})

	want(t, "UNLISTEN", c.query("UNLISTEN jobs"), []string{"CommandComplete UNLISTEN", "ReadyForQuery I"})
	waitFor(t, "UNLISTEN jobs", func() bool { return slices.Contains(p.queries(0), `UNLISTEN "jobs"`) })
	want(t, "SELECT", c.query("SELECT 1"), selectOne)
	c.terminate()
	waitFor(t, "UNLISTEN Mixed at session end", func() bool { return slices.Contains(p.queries(0), `UNLISTEN "Mixed"`) })
}

func TestSessionDeliversNotifyWhenIdle(t *testing.T) {
	h, p, hub := newHubHarness(t)
	h.dialer.hold = make(chan struct{})
	c := h.connect(nil)
	c.query("LISTEN jobs")
	watch := NewSubscriber()
	hub.Subscribe("jobs", watch)
	waitListening(t, hub, "jobs")

	// Mid-result: the backend has sent a row and holds the rest back.
	c.send(&pgproto3.Query{String: "SELECT hold"})
	want(t, "first rows", c.read(2), []string{"RowDescription", "DataRow hold"})
	p.notify(t, "jobs", "mid")
	recvNote(t, watch)
	time.Sleep(20 * time.Millisecond) // a session that forwards at once would do it now
	close(h.dialer.hold)
	want(t, "rest", c.read(3), []string{"CommandComplete SELECT 1", "ReadyForQuery I", "NotificationResponse jobs mid 100"})

	// Inside a transaction: held until the transaction ends.
	c.query("BEGIN")
	p.notify(t, "jobs", "txn")
	recvNote(t, watch)
	time.Sleep(20 * time.Millisecond)
	want(t, "in txn", c.query("SELECT 1"), []string{"RowDescription", "DataRow 1", "CommandComplete SELECT 1", "ReadyForQuery T"})
	want(t, "COMMIT", c.query("COMMIT"), []string{"CommandComplete COMMIT", "ReadyForQuery I"})
	want(t, "after COMMIT", c.read(1), []string{"NotificationResponse jobs txn 100"})
	c.terminate()
}

func TestSessionListenInTransactionPins(t *testing.T) {
	h, p, _ := newHubHarness(t)
	c := h.connect(nil)
	c.query("BEGIN")
	want(t, "LISTEN", c.query("LISTEN jobs"), []string{"CommandComplete LISTEN", "ReadyForQuery T"})
	c.query("COMMIT")
	if !slices.Contains(h.dialer.server(0).queries(), "LISTEN jobs") {
		t.Fatalf("backend queries = %q, want the LISTEN on the client's own connection", h.dialer.server(0).queries())
	}
	if n := p.dialCount(); n != 0 {
		t.Fatalf("listener dials = %d, want the hub unused", n)
	}
	if s := h.pool.Stats(); s.Idle != 0 || s.Open != 1 {
		t.Fatalf("stats = %+v, want the lease pinned", s)
	}
	// Once pinned, LISTEN keeps going to the session's own connection.
	c.query("LISTEN more")
	if !slices.Contains(h.dialer.server(0).queries(), "LISTEN more") || p.dialCount() != 0 {
		t.Fatal("a pinned session's LISTEN did not reach its own connection")
	}
	c.terminate()
}

func TestSessionExtendedListenPins(t *testing.T) {
	h, p, _ := newHubHarness(t)
	c := h.connect(nil)
	c.send(&pgproto3.Parse{Query: "LISTEN jobs"}, &pgproto3.Bind{}, &pgproto3.Execute{}, &pgproto3.Sync{})
	want(t, "extended LISTEN", c.read(4), []string{"ParseComplete", "BindComplete", "CommandComplete LISTEN", "ReadyForQuery I"})
	if p.dialCount() != 0 {
		t.Fatal("an extended-protocol LISTEN used the hub")
	}
	if s := h.pool.Stats(); s.Open != 1 || s.Idle != 0 {
		t.Fatalf("stats = %+v, want the lease pinned", s)
	}
	c.terminate()
}

func TestSessionUnlistenAll(t *testing.T) {
	h, p, _ := newHubHarness(t)
	c := h.connect(nil)
	c.query("LISTEN a")
	c.query(`LISTEN "B"`)
	want(t, "UNLISTEN *", c.query("UNLISTEN *"), []string{"CommandComplete UNLISTEN", "ReadyForQuery I"})
	waitFor(t, "both channels dropped", func() bool {
		q := p.queries(0)
		return slices.Contains(q, `UNLISTEN "a"`) && slices.Contains(q, `UNLISTEN "B"`)
	})
	// The session no longer counts as subscribed, so LISTEN goes back to the
	// listener.
	c.query("LISTEN a")
	waitFor(t, "LISTEN a again", func() bool {
		q := p.queries(0)
		return q[len(q)-1] == `LISTEN "a"`
	})
	c.terminate()
}

func TestSessionDiscardAllUnlistens(t *testing.T) {
	h, p, _ := newHubHarness(t)
	c := h.connect(nil)
	c.query("LISTEN jobs")
	want(t, "DISCARD ALL", c.query("DISCARD ALL"), []string{"CommandComplete DISCARD ALL", "ReadyForQuery I"})
	waitFor(t, "UNLISTEN jobs", func() bool { return slices.Contains(p.queries(0), `UNLISTEN "jobs"`) })
	c.terminate()
}

func TestSessionListenRefused(t *testing.T) {
	h, p, _ := newHubHarness(t)
	p.setRefuse("bad")
	c := h.connect(nil)
	want(t, "LISTEN bad", c.query("LISTEN bad"), []string{"ErrorResponse ERROR 55000 cannot listen", "ReadyForQuery I"})
	want(t, "SELECT", c.query("SELECT 1"), selectOne)
	want(t, "LISTEN good", c.query("LISTEN good"), []string{"CommandComplete LISTEN", "ReadyForQuery I"})
	c.terminate()
}

func TestSessionUnlistenQuotedStarIsAChannel(t *testing.T) {
	h, p, _ := newHubHarness(t)
	c := h.connect(nil)
	c.query(`LISTEN "*"`)
	c.query("LISTEN a")
	want(t, "listener queries", p.queries(0), []string{`LISTEN "*"`, `LISTEN "a"`})
	want(t, `UNLISTEN "*"`, c.query(`UNLISTEN "*"`), []string{"CommandComplete UNLISTEN", "ReadyForQuery I"})
	waitFor(t, `UNLISTEN "*"`, func() bool { return slices.Contains(p.queries(0), `UNLISTEN "*"`) })
	// The client still listens on a.
	if !p.notify(t, "a", "kept") {
		t.Fatal("the listener dropped a")
	}
	want(t, "notification", c.read(1), []string{"NotificationResponse a kept 100"})
	c.query("UNLISTEN *")
	waitFor(t, "UNLISTEN a", func() bool { return slices.Contains(p.queries(0), `UNLISTEN "a"`) })
	c.terminate()
}

// TestSessionHoldsNotifyInExtendedBatch: with no lease held, a notification
// that arrives between extended-protocol messages waits for the Sync's
// ReadyForQuery.
func TestSessionHoldsNotifyInExtendedBatch(t *testing.T) {
	h, p, hub := newHubHarness(t)
	c := h.connect(nil)
	c.query("LISTEN jobs")
	watch := NewSubscriber()
	hub.Subscribe("jobs", watch)
	waitListening(t, hub, "jobs")

	// A Close of a named statement is answered without a lease.
	c.send(&pgproto3.Close{ObjectType: 'S', Name: "nope"}, &pgproto3.Flush{})
	want(t, "Close", c.read(1), []string{"CloseComplete"})
	p.notify(t, "jobs", "batch")
	recvNote(t, watch)
	time.Sleep(20 * time.Millisecond) // a session that forwards at once would do it now
	c.send(&pgproto3.Sync{})
	want(t, "Sync", c.read(2), []string{"ReadyForQuery I", "NotificationResponse jobs batch 100"})
	if n := h.dialer.dials(); n != 0 {
		t.Fatalf("pool dials = %d, want none", n)
	}
	c.terminate()
}

// TestSessionHoldsNotifyDuringHubListen: a notification that arrives while
// the hub answers a LISTEN goes out after that LISTEN's reply.
func TestSessionHoldsNotifyDuringHubListen(t *testing.T) {
	h, p, hub := newHubHarness(t)
	c := h.connect(nil)
	c.query("LISTEN a")
	watch := NewSubscriber()
	hub.Subscribe("a", watch)
	waitListening(t, hub, "a")

	release := p.holdReply(t, `LISTEN "b"`)
	c.send(&pgproto3.Query{String: "LISTEN b"})
	waitFor(t, "LISTEN b at the listener", func() bool { return slices.Contains(p.queries(0), `LISTEN "b"`) })
	p.notify(t, "a", "during")
	recvNote(t, watch)
	time.Sleep(20 * time.Millisecond)
	close(release)
	want(t, "LISTEN b", c.read(3), []string{"CommandComplete LISTEN", "ReadyForQuery I", "NotificationResponse a during 100"})
	c.terminate()
}
