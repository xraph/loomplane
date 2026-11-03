package nodes

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// Timings the hub runs on. Heartbeats and the offline cutoff are set by the
// global constraints; the rest bound how long one step may hold things up.
const (
	challengeTTL     = 60 * time.Second
	heartbeatEvery   = 15 * time.Second
	offlineAfter     = 45 * time.Second
	hookTimeout      = 30 * time.Second
	storeTimeout     = 10 * time.Second
	verdictTimeout   = 30 * time.Second
	controlWriteWait = 10 * time.Second
	revokeGrace      = 500 * time.Millisecond
	controlQueue     = 64
	maxControlLine   = 1 << 20
	// nonceSweepMargin is how long a spent nonce outlives its expiry, to
	// cover clock skew between replicas.
	nonceSweepMargin = 5 * time.Minute
)

var (
	// ErrEndpointRefused is returned when the node refuses a stream. The
	// error's text ends with the node's reason.
	ErrEndpointRefused = errors.New("the node refused the stream")
	// ErrUnauthorized is returned by Accept for a connection it does not
	// trust. The wrapped error says why, for logs; don't show it to the node.
	ErrUnauthorized = errors.New("nodes: the node could not be authenticated")
	// ErrNodeRevoked is returned by Authenticate, wrapped in ErrUnauthorized,
	// for a revoked node whose signature checked out. Only the key holder can
	// get it, so the node may be told it was revoked.
	ErrNodeRevoked = errors.New("nodes: the node was revoked")
	// ErrHubConfig is returned when the hub is missing what it needs to run.
	ErrHubConfig = errors.New("nodes: hub configuration")
	// ErrHopInsecure is returned when a hop would cross plain http and
	// HopInsecure is not set.
	ErrHopInsecure = errors.New("nodes: refusing a replica hop over plain http; use an https advertise URL or set HopInsecure")
	// ErrSuperseded is returned by Accept when a newer session for the same
	// node took over before this one was registered.
	ErrSuperseded = errors.New("nodes: a newer session for this node took over")
)

// ErrNodeOffline is returned by Open when no replica holds the node's session.
//
//nolint:errname // later phases match on this name
type ErrNodeOffline struct {
	NodeID string
	Name   string
	Since  time.Time
}

func (e ErrNodeOffline) Error() string {
	if e.Since.IsZero() {
		return fmt.Sprintf("node %s is offline", e.Name)
	}
	return fmt.Sprintf("node %s is offline since %s", e.Name, e.Since.UTC().Format("15:04 UTC"))
}

func offline(n *Node) error {
	since := n.UpdatedAt
	if n.LastSeenAt != nil {
		since = *n.LastSeenAt
	}
	return ErrNodeOffline{NodeID: n.ID, Name: n.Name, Since: since}
}

// refusedError carries the node's reason for refusing a stream.
type refusedError struct{ reason string }

func (e *refusedError) Error() string { return ErrEndpointRefused.Error() + ": " + e.reason }
func (e *refusedError) Unwrap() error { return ErrEndpointRefused }

// HubConfig is what a hub needs to know about the instance and its replica.
type HubConfig struct {
	// Audience is the instance's public URL. Nodes sign it; it is normalized
	// with NormalizeAudience and must not be empty.
	Audience string
	// Replica names this replica in logs.
	Replica string
	// AdvertiseURL is where other replicas reach this one's hop. It is
	// recorded on each session this replica holds. Empty means a single
	// replica: sessions are recorded under "replica:<Replica>" and no other
	// replica can hop to them.
	AdvertiseURL string
	// HopSecret authenticates replica-to-replica hops. Required.
	HopSecret []byte
	// HopInsecure allows hops over plain http: an http AdvertiseURL here, and
	// dialing http session addresses from here. Off by default; the hop
	// carries stream bytes, so only set it on a private network.
	HopInsecure bool
	// HopRootCAs verifies other replicas' certificates on https hops. Nil
	// means the system roots.
	HopRootCAs *x509.CertPool
	// ChallengeSecret signs challenge nonces so any replica can check them.
	// Required. Derive it from the instance secret with HKDF info
	// "loomplane-node-challenge".
	ChallengeSecret []byte
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// Log receives what goes wrong off the request path. Nil means slog.Default().
	Log *slog.Logger
}

// Hub holds the node sessions this replica owns and opens streams to any
// node, through the replica that holds it when that isn't this one.
type Hub struct {
	cfg      HubConfig
	audience string
	audErr   error
	self     string
	log      *slog.Logger
	store    Store
	svc      *Service

	mu          sync.Mutex
	sessions    map[string]*session
	onOnline    []func(ctx context.Context, n *Node)
	onInventory []func(ctx context.Context, n *Node, old, cur nodeproto.Inventory)

	hopMu   sync.Mutex
	hopSeen map[string]time.Time // hop MACs already used, until they expire

	// unreachableOnce warns once that another replica holds sessions with no
	// advertise URL, so this one can't reach its nodes.
	unreachableOnce sync.Once
}

// NewHub returns a hub. It does no I/O; Bind checks the configuration.
func NewHub(cfg HubConfig) *Hub {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	h := &Hub{cfg: cfg, log: cfg.Log, sessions: map[string]*session{}}
	if h.log == nil {
		h.log = slog.Default()
	}
	h.audience, h.audErr = nodeproto.NormalizeAudience(cfg.Audience)
	h.self = "replica:" + cfg.Replica
	if cfg.AdvertiseURL != "" {
		if u, err := nodeproto.NormalizeAudience(cfg.AdvertiseURL); err == nil {
			h.self = u
		} else {
			h.audErr = errors.Join(h.audErr, fmt.Errorf("advertise URL: %w", err))
		}
	}
	return h
}

// NormalizeAudience is nodeproto.NormalizeAudience, so the node side and the
// hub compare one form.
func NormalizeAudience(raw string) (string, error) { return nodeproto.NormalizeAudience(raw) }

// Bind gives the hub its store and service, and makes the hub the service's
// notifier. It refuses an empty or malformed audience, a malformed advertise
// URL, and empty secrets, so a misconfigured replica does not start.
func (h *Hub) Bind(store Store, svc *Service) error {
	if h.audErr != nil {
		return fmt.Errorf("%w: audience: %w", ErrHubConfig, h.audErr)
	}
	if len(h.cfg.HopSecret) == 0 {
		return fmt.Errorf("%w: the hop secret is empty", ErrHubConfig)
	}
	if len(h.cfg.ChallengeSecret) == 0 {
		return fmt.Errorf("%w: the challenge secret is empty", ErrHubConfig)
	}
	if store == nil {
		return fmt.Errorf("%w: no store", ErrHubConfig)
	}
	if strings.HasPrefix(h.self, "http://") {
		if !h.cfg.HopInsecure {
			return fmt.Errorf("%w: the advertise URL %s is plain http: %w", ErrHubConfig, h.self, ErrHopInsecure)
		}
		h.log.Warn("nodes: replica hops run over plain http; stream bytes cross the network unencrypted", "advertise_url", h.self)
	}
	if h.cfg.AdvertiseURL == "" {
		h.log.Info("nodes: nodes.advertise_url is not set, which is right for a single replica; with several replicas on one database, set it on each, or a node is reachable only through the replica it connected to", "replica", h.self)
	}
	h.store, h.svc = store, svc
	if svc != nil && svc.Notify == nil {
		svc.Notify = h.Notify
	}
	return nil
}

func (h *Hub) now() time.Time { return h.cfg.Now() }

// opCtx is a bounded context for one store call made off the request path.
func opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), storeTimeout)
}

// Challenge returns a challenge for the node to sign. The nonce is
// random16 || expiry (unix seconds, 8 bytes) || HMAC(ChallengeSecret,
// nodeID || random16 || expiry)[:16], base64url encoded, so any replica can
// check it without shared memory. Accept spends it once, in the store.
func (h *Hub) Challenge(nodeID string) nodeproto.Challenge {
	now := h.now()
	raw := make([]byte, 40)
	_, _ = rand.Read(raw[:16])
	binary.BigEndian.PutUint64(raw[16:24], uint64(now.Add(challengeTTL).Unix())) //nolint:gosec // a unix time after 1970 is positive
	copy(raw[24:], h.nonceMAC(nodeID, raw[:24]))
	return nodeproto.Challenge{NodeID: nodeID, Audience: h.audience, Nonce: base64.RawURLEncoding.EncodeToString(raw), IssuedAt: now.UTC()}
}

func (h *Hub) nonceMAC(nodeID string, body []byte) []byte {
	m := hmac.New(sha256.New, h.cfg.ChallengeSecret)
	m.Write([]byte(nodeID))
	m.Write(body)
	return m.Sum(nil)[:16]
}

// checkNonce returns the nonce's expiry when this instance issued it for
// nodeID and it has not expired.
func (h *Hub) checkNonce(nodeID, nonce string, now time.Time) (time.Time, error) {
	raw, err := base64.RawURLEncoding.DecodeString(nonce)
	if err != nil || len(raw) != 40 {
		return time.Time{}, errors.New("malformed nonce")
	}
	if len(h.cfg.ChallengeSecret) == 0 || !hmac.Equal(raw[24:], h.nonceMAC(nodeID, raw[:24])) {
		return time.Time{}, errors.New("nonce not issued for this node")
	}
	secs := binary.BigEndian.Uint64(raw[16:24])
	if secs > math.MaxInt64 {
		return time.Time{}, errors.New("malformed nonce")
	}
	exp := time.Unix(int64(secs), 0)
	if !now.Before(exp) {
		return time.Time{}, errors.New("nonce expired")
	}
	return exp, nil
}

// Authenticate checks the assertion: a nonce issued for this node and not
// expired, a node that exists, a signature by its key over this instance's
// audience, a nonce not used before, and a node that is not revoked. The key
// is looked up by the challenge's node id, so a key can only answer for its
// own node. The revoke is checked last, once the signature has proved the
// caller holds the key, so only the node itself learns that it was revoked
// (ErrNodeRevoked); anyone else gets a plain ErrUnauthorized.
func (h *Hub) Authenticate(ctx context.Context, a nodeproto.Assertion) (*Node, error) {
	if h.store == nil {
		return nil, fmt.Errorf("%w: Authenticate before Bind", ErrHubConfig)
	}
	c := a.Challenge
	now := h.now()
	exp, err := h.checkNonce(c.NodeID, c.Nonce, now)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnauthorized, err)
	}
	n, err := h.store.GetByID(ctx, c.NodeID)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("%w: unknown node", ErrUnauthorized)
	}
	if err != nil {
		return nil, err
	}
	if n.ID != c.NodeID {
		return nil, fmt.Errorf("%w: node id mismatch", ErrUnauthorized)
	}
	if err := nodeproto.Verify(n.PublicKey, a, h.audience, now); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnauthorized, err)
	}
	if err := h.store.UseNonce(ctx, c.Nonce, exp); err != nil {
		if errors.Is(err, ErrNonceUsed) {
			return nil, fmt.Errorf("%w: %w", ErrUnauthorized, err)
		}
		return nil, err
	}
	if n.RevokedAt != nil || n.Status == StatusRevoked {
		return nil, fmt.Errorf("%w: %w", ErrUnauthorized, ErrNodeRevoked)
	}
	return n, nil
}

// yamuxConfig is the control plane's side of the session: keepalives every
// heartbeat interval, and no stderr logging.
func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = heartbeatEvery
	cfg.ConnectionWriteTimeout = controlWriteWait
	cfg.LogOutput = io.Discard
	return cfg
}

// refuseUnauthorized tells a node it was refused after the upgrade, with a
// close frame it can act on, then closes conn.
func refuseUnauthorized(conn *websocket.Conn) {
	msg := websocket.FormatCloseMessage(nodeproto.CloseUnauthorized, "unauthorized")
	_ = conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(time.Second)) //nolint:errcheck // the connection is closing either way
	_ = conn.Close()
}

// Accept holds the session of a node Authenticate approved, until it ends.
// It owns conn: it closes it before returning, on every path. It returns nil
// when an accepted session ends.
func (h *Hub) Accept(ctx context.Context, conn *websocket.Conn, n *Node) error {
	if h.store == nil {
		_ = conn.Close()
		return fmt.Errorf("%w: Accept before Bind", ErrHubConfig)
	}
	epoch, err := h.store.ClaimSession(ctx, Session{NodeID: n.ID, Replica: h.self, RemoteAddr: conn.RemoteAddr().String(), ConnectedAt: h.now().UTC()})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			refuseUnauthorized(conn)
			return fmt.Errorf("%w: node revoked", ErrUnauthorized)
		}
		_ = conn.Close()
		return err
	}
	mux, err := yamux.Client(NewWSConn(conn), yamuxConfig())
	if err != nil {
		_ = conn.Close()
		h.release(n.ID, epoch)
		return err
	}
	ctrl, err := mux.OpenStream()
	if err != nil {
		_ = mux.Close()
		h.release(n.ID, epoch)
		return err
	}
	s := newSession(h, n, epoch, mux, ctrl)
	s.conn = conn
	s.remote = conn.RemoteAddr().String()
	// Hello goes first, before the writer starts, so nothing can pass it.
	if err := s.writeNow(nodeproto.Hello{Version: nodeproto.Version, Epoch: epoch}); err != nil {
		s.close()
		h.release(n.ID, epoch)
		return err
	}
	// Online after the claim, before the session is visible here. An older
	// session ending meanwhile sees a newer epoch and leaves the status alone.
	h.markOnline(s)
	if !h.register(s) {
		s.close()
		h.release(n.ID, epoch)
		return ErrSuperseded
	}
	// A revoke that committed after the claim found no session to tell.
	// Revoke writes before it notifies and this reads after registering, so
	// one of the two sees the other.
	if cur, err := h.store.GetByID(ctx, n.ID); err == nil && (cur.RevokedAt != nil || cur.Status == StatusRevoked) {
		s.deliver(nodeproto.Revoked{})
	}
	stop := context.AfterFunc(ctx, s.close)
	defer stop()
	go s.writeLoop()
	s.readLoop()
	s.close()
	h.unregister(s)
	h.ended(s)
	return nil
}

// Online reports whether this replica holds a live session for the node.
func (h *Hub) Online(nodeID string) bool { return h.local(nodeID) != nil }

// OnOnline registers fn to run once per session, after the session's first
// inventory is stored.
func (h *Hub) OnOnline(fn func(ctx context.Context, n *Node)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onOnline = append(h.onOnline, fn)
}

// OnInventory registers fn to run when a node's stored inventory changes.
func (h *Hub) OnInventory(fn func(ctx context.Context, n *Node, old, cur nodeproto.Inventory)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onInventory = append(h.onInventory, fn)
}

// runHook runs fn in its own goroutine with a deadline. A panic is logged and
// goes no further: a hook never ends a session.
func (h *Hub) runHook(name, nodeID string, fn func(ctx context.Context)) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				h.log.Error("nodes: a hook panicked", "hook", name, "node", nodeID, "panic", fmt.Sprint(r))
			}
		}()
		fn(ctx)
	}()
}

func (h *Hub) fireOnline(n *Node) {
	h.mu.Lock()
	hooks := slices.Clone(h.onOnline)
	h.mu.Unlock()
	for _, fn := range hooks {
		c := cloneNode(n)
		h.runHook("online", n.ID, func(ctx context.Context) { fn(ctx, c) })
	}
}

func (h *Hub) fireInventory(n *Node, old, cur nodeproto.Inventory) {
	h.mu.Lock()
	hooks := slices.Clone(h.onInventory)
	h.mu.Unlock()
	for _, fn := range hooks {
		c := cloneNode(n)
		h.runHook("inventory", n.ID, func(ctx context.Context) { fn(ctx, c, old, cur) })
	}
}

func cloneNode(n *Node) *Node {
	c := *n
	c.Endpoints = slices.Clone(n.Endpoints)
	return &c
}

// notifiable reports a message the control plane may push to a node.
func notifiable(msg any) bool {
	switch msg.(type) {
	case nodeproto.Narrow, nodeproto.SetIngress, nodeproto.Revoked:
		return true
	}
	return false
}

// Notify delivers a control message to the node's session wherever it is. It
// never blocks on the network: a local session queues it, and the replica
// the session row names gets it through the hop in the background. Both
// happen, because the local session may be a stale half-open one while the
// live session is elsewhere. Every message here is idempotent on the node,
// and the hop is skipped when the row names this replica.
func (h *Hub) Notify(nodeID string, msg any) {
	if !notifiable(msg) {
		h.log.Warn("nodes: refusing to notify a node with this message", "node", nodeID, "type", fmt.Sprintf("%T", msg))
		return
	}
	if s := h.local(nodeID); s != nil {
		s.deliver(msg)
	}
	go h.notifyRemote(nodeID, msg)
}

// Open opens a stream to a node of org. A local session opens it here; a
// session on another replica opens it through the hop.
func (h *Hub) Open(ctx context.Context, orgID, nodeID string, hdr nodeproto.StreamHeader) (net.Conn, error) {
	return h.open(ctx, orgID, nodeID, hdr, true)
}

func (h *Hub) open(ctx context.Context, orgID, nodeID string, hdr nodeproto.StreamHeader, hop bool) (net.Conn, error) {
	if h.store == nil {
		return nil, fmt.Errorf("%w: Open before Bind", ErrHubConfig)
	}
	if !hdr.Endpoint.Valid() || hdr.Endpoint == nodeproto.EndpointControl {
		return nil, fmt.Errorf("%w: cannot open a %q stream", ErrInvalid, hdr.Endpoint)
	}
	n, err := h.store.Get(ctx, orgID, nodeID)
	if err != nil {
		return nil, err
	}
	if n.RevokedAt != nil || n.Status == StatusRevoked {
		return nil, ErrNotFound
	}
	hdr.OrgID = orgID
	row, err := h.store.Session(ctx, nodeID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	s := h.local(nodeID)
	switch {
	case row == nil:
		if s != nil {
			s.close() // no row names it, so it is stale
		}
	case row.Replica == h.self && s != nil && s.epoch == row.Epoch:
		st, err := s.open(ctx, hdr)
		if errors.Is(err, yamux.ErrSessionShutdown) {
			return nil, offline(n)
		}
		return st, err
	case row.Replica != h.self && hop && hopTarget(row.Replica):
		if s != nil {
			s.close() // a newer session lives on another replica
		}
		if !h.hopAllowed(row.Replica) {
			h.log.Warn("nodes: not hopping to a replica over plain http", "node", n.ID, "replica", row.Replica)
			return nil, ErrHopInsecure
		}
		return h.hopOpen(ctx, row.Replica, n, hdr)
	}
	if s != nil && row != nil && row.Epoch > s.epoch {
		s.close()
	}
	if row != nil && row.Replica != h.self && !hopTarget(row.Replica) {
		h.unreachableOnce.Do(func() {
			h.log.Warn("nodes: another replica holds node sessions but has no nodes.advertise_url, so this replica can't reach those nodes and revokes reach them only through the heartbeat; set nodes.advertise_url on every replica", "replica", row.Replica, "node", n.ID)
		})
	}
	return nil, offline(n)
}

// Sweep marks nodes without a heartbeat for 45 seconds offline, and drops
// expired challenge nonces, now and every 15 seconds until ctx ends.
func (h *Hub) Sweep(ctx context.Context) {
	t := time.NewTicker(heartbeatEvery)
	defer t.Stop()
	for {
		h.sweepOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (h *Hub) sweepOnce(ctx context.Context) {
	if h.store == nil {
		return
	}
	now := h.now()
	if ids, err := h.store.MarkStaleOffline(ctx, now.Add(-offlineAfter)); err != nil {
		h.log.Warn("nodes: marking stale nodes offline", "error", err)
	} else if len(ids) > 0 {
		h.log.Info("nodes: marked nodes offline", "nodes", strings.Join(ids, ","))
	}
	// Spent nonces are kept past their expiry by a margin: another replica
	// whose clock runs behind this one's still accepts a nonce this one
	// sees as expired, and deleting it would let it be used again there.
	if err := h.store.DeleteExpiredNonces(ctx, now.Add(-nonceSweepMargin)); err != nil {
		h.log.Warn("nodes: deleting expired challenge nonces", "error", err)
	}
	h.closeSilent(now)
}

// closeSilent closes local sessions that sent no heartbeat for 45 seconds.
// It sends nothing: the node reconnects, and a fresh session takes over.
func (h *Hub) closeSilent(now time.Time) {
	h.mu.Lock()
	var silent []*session
	for _, s := range h.sessions {
		if now.Sub(time.Unix(0, s.lastBeat.Load())) > offlineAfter {
			silent = append(silent, s)
		}
	}
	h.mu.Unlock()
	for _, s := range silent {
		h.log.Info("nodes: closing a session with no heartbeat", "node", s.nodeID, "epoch", s.epoch)
		s.close()
	}
}
