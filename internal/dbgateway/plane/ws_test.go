package plane

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgproto3"
)

// wsRig is a router with one route behind the WebSocket handler on a test
// server.
type wsRig struct {
	t      *testing.T
	h      *routerHarness
	hd     *wsHandler
	srv    *httptest.Server
	cancel context.CancelFunc
}

const wsTestAddress = "orders." + string(testSuffix) + ":5432"

func newWSRig(t *testing.T) *wsRig {
	t.Helper()
	h := newRouterHarness(t, nil, nil)
	h.apply(h.route("r1", "orders"))
	base, cancel := context.WithCancel(context.Background())
	hd := WebSocketHandlerContext(base, h.r).(*wsHandler)
	mux := http.NewServeMux()
	mux.Handle("/v2", hd)
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = smallBuffers{srv.Listener}
	srv.Start()
	t.Cleanup(func() {
		cancel()
		srv.Close()
	})
	return &wsRig{t: t, h: h, hd: hd, srv: srv, cancel: cancel}
}

// smallBuffers makes the server's sockets hold little, so a client that
// stops reading backs a write up after kilobytes rather than megabytes.
type smallBuffers struct{ net.Listener }

func (l smallBuffers) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.SetWriteBuffer(4096)
			_ = tc.SetReadBuffer(4096)
		}
	}
	return c, err
}

// smallDialer is a dialer whose sockets hold little, for the same reason.
var smallDialer = &websocket.Dialer{
	NetDial: func(network, addr string) (net.Conn, error) {
		c, err := net.Dial(network, addr)
		if err == nil {
			if tc, ok := c.(*net.TCPConn); ok {
				_ = tc.SetReadBuffer(4096)
				_ = tc.SetWriteBuffer(4096)
			}
		}
		return c, err
	},
}

func (g *wsRig) url(query string) string {
	return "ws" + strings.TrimPrefix(g.srv.URL, "http") + "/v2?" + query
}

// dial opens a WebSocket for the route address.
func (g *wsRig) dial(address string) *wsClient {
	g.t.Helper()
	q := url.Values{"address": {address}}
	return g.dialQuery(q.Encode())
}

func (g *wsRig) dialQuery(query string) *wsClient {
	g.t.Helper()
	c, resp, err := smallDialer.Dial(g.url(query), nil)
	if err != nil {
		g.t.Fatalf("dial: %v (response %v)", err, resp)
	}
	g.t.Cleanup(func() { _ = c.Close() })
	wc := &wsClient{t: g.t, c: c}
	wc.fe = pgproto3.NewFrontend(wc, io.Discard)
	return wc
}

// wsClient is the client end of a WebSocket, written without wsConn so a bug
// there doesn't hide behind itself. It reads Postgres messages out of the
// binary messages it receives and writes whatever frames a test hands it.
type wsClient struct {
	t   *testing.T
	c   *websocket.Conn
	fe  *pgproto3.Frontend
	buf bytes.Buffer
}

func (c *wsClient) Read(p []byte) (int, error) {
	for c.buf.Len() == 0 {
		kind, data, err := c.c.ReadMessage()
		if err != nil {
			return 0, err
		}
		if kind != websocket.BinaryMessage {
			return 0, errors.New("server sent a text message")
		}
		c.buf.Write(data)
	}
	return c.buf.Read(p)
}

// frame sends each argument as one binary message.
func (c *wsClient) frame(frames ...[]byte) {
	c.t.Helper()
	for _, f := range frames {
		if err := c.c.WriteMessage(websocket.BinaryMessage, f); err != nil {
			c.t.Fatalf("send frame: %v", err)
		}
	}
}

func (c *wsClient) recv() string {
	c.t.Helper()
	_ = c.c.SetReadDeadline(time.Now().Add(3 * time.Second))
	msg, err := c.fe.Receive()
	if err != nil {
		c.t.Fatalf("client receive: %v", err)
	}
	if e, ok := msg.(*pgproto3.ErrorResponse); ok {
		return showBackend(e) + " " + e.Message
	}
	return showBackend(msg)
}

func (c *wsClient) readUntilReady() []string {
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

// expectClosed waits for the server to end the connection and returns the
// error that told us.
func (c *wsClient) expectClosed() error {
	c.t.Helper()
	_ = c.c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, _, err := c.c.ReadMessage()
		if err == nil {
			continue
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			c.t.Fatal("the server did not close the connection")
		}
		return err
	}
}

func startupBytes(t *testing.T, params map[string]string) []byte {
	t.Helper()
	b, err := (&pgproto3.StartupMessage{ProtocolVersion: pgproto3.ProtocolVersionNumber, Parameters: params}).Encode(nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func frontendBytes(t *testing.T, msgs ...pgproto3.FrontendMessage) []byte {
	t.Helper()
	var out []byte
	for _, m := range msgs {
		var err error
		if out, err = m.Encode(out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

var appStartup = map[string]string{"user": testRole, "database": "orders"}

// login sends the startup packet, waits for the cleartext request, answers
// it, and reads the greeting up to ReadyForQuery.
func (c *wsClient) login(g *wsRig) {
	c.t.Helper()
	c.frame(startupBytes(c.t, appStartup))
	if got := c.recv(); got != "AuthenticationCleartextPassword" {
		c.t.Fatalf("first message = %q, want AuthenticationCleartextPassword", got)
	}
	c.frame(frontendBytes(c.t, &pgproto3.PasswordMessage{Password: testPass}))
	if got := c.recv(); got != "AuthenticationOk" {
		c.t.Fatalf("after the password = %q, want AuthenticationOk", got)
	}
	c.readUntilReady()
}

func TestWSReplaysNeonOpen(t *testing.T) {
	raw, err := os.ReadFile("testdata/neon/ws_open.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Path   string              `json:"path"`
		Query  map[string][]string `json:"query"`
		Frames []string            `json:"frames_base64"`
	}
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	if capture.Path != "/v2" || len(capture.Frames) == 0 {
		t.Fatalf("fixture = %+v", capture)
	}

	g := newWSRig(t)
	// The captured connection logs in as app with the password "pw".
	rt := g.h.route("r1", "orders")
	rt.Verifiers = []Verifier{NewVerifier(testRole, "pw", testRouteSalt, 4096)}
	g.h.apply(rt)

	c := g.dialQuery(url.Values(capture.Query).Encode())
	for _, f := range capture.Frames {
		b, err := base64.StdEncoding.DecodeString(f)
		if err != nil {
			t.Fatal(err)
		}
		c.frame(b)
	}

	// The driver sent its password and query without waiting, and the
	// gateway still asks the usual way and then answers both.
	if got := c.recv(); got != "AuthenticationCleartextPassword" {
		t.Fatalf("first message = %q, want AuthenticationCleartextPassword", got)
	}
	if got := c.recv(); got != "AuthenticationOk" {
		t.Fatalf("second message = %q, want AuthenticationOk", got)
	}
	greeting := c.readUntilReady()
	if len(greeting) < 2 || greeting[len(greeting)-2] != "BackendKeyData" || greeting[len(greeting)-1] != "ReadyForQuery I" {
		t.Fatalf("greeting = %q, want it to end BackendKeyData, ReadyForQuery I", greeting)
	}
	want(t, "reply to the pipelined query", c.readUntilReady(), selectOne)
	// The backend saw the driver's query once, after the gateway's own probe
	// and the startup parameters it replays.
	if got := g.h.dialer.server(0).queries(); len(got) == 0 || got[len(got)-1] != "SELECT 1" {
		t.Fatalf("backend queries = %q, want them to end with SELECT 1", got)
	}
}

func TestWSFrameSplitting(t *testing.T) {
	g := newWSRig(t)
	c := g.dial(wsTestAddress)
	c.login(g)

	// One Query cut across three frames: inside its 5-byte header, then
	// inside its text.
	q := frontendBytes(t, &pgproto3.Query{String: "SELECT 1"})
	c.frame(q[:3], q[3:9], q[9:])
	want(t, "split query", c.readUntilReady(), selectOne)

	// Two messages in one frame are both handled.
	c.frame(frontendBytes(t, &pgproto3.Query{String: "SELECT 1"}, &pgproto3.Query{String: "SELECT 1"}))
	want(t, "first of two", c.readUntilReady(), selectOne)
	want(t, "second of two", c.readUntilReady(), selectOne)

	// A frame that ends mid-message and carries the start of the next.
	two := frontendBytes(t, &pgproto3.Query{String: "SELECT 1"}, &pgproto3.Query{String: "SELECT 1"})
	cut := len(two)/2 + 2
	c.frame(two[:cut], nil, two[cut:])
	want(t, "straddling first", c.readUntilReady(), selectOne)
	want(t, "straddling second", c.readUntilReady(), selectOne)

	c.frame(frontendBytes(t, &pgproto3.Terminate{}))
	c.expectClosed()
}

func TestWSAddressRequired(t *testing.T) {
	g := newWSRig(t)
	for _, query := range []string{"", "address=", "other=orders.example.com", "address=:5432"} {
		_, resp, err := websocket.DefaultDialer.Dial(g.url(query), nil)
		if !errors.Is(err, websocket.ErrBadHandshake) || resp == nil || resp.StatusCode != http.StatusBadRequest {
			t.Errorf("query %q: err = %v, response %v, want a 400 before the upgrade", query, err, resp)
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
	}
}

func TestWSRouteHost(t *testing.T) {
	for _, tc := range []struct {
		address string
		host    string
		ok      bool
	}{
		{"orders.example.com:5432", "orders.example.com", true},
		{"orders.example.com", "orders.example.com", true},
		{"[::1]:5432", "::1", true},
		{"[::1]", "::1", true},
		{"", "", false},
		{":5432", "", false},
	} {
		host, ok := wsRouteHost(tc.address)
		if host != tc.host || ok != tc.ok {
			t.Errorf("wsRouteHost(%q) = %q, %v, want %q, %v", tc.address, host, ok, tc.host, tc.ok)
		}
	}
}

func TestWSDraining(t *testing.T) {
	g := newWSRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	g.h.r.Drain(ctx)

	q := url.Values{"address": {wsTestAddress}}
	_, resp, err := websocket.DefaultDialer.Dial(g.url(q.Encode()), nil)
	if !errors.Is(err, websocket.ErrBadHandshake) || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("upgrade while draining: err = %v, response %v, want 503", err, resp)
	}
	_ = resp.Body.Close()
}

func TestWSStartupTimeout(t *testing.T) {
	g := newWSRig(t)
	g.hd.startupTimeout = 200 * time.Millisecond
	c := g.dial(wsTestAddress)

	start := time.Now()
	if got := c.recv(); got != "ErrorResponse FATAL 08P01 the database gateway got no startup message in time" {
		t.Fatalf("message = %q, want FATAL 08P01", got)
	}
	c.expectClosed()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v to give up on a silent client", d)
	}
}

func TestWSStartupTimeoutIsNotExtendedByPongs(t *testing.T) {
	g := newWSRig(t)
	g.hd.startupTimeout = 300 * time.Millisecond
	g.hd.pingInterval = 20 * time.Millisecond
	g.hd.readWait = 10 * time.Second
	c := g.dial(wsTestAddress)
	// The client answers every ping, and still runs out of startup time.
	if got := c.recv(); !strings.HasPrefix(got, "ErrorResponse FATAL 08P01") {
		t.Fatalf("message = %q, want FATAL 08P01", got)
	}
}

func TestWSSSLRequestGetsN(t *testing.T) {
	for name, req := range map[string]pgproto3.FrontendMessage{
		"SSLRequest":    &pgproto3.SSLRequest{},
		"GSSEncRequest": &pgproto3.GSSEncRequest{},
	} {
		t.Run(name, func(t *testing.T) {
			g := newWSRig(t)
			c := g.dial(wsTestAddress)
			c.frame(frontendBytes(t, req))
			_ = c.c.SetReadDeadline(time.Now().Add(3 * time.Second))
			kind, data, err := c.c.ReadMessage()
			if err != nil || kind != websocket.BinaryMessage || string(data) != "N" {
				t.Fatalf("reply = %v %q %v, want a binary message holding N", kind, data, err)
			}
			// The client carries on in the clear with the real startup packet.
			c.login(g)
			c.frame(frontendBytes(t, &pgproto3.Query{String: "SELECT 1"}))
			want(t, "query after the refusal", c.readUntilReady(), selectOne)
		})
	}
}

func TestWSCancelRequestCloses(t *testing.T) {
	g := newWSRig(t)
	c := g.dial(wsTestAddress)
	c.frame(frontendBytes(t, &pgproto3.CancelRequest{ProcessID: 1, SecretKey: []byte{0, 0, 0, 2}}))
	c.expectClosed()
	if n := g.h.dialer.dials(); n != 0 {
		t.Fatalf("a CancelRequest dialed %d backends", n)
	}
}

func TestWSTextMessageCloses(t *testing.T) {
	g := newWSRig(t)
	c := g.dial(wsTestAddress)
	c.login(g)

	if err := c.c.WriteMessage(websocket.TextMessage, []byte("SELECT 1")); err != nil {
		t.Fatal(err)
	}
	err := c.expectClosed()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.CloseUnsupportedData {
		t.Fatalf("closed with %v, want close code %d", err, websocket.CloseUnsupportedData)
	}
}

func TestWSPingKeepsLiveClientOpen(t *testing.T) {
	g := newWSRig(t)
	g.hd.pingInterval = 50 * time.Millisecond
	g.hd.readWait = 300 * time.Millisecond
	c := g.dial(wsTestAddress)
	c.login(g)

	// Idle for well past the read timeout. The client is reading, so it
	// answers every ping, and the connection stays up.
	time.AfterFunc(900*time.Millisecond, func() {
		_ = c.c.WriteMessage(websocket.BinaryMessage, frontendBytes(t, &pgproto3.Query{String: "SELECT 1"}))
	})
	want(t, "query after idling", c.readUntilReady(), selectOne)
}

func TestWSDeadClientTimesOut(t *testing.T) {
	g := newWSRig(t)
	g.hd.pingInterval = 50 * time.Millisecond
	g.hd.readWait = 300 * time.Millisecond
	c := g.dial(wsTestAddress)

	// Log in and then stop reading: the pings go unanswered.
	c.frame(startupBytes(t, appStartup))
	c.frame(frontendBytes(t, &pgproto3.PasswordMessage{Password: testPass}))
	time.Sleep(900 * time.Millisecond)

	// What the server wrote before it gave up is still in our buffer. The
	// stream ends in a close, not in a hang.
	if err := c.expectClosed(); err == nil {
		t.Fatal("expectClosed returned no error")
	}
	g.h.r.mu.Lock()
	live := len(g.h.r.sessions)
	g.h.r.mu.Unlock()
	if live != 0 {
		t.Fatalf("%d sessions still live after the read timeout", live)
	}
}

func TestWSBaseContextClosesConnections(t *testing.T) {
	g := newWSRig(t)
	c := g.dial(wsTestAddress)
	c.login(g)
	g.cancel()
	c.expectClosed()
}

// wsPair returns the server end of a WebSocket as a wsConn and the client end
// as a plain gorilla connection.
func wsPair(t *testing.T) (*wsConn, *websocket.Conn) {
	t.Helper()
	got := make(chan *wsConn, 1)
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		got <- newWSConn(c)
	}))
	t.Cleanup(srv.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server := <-got
	t.Cleanup(func() { _ = server.Close() })
	return server, client
}

func TestWSConnWritesAreBinaryMessages(t *testing.T) {
	server, client := wsPair(t)
	for _, p := range []string{"a", "bc", "def"} {
		if n, err := server.Write([]byte(p)); err != nil || n != len(p) {
			t.Fatalf("Write(%q) = %d, %v", p, n, err)
		}
	}
	for _, p := range []string{"a", "bc", "def"} {
		kind, data, err := client.ReadMessage()
		if err != nil || kind != websocket.BinaryMessage || string(data) != p {
			t.Fatalf("message = %d %q %v, want binary %q", kind, data, err, p)
		}
	}
}

func TestWSConnConcurrentWrites(t *testing.T) {
	server, client := wsPair(t)
	const writers, each = 8, 25
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				if _, err := server.Write([]byte("0123456789")); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	for i := 0; i < writers*each; i++ {
		_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, data, err := client.ReadMessage(); err != nil || string(data) != "0123456789" {
			t.Fatalf("message %d = %q, %v", i, data, err)
		}
	}
	wg.Wait()
}

func TestWSConnCloseIsIdempotent(t *testing.T) {
	server, client := wsPair(t)
	if err := server.Close(); err != nil {
		t.Fatalf("first Close = %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err := client.ReadMessage()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.CloseNormalClosure {
		t.Fatalf("client saw %v, want a normal close frame", err)
	}
	if _, err := server.Write([]byte("x")); err == nil {
		t.Fatal("Write after Close succeeded")
	}
}

// oversizedHeader is the header of a message that declares a body of just
// under 2 GiB, the way a client would try to make the gateway allocate it.
func oversizedHeader(kind byte) []byte {
	return []byte{kind, 0x7F, 0xFF, 0xFF, 0xFF}
}

func TestWSOversizedMessageBodyCloses(t *testing.T) {
	// Before the password, which is when the gateway is reading for it.
	t.Run("before login", func(t *testing.T) {
		g := newWSRig(t)
		c := g.dial(wsTestAddress)
		c.frame(startupBytes(t, appStartup))
		if got := c.recv(); got != "AuthenticationCleartextPassword" {
			t.Fatalf("first message = %q, want AuthenticationCleartextPassword", got)
		}
		c.frame(oversizedHeader('p'))
		c.expectClosed()
		if n := g.h.dialer.dials(); n != 0 {
			t.Fatalf("an oversized message dialed %d backends", n)
		}
	})
	t.Run("after login", func(t *testing.T) {
		g := newWSRig(t)
		c := g.dial(wsTestAddress)
		c.login(g)
		c.frame(oversizedHeader('Q'))
		c.expectClosed()
	})
	t.Run("just over the limit", func(t *testing.T) {
		g := newWSRig(t)
		g.hd.readLimit = 1024
		c := g.dial(wsTestAddress)
		c.login(g)
		// A body of 1025 bytes is refused at its header, with only five
		// bytes sent.
		c.frame([]byte{'Q', 0, 0, 0x04, 0x05})
		c.expectClosed()
	})
}

func TestWSMessageOverReadLimitCloses1009(t *testing.T) {
	g := newWSRig(t)
	g.hd.readLimit = 1024
	c := g.dial(wsTestAddress)
	c.login(g)

	// One WebSocket message larger than the limit, though it holds nothing
	// but valid Postgres messages.
	var big []byte
	for len(big) <= 2048 {
		big = append(big, frontendBytes(t, &pgproto3.Query{String: "SELECT 1"})...)
	}
	c.frame(big)
	err := c.expectClosed()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.CloseMessageTooBig {
		t.Fatalf("closed with %v, want close code %d", err, websocket.CloseMessageTooBig)
	}
}

func TestWSBusySessionDoesNotCostTheConnection(t *testing.T) {
	g := newWSRig(t)
	g.hd.pingInterval = 50 * time.Millisecond
	g.hd.readWait = 300 * time.Millisecond
	g.h.r.pauseLimit = 5 * time.Second
	c := g.dial(wsTestAddress)
	c.login(g)

	// The route pauses, and the next query waits for it to resume. While it
	// waits, the reader is held up behind it and isn't reading the socket,
	// so pongs go unread for longer than the read timeout. The connection
	// has to survive that.
	paused := g.h.route("r1", "orders")
	paused.State = RouteStatePaused
	g.h.apply(paused)
	c.frame(frontendBytes(t, &pgproto3.Query{String: "SELECT 1"}))
	time.Sleep(900 * time.Millisecond)
	g.h.apply(g.h.route("r1", "orders"))
	want(t, "the query that waited", c.readUntilReady(), selectOne)

	c.frame(frontendBytes(t, &pgproto3.Query{String: "SELECT 1"}))
	want(t, "query after the wait", c.readUntilReady(), selectOne)
}

func TestWSStalledClientWriteTimesOut(t *testing.T) {
	g := newWSRig(t)
	g.hd.writeWait = 300 * time.Millisecond
	c := g.dial(wsTestAddress)
	c.login(g)

	// Ask for far more result than the socket buffers hold, and never read
	// any of it.
	row := strings.Repeat("x", 1<<20)
	var queries []pgproto3.FrontendMessage
	for i := 0; i < 8; i++ {
		queries = append(queries, &pgproto3.Query{String: "SELECT " + row})
	}
	// Sent from the side: the gateway closes the connection under it.
	all := frontendBytes(t, queries...)
	go func() { _ = c.c.WriteMessage(websocket.BinaryMessage, all) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		g.h.r.mu.Lock()
		live := len(g.h.r.sessions)
		g.h.r.mu.Unlock()
		if live == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the session is still live, stuck writing to a client that stopped reading")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWSConnWriteTimesOut(t *testing.T) {
	server, _ := wsPair(t)
	server.writeWait = 200 * time.Millisecond
	// The client never reads, so the socket buffers fill and a write blocks.
	chunk := make([]byte, 1<<20)
	done := make(chan error, 1)
	go func() {
		for {
			if _, err := server.Write(chunk); err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case err := <-done:
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			t.Fatalf("Write failed with %v, want a timeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Write to a client that doesn't read never gave up")
	}
}

func TestWSConnReadSpansMessages(t *testing.T) {
	server, client := wsPair(t)
	for _, p := range []string{"he", "", "llo ", "world"} {
		if err := client.WriteMessage(websocket.BinaryMessage, []byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	if err := client.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(server)
	if err != nil || string(got) != "hello world" {
		t.Fatalf("ReadAll = %q, %v, want hello world and a clean end", got, err)
	}
}
