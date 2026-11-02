package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

const (
	// maxAssertion bounds the first message. A signed challenge is a few
	// hundred bytes.
	maxAssertion = 16 << 10
	// maxNodeID bounds the node a challenge is asked for.
	maxNodeID = 128
)

// assertionTimeout is how long a new connection has to send its assertion.
// A variable so a test need not wait the whole time.
var assertionTimeout = 10 * time.Second

// upgrader accepts a connection only when it carries no Origin header. A node
// is not a browser, so a page in someone's browser can never open one.
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
}

// mountNodeEndpoints serves what sits outside /api/: a node asks for a
// challenge and connects, and replicas hop to each other. None of them is in
// the generated API client.
func (e *Extension) mountNodeEndpoints(r forge.Router) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/node/v1/challenge", e.challenge)
	mux.HandleFunc("/node/v1/connect", e.connect)
	mux.HandleFunc("/node/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`)) //nolint:errcheck // the client went away; nothing to do
	})
	if err := r.Handle("/node/", mux); err != nil {
		return err
	}
	return r.Handle(nodes.HopPath, e.hub.HopHandler())
}

// challenge hands out a challenge for a node to sign. It needs no session:
// the nonce is useless without the node's key.
func (e *Extension) challenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("node")
	if id == "" || len(id) > maxNodeID {
		http.Error(w, "send ?node=<the node's id>", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(e.hub.Challenge(id)) //nolint:errcheck // the client went away; nothing to do
}

// connect upgrades to a WebSocket and waits for the node's assertion, its
// first message. A bad one closes the connection with a close frame the node
// can read (nodeproto.CloseUnauthorized or CloseBadRequest); a good one hands
// the connection to the hub for the life of the session.
func (e *Extension) connect(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// The upgrader has answered: 400 for a plain request, 403 for an Origin.
		return
	}
	conn.SetReadLimit(maxAssertion)
	_ = conn.SetReadDeadline(time.Now().Add(assertionTimeout)) //nolint:errcheck // a dead connection fails the read below
	typ, msg, err := conn.ReadMessage()
	var a nodeproto.Assertion
	if err != nil || typ != websocket.TextMessage || json.Unmarshal(msg, &a) != nil {
		hangUp(conn, nodeproto.CloseBadRequest, "send the signed challenge as the first message")
		return
	}
	_ = conn.SetReadDeadline(time.Time{}) //nolint:errcheck // a dead connection ends in Accept
	ctx, cancel := e.sessionContext(r.Context())
	defer cancel()
	switch err = e.hub.Accept(ctx, conn, a); {
	case err == nil, errors.Is(err, context.Canceled):
	case errors.Is(err, nodes.ErrUnauthorized):
		// Why is for the log; the node learns only that it was refused.
		e.Logger().Info("a node was refused", forge.F("remote", r.RemoteAddr), forge.F("reason", err.Error()))
	default:
		e.Logger().Warn("a node session ended with an error", forge.F("remote", r.RemoteAddr), forge.F("error", err.Error()))
	}
}

// sessionContext ends with the request and with the extension, so stopping
// the platform closes the sessions it holds. A hijacked connection is not
// tracked by the HTTP server's shutdown.
func (e *Extension) sessionContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	e.mu.Lock()
	run := e.run
	e.mu.Unlock()
	if run == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(run, cancel)
	return ctx, func() { stop(); cancel() }
}

// hangUp sends a close frame and closes conn.
func hangUp(conn *websocket.Conn, code int, reason string) {
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second)) //nolint:errcheck // the connection is closing either way
	_ = conn.Close()
}
