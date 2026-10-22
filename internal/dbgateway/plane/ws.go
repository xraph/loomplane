package plane

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgproto3"
)

const (
	// wsStartupTimeout is how long a client has to send its StartupMessage
	// after the upgrade.
	wsStartupTimeout = 10 * time.Second
	// wsPingInterval is how often the gateway pings an open connection, and
	// wsReadTimeout how long it may hear nothing (no data, no pong) before
	// it is dropped.
	wsPingInterval = 30 * time.Second
	wsReadTimeout  = 60 * time.Second
	// wsCloseWait bounds the best-effort close frame.
	wsCloseWait = time.Second
)

var (
	errWSCancel  = errors.New("plane: CancelRequest over WebSocket, which has no session to cancel")
	errWSStartup = errors.New("plane: unexpected message at startup")
	errWSText    = errors.New("plane: WebSocket text message; only binary messages carry Postgres")
)

// wsHandler upgrades /v2 requests, the way Neon's serverless driver opens a
// connection, and hands each one to the Router.
type wsHandler struct {
	router *Router
	// base is the lifetime of every connection the handler serves. The
	// request's own context can't bound a hijacked WebSocket, so a session
	// runs under this one instead and ends when it does.
	base context.Context

	up                                                websocket.Upgrader
	readLimit                                         int64
	startupTimeout, pingInterval, readWait, writeWait time.Duration
}

// WebSocketHandler serves Postgres over WebSocket at /v2?address=<host>:<port>.
// The host part picks the route, as the logical host of a TCP connection
// would. Sessions end only when the Router ends them or the client leaves.
func WebSocketHandler(r *Router) http.Handler {
	return WebSocketHandlerContext(context.Background(), r)
}

// WebSocketHandlerContext is WebSocketHandler with sessions bound to base:
// cancelling it closes every connection the handler is serving.
func WebSocketHandlerContext(base context.Context, r *Router) http.Handler {
	return &wsHandler{
		router: r,
		base:   base,
		// Clients authenticate with a password and carry no cookies, so
		// there is nothing for a cross-origin page to ride on.
		up:             websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		readLimit:      MaxMessageLen,
		startupTimeout: wsStartupTimeout,
		pingInterval:   wsPingInterval,
		readWait:       wsReadTimeout,
		writeWait:      wsReadTimeout,
	}
}

func (h *wsHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	host, ok := wsRouteHost(req.URL.Query().Get("address"))
	if !ok {
		http.Error(w, "the address query parameter is required", http.StatusBadRequest)
		return
	}
	if h.router.Draining() {
		http.Error(w, msgShuttingDown, http.StatusServiceUnavailable)
		return
	}
	c, err := h.up.Upgrade(w, req, nil)
	if err != nil {
		// Upgrade has answered the request.
		return
	}
	ws := newWSConn(c)
	ws.writeWait = h.writeWait
	c.SetReadLimit(h.readLimit)
	// The connection is closed when the handler returns, and with it
	// everything below that reads or writes it.
	defer ws.Close()
	stopBase := context.AfterFunc(h.base, func() { _ = ws.Close() })
	defer stopBase()

	// A pong is the one sign of life an idle client gives.
	c.SetPongHandler(func(string) error {
		ws.extendRead()
		return nil
	})
	done := make(chan struct{})
	defer close(done)
	go ws.ping(h.pingInterval, done)

	be := pgproto3.NewBackend(ws, ws)
	_ = c.SetReadDeadline(time.Now().Add(h.startupTimeout)) //nolint:errcheck // a dead socket fails the startup read
	startup, err := readWSStartup(be, ws)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			SendError(be, "08P01", "the database gateway got no startup message in time")
		}
		return
	}
	ws.readWait.Store(int64(h.readWait))
	ws.extendRead()

	ctx, cancel := context.WithCancel(h.base)
	defer cancel()
	// The client's PasswordMessage may already be behind the startup packet
	// (Neon's driver pipelines it), but the usual cleartext request goes out
	// anyway: it is what a client that waits for it needs, and one that
	// pipelined just has the answer read from its buffer.
	_ = h.router.Serve(ctx, be, ws, host, startup, nil) //nolint:errcheck // Serve has told the client; the socket closes on return
}

// wsRouteHost turns the address query parameter, host or host:port, into the
// route host. ok is false when there is no host.
func wsRouteHost(address string) (host string, ok bool) {
	if h, _, err := net.SplitHostPort(address); err == nil {
		host = h
	} else {
		host = strings.TrimSuffix(strings.TrimPrefix(address, "["), "]")
	}
	return host, host != ""
}

// readWSStartup reads the client's StartupMessage. SSL and GSS encryption
// requests are declined with 'N', since the WebSocket is already inside TLS,
// and the client sends the real startup packet next. A CancelRequest has
// nothing to cancel here, so it ends the connection.
func readWSStartup(be *pgproto3.Backend, w io.Writer) (map[string]string, error) {
	for {
		msg, err := be.ReceiveStartupMessage()
		if err != nil {
			return nil, err
		}
		switch m := msg.(type) {
		case *pgproto3.StartupMessage:
			return m.Parameters, nil
		case *pgproto3.SSLRequest, *pgproto3.GSSEncRequest:
			if _, err := w.Write([]byte{'N'}); err != nil {
				return nil, err
			}
		case *pgproto3.CancelRequest:
			return nil, errWSCancel
		default:
			return nil, errWSStartup
		}
	}
}

// wsConn adapts a WebSocket to the byte stream pgproto3 reads and writes.
// Every Write is one binary message, and Read carries on across the
// messages of the client's stream, whatever their boundaries.
type wsConn struct {
	conn *websocket.Conn

	// Reads come from one goroutine at a time, so cur needs no lock.
	cur io.Reader

	wmu       sync.Mutex // serialises Write
	writeWait time.Duration

	readWait  atomic.Int64 // ns; 0 until startup is done
	closeOnce sync.Once
}

func newWSConn(c *websocket.Conn) *wsConn { return &wsConn{conn: c, writeWait: wsReadTimeout} }

// extendRead pushes the read deadline out by the read timeout, once startup
// has set one.
func (c *wsConn) extendRead() {
	if d := time.Duration(c.readWait.Load()); d > 0 {
		_ = c.conn.SetReadDeadline(time.Now().Add(d)) //nolint:errcheck // a dead socket fails the next read
	}
}

// Read fills p from the client's binary messages. A message is not a
// boundary: a Postgres message may span several, and one may hold several.
// A text message closes the connection.
func (c *wsConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	// A reader that was busy elsewhere for a while has not been listening for
	// pongs, so the clock for the next one starts now.
	c.extendRead()
	for {
		if c.cur == nil {
			kind, r, err := c.conn.NextReader()
			if err != nil {
				return 0, wsReadError(err)
			}
			if kind != websocket.BinaryMessage {
				_ = c.closeWith(websocket.CloseUnsupportedData, "binary messages only") //nolint:errcheck // the read error below ends the session
				return 0, errWSText
			}
			c.cur = r
		}
		n, err := c.cur.Read(p)
		if errors.Is(err, io.EOF) {
			c.cur = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, wsReadError(err)
	}
}

// wsReadError reports a client that closed the WebSocket cleanly as io.EOF.
func wsReadError(err error) error {
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
		return io.EOF
	}
	return err
}

// Write sends p as one binary message. A client that stops reading fails the
// write once the write timeout passes. A failed write leaves the WebSocket
// unusable, so it closes the socket: the session's reader then fails too, and
// the session ends instead of holding its backend connection for good.
func (c *wsConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(c.writeWait)) //nolint:errcheck // a dead socket fails the write below
	if err := c.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		// No close frame: the stream is broken mid-message.
		c.closeOnce.Do(func() { _ = c.conn.Close() })
		return 0, err
	}
	return len(p), nil
}

// Close tells the client the connection is ending, if it can in a second,
// and closes the socket. It is safe to call more than once and from any
// goroutine.
func (c *wsConn) Close() error {
	return c.closeWith(websocket.CloseNormalClosure, "")
}

func (c *wsConn) closeWith(code int, text string) error {
	var err error
	c.closeOnce.Do(func() {
		_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, text), time.Now().Add(wsCloseWait)) //nolint:errcheck // the client may be gone; Close below reports
		err = c.conn.Close()
	})
	return err
}

// ping sends a ping every interval until done closes, and closes the
// connection when one can't be sent.
func (c *wsConn) ping(interval time.Duration, done <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if err := c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(interval)); err != nil {
				_ = c.Close()
				return
			}
		}
	}
}
