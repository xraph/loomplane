package nodestest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// Org and User are who the test join token belongs to.
const (
	Org  = "org_1"
	User = "usr_1"
)

// Principal is a member who may manage nodes.
var Principal = security.Principal{UserID: User, OrgID: Org}

// Tokens is a join token spender that accepts every token.
type Tokens struct {
	mu    sync.Mutex
	spent int
}

// Spent is how many tokens have been spent.
func (t *Tokens) Spent() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.spent
}

// ConsumeNodeJoinToken implements nodes.JoinTokens.
func (t *Tokens) ConsumeNodeJoinToken(context.Context, string) (*apikeys.NodeJoinClaim, error) {
	t.mu.Lock()
	t.spent++
	t.mu.Unlock()
	return &apikeys.NodeJoinClaim{OrgID: Org, MintedBy: User, MintedByKeyID: "key_1"}, nil
}

type allowAll struct{}

func (allowAll) Allowed(context.Context, security.Principal, security.Permission) (bool, error) {
	return true, nil
}

// Env is a hub bound to an in-memory store, with the node-facing routes the
// control plane serves: join, challenge and connect.
type Env struct {
	Store    *Store
	Tokens   *Tokens
	Svc      *nodes.Service
	Audience string

	mu     sync.Mutex
	hub    *nodes.Hub
	cancel map[int]context.CancelFunc // sessions the current hub holds
	nextID int
}

// New returns an Env whose hub signs for audience.
func New(t testing.TB, audience string) *Env {
	t.Helper()
	e := &Env{Store: NewStore(), Tokens: &Tokens{}, Audience: audience, cancel: map[int]context.CancelFunc{}}
	e.Svc = &nodes.Service{Store: e.Store, Tokens: e.Tokens, Authz: allowAll{}, Version: "v1.8.0",
		Notify: func(id string, msg any) { e.Hub().Notify(id, msg) }}
	e.RestartHub(t)
	return e
}

// Hub is the current hub.
func (e *Env) Hub() *nodes.Hub {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.hub
}

// RestartHub ends every session the hub holds and replaces the hub with a
// fresh one on the same store, as a restarted control plane would.
func (e *Env) RestartHub(t testing.TB) {
	t.Helper()
	e.mu.Lock()
	old := e.cancel
	e.cancel = map[int]context.CancelFunc{}
	e.mu.Unlock()
	for _, cancel := range old {
		cancel()
	}
	h := nodes.NewHub(nodes.HubConfig{
		Audience: e.Audience, Replica: "test",
		HopSecret: []byte("hop-secret-for-tests-only-000000"), ChallengeSecret: []byte("challenge-secret-for-tests-00000"),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := h.Bind(e.Store, e.Svc); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.hub = h
	e.mu.Unlock()
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }}

// Handler serves /api/v1/nodes/join, /node/v1/challenge and /node/v1/connect
// the way extensions/nodes does.
func (e *Env) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/nodes/join", e.join)
	mux.HandleFunc("/node/v1/challenge", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(e.Hub().Challenge(r.URL.Query().Get("node"))) //nolint:errcheck // the client went away
	})
	mux.HandleFunc("/node/v1/connect", e.connect)
	return mux
}

func (e *Env) connect(w http.ResponseWriter, r *http.Request) {
	hub := e.Hub()
	a, err := nodeproto.DecodeAuthorization(r.Header.Get("Authorization"))
	var n *nodes.Node
	if err == nil {
		n, err = hub.Authenticate(r.Context(), a)
	}
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	e.mu.Lock()
	id := e.nextID
	e.nextID++
	e.cancel[id] = cancel
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.cancel, id)
		e.mu.Unlock()
	}()
	_ = hub.Accept(ctx, conn, n) //nolint:errcheck // a test hub logs nothing; the session ended either way
}

type joinBody struct {
	Token           string               `json:"token"`
	Name            string               `json:"name"`
	PublicKey       string               `json:"public_key"`
	Endpoints       []nodeproto.Endpoint `json:"endpoints"`
	Labels          map[string]string    `json:"labels"`
	Inventory       nodeproto.Inventory  `json:"inventory"`
	NodeVersion     string               `json:"node_version"`
	ProtocolVersion int                  `json:"protocol_version"`
}

func (e *Env) join(w http.ResponseWriter, r *http.Request) {
	var in joinBody
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the body must be JSON"})
		return
	}
	key, err := base64.StdEncoding.DecodeString(in.PublicKey)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "public_key must be base64"})
		return
	}
	res, err := e.Svc.Join(r.Context(), nodes.JoinRequest{Token: in.Token, Name: in.Name, PublicKey: key, Endpoints: in.Endpoints,
		Labels: in.Labels, Inventory: in.Inventory, NodeVersion: in.NodeVersion, ProtocolVersion: in.ProtocolVersion})
	switch {
	case errors.Is(err, nodes.ErrUpdateNode), errors.Is(err, nodes.ErrNodeNewer):
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": err.Error()})
		return
	case errors.Is(err, nodes.ErrInvalid), errors.Is(err, nodes.ErrNameTaken):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"node_id": res.NodeID, "org_id": res.OrgID,
		"identity_generation": res.IdentityGeneration, "endpoints": res.Endpoints, "cloud_id": res.CloudID})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) //nolint:errcheck // the client went away
}
