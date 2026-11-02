package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

const (
	// maxNodeID bounds the node a challenge is asked for.
	maxNodeID = 128
)

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

// connect authenticates a node from its Authorization header, then upgrades
// to a WebSocket and hands the connection to the hub for the life of the
// session. A request that is not a WebSocket upgrade, or carries a browser
// Origin, is answered before anything is checked, so it cannot spend a
// challenge. A missing, malformed or refused assertion is an HTTP 401 and no
// upgrade happens.
func (e *Extension) connect(w http.ResponseWriter, r *http.Request) {
	if !websocket.IsWebSocketUpgrade(r) {
		http.Error(w, "this endpoint is a WebSocket", http.StatusBadRequest)
		return
	}
	if !upgrader.CheckOrigin(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	a, err := nodeproto.DecodeAuthorization(r.Header.Get("Authorization"))
	if err != nil {
		unauthorized(w)
		return
	}
	n, err := e.hub.Authenticate(r.Context(), a)
	switch {
	case errors.Is(err, nodes.ErrUnauthorized):
		// Why is for the log; the node learns only that it was refused.
		e.Logger().Info("a node was refused", forge.F("remote", r.RemoteAddr), forge.F("reason", err.Error()))
		unauthorized(w)
		return
	case err != nil:
		e.Logger().Warn("a node could not be checked", forge.F("remote", r.RemoteAddr), forge.F("error", err.Error()))
		writeJSONError(w, http.StatusServiceUnavailable, "try again")
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ctx, cancel := e.sessionContext(r.Context())
	defer cancel()
	switch err = e.hub.Accept(ctx, conn, n); {
	case err == nil, errors.Is(err, context.Canceled):
	case errors.Is(err, nodes.ErrUnauthorized):
		e.Logger().Info("a node was revoked as it connected", forge.F("remote", r.RemoteAddr))
	default:
		e.Logger().Warn("a node session ended with an error", forge.F("remote", r.RemoteAddr), forge.F("error", err.Error()))
	}
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", nodeproto.AuthScheme)
	writeJSONError(w, http.StatusUnauthorized, "unauthorized")
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck // the client went away; nothing to do
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
