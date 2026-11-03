package nodes

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

var (
	testHopSecret       = []byte("hop-secret-for-tests-only-000000")
	testChallengeSecret = []byte("challenge-secret-for-tests-00000")
)

// clock is a test clock that runs with real time plus an offset.
type clock struct{ skew atomic.Int64 }

func (c *clock) Now() time.Time          { return time.Now().Add(time.Duration(c.skew.Load())) }
func (c *clock) advance(d time.Duration) { c.skew.Add(int64(d)) }

// syncBuf is a log sink tests can read while the hub writes to it.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// hubRig is one replica: a hub, its service and an HTTP server that serves
// the connect endpoint and the hop.
type hubRig struct {
	t        *testing.T
	st       Store
	store    *fakeStore // nil when st is a real store
	svc      *Service
	audit    *fakeAudit
	hub      *Hub
	srv      *httptest.Server
	clock    *clock
	logs     *syncBuf
	accepted chan error
	dialer   *websocket.Dialer
}

func newHubRig(t *testing.T, store *fakeStore, replica string) *hubRig {
	t.Helper()
	r := newHubRigOn(t, store, replica)
	r.store = store
	return r
}

func newHubRigOn(t *testing.T, store Store, replica string) *hubRig {
	t.Helper()
	return newHubRigWith(t, store, replica, rigOpts{insecure: true})
}

// rigOpts picks the rig's hop transport: plain http with HopInsecure, or TLS
// trusting the certificates in pool.
type rigOpts struct {
	insecure bool
	tls      bool
	pool     *x509.CertPool
}

func newHubRigWith(t *testing.T, store Store, replica string, o rigOpts) *hubRig {
	t.Helper()
	r := &hubRig{t: t, st: store, audit: &fakeAudit{}, clock: &clock{}, logs: &syncBuf{}, accepted: make(chan error, 16)}
	r.svc = &Service{Store: store, Authz: fakeAuthz{manage: true}, Audit: r.audit, Version: "v1.8.0"}
	r.srv = httptest.NewUnstartedServer(nil)
	scheme := "http://"
	if o.tls {
		scheme = "https://"
	}
	r.hub = NewHub(HubConfig{
		Audience: "https://Loom.Example.com/", Replica: replica, AdvertiseURL: scheme + r.srv.Listener.Addr().String(),
		HopSecret: testHopSecret, ChallengeSecret: testChallengeSecret, Now: r.clock.Now,
		HopInsecure: o.insecure, HopRootCAs: o.pool,
		Log: slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err := r.hub.Bind(store, r.svc); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	up := websocket.Upgrader{}
	mux.HandleFunc("/node/v1/connect", func(w http.ResponseWriter, req *http.Request) {
		a, err := nodeproto.DecodeAuthorization(req.Header.Get("Authorization"))
		var n *Node
		if err == nil {
			n, err = r.hub.Authenticate(req.Context(), a)
		}
		if err != nil {
			r.accepted <- err
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		r.accepted <- r.hub.Accept(req.Context(), conn, n)
	})
	mux.Handle("/_/nodes/hop", r.hub.HopHandler())
	r.srv.Config.Handler = mux
	r.dialer = websocket.DefaultDialer
	if o.tls {
		r.srv.StartTLS()
		r.dialer = &websocket.Dialer{TLSClientConfig: r.srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}
	} else {
		r.srv.Start()
	}
	t.Cleanup(r.srv.Close)
	return r
}

// seedNode stores a pending node and returns its key.
func seedNode(t *testing.T, store Store, id, name string, eps ...nodeproto.Endpoint) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if eps == nil {
		eps = []nodeproto.Endpoint{}
	}
	now := time.Now().UTC()
	if err := store.Create(context.Background(), &Node{ID: id, OrgID: "org_1", Name: name, PublicKey: pub,
		KeyFingerprint: nodeproto.Fingerprint(pub), Status: StatusPending, Endpoints: eps, IngressMode: "none",
		Role: "node", Version: "v1.8.0", ProtocolVersion: nodeproto.Version, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return priv
}

// fakeNode is the machine side, built from nodeproto and a yamux server.
type fakeNode struct {
	t       *testing.T
	ws      *websocket.Conn
	mux     *yamux.Session
	ctrl    net.Conn
	wmu     sync.Mutex
	msgs    chan any
	tag     string
	verdict func(nodeproto.StreamHeader) (byte, string)
}

func (r *hubRig) signed(id string, priv ed25519.PrivateKey) nodeproto.Assertion {
	r.t.Helper()
	a, err := nodeproto.Sign(priv, r.hub.Challenge(id))
	if err != nil {
		r.t.Fatal(err)
	}
	return a
}

// dial opens a WebSocket to the rig and sends the assertion.
// tryDial connects with a in the Authorization header.
func (r *hubRig) tryDial(a nodeproto.Assertion) (*websocket.Conn, *http.Response, error) {
	r.t.Helper()
	v, err := nodeproto.EncodeAuthorization(a)
	if err != nil {
		r.t.Fatal(err)
	}
	return r.dialer.Dial("ws"+strings.TrimPrefix(r.srv.URL, "http")+"/node/v1/connect", http.Header{"Authorization": []string{v}})
}

func (r *hubRig) dial(a nodeproto.Assertion) *websocket.Conn {
	r.t.Helper()
	ws, resp, err := r.tryDial(a)
	if err != nil {
		r.t.Fatal(err)
	}
	_ = resp.Body.Close()
	r.t.Cleanup(func() { _ = ws.Close() })
	return ws
}

// refused dials with a and returns the error Authenticate gave. The node is
// turned away with an HTTP 401 and never upgraded.
func (r *hubRig) refused(a nodeproto.Assertion) error {
	r.t.Helper()
	ws, resp, err := r.tryDial(a)
	if err == nil {
		_ = ws.Close()
		r.t.Fatal("a refused node was upgraded")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		r.t.Fatalf("refused with %v, %v", resp, err)
	}
	_ = resp.Body.Close()
	select {
	case err := <-r.accepted:
		if err == nil {
			r.t.Fatal("Authenticate returned nil for a refused node")
		}
		return err
	case <-time.After(3 * time.Second):
		r.t.Fatal("no verdict")
	}
	return nil
}

func (r *hubRig) connect(id string, priv ed25519.PrivateKey, tag string) *fakeNode {
	r.t.Helper()
	return r.connectWith(r.signed(id, priv), tag)
}

func (r *hubRig) connectWith(a nodeproto.Assertion, tag string) *fakeNode {
	r.t.Helper()
	n := &fakeNode{t: r.t, msgs: make(chan any, 64), tag: tag,
		verdict: func(nodeproto.StreamHeader) (byte, string) { return 0, "" }}
	n.ws = r.dial(a)
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	mux, err := yamux.Server(NewWSConn(n.ws), cfg)
	if err != nil {
		r.t.Fatal(err)
	}
	n.mux = mux
	r.t.Cleanup(func() { _ = mux.Close() })
	got := make(chan net.Conn, 1)
	go func() {
		if st, err := mux.AcceptStream(); err == nil {
			got <- st
		}
	}()
	// Another session's Accept may return meanwhile, so only the control
	// stream or a timeout ends the wait.
	select {
	case n.ctrl = <-got:
	case <-time.After(3 * time.Second):
		r.t.Fatal("no control stream: Accept refused the node")
	}
	go n.readControl()
	go n.serveStreams()
	return n
}

func (n *fakeNode) readControl() {
	sc := bufio.NewScanner(n.ctrl)
	for sc.Scan() {
		msg, err := nodeproto.DecodeControl(sc.Bytes())
		if err != nil {
			continue
		}
		n.msgs <- msg
	}
	close(n.msgs)
}

func (n *fakeNode) serveStreams() {
	for {
		st, err := n.mux.AcceptStream()
		if err != nil {
			return
		}
		go func() {
			defer st.Close()
			h, err := nodeproto.ReadHeader(st)
			if err != nil {
				return
			}
			v, reason := n.verdict(h)
			if v != 0 {
				_, _ = st.Write(append([]byte{v}, reason+"\n"...))
				return
			}
			_, _ = st.Write(append([]byte{0}, n.tag...))
			_, _ = io.Copy(st, st)
		}()
	}
}

func (n *fakeNode) send(msg any) {
	n.t.Helper()
	n.wmu.Lock()
	defer n.wmu.Unlock()
	if err := nodeproto.EncodeControl(n.ctrl, msg); err != nil {
		n.t.Fatal(err)
	}
}

// next returns the next control message from the hub.
func (n *fakeNode) next() any {
	n.t.Helper()
	select {
	case m, ok := <-n.msgs:
		if !ok {
			n.t.Fatal("control stream closed")
		}
		return m
	case <-time.After(3 * time.Second):
		n.t.Fatal("no control message")
	}
	return nil
}

// quiet asserts no control message arrives for d.
func (n *fakeNode) quiet(d time.Duration) {
	n.t.Helper()
	select {
	case m, ok := <-n.msgs:
		if ok {
			n.t.Fatalf("unexpected message %#v", m)
		}
	case <-time.After(d):
	}
}

// closedWithin asserts the hub closed the node's connection within d.
func (n *fakeNode) closedWithin(d time.Duration) {
	n.t.Helper()
	select {
	case <-n.mux.CloseChan():
	case <-time.After(d):
		n.t.Fatalf("session still open after %v", d)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *hubRig) node(id string) *Node {
	r.t.Helper()
	n, err := r.st.GetByID(context.Background(), id)
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

// echo opens a probe stream and checks it echoes behind the node's tag.
func echo(t *testing.T, h *Hub, id, tag string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := h.Open(ctx, "org_1", id, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointProbe})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(tag)+4)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != tag+"ping" {
		t.Fatalf("echo %q, want %q", buf, tag+"ping")
	}
}

func TestAcceptAndOpenProbe(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1", nodeproto.EndpointProbe)
	n := r.connect("node_1", priv, "n1:")
	if m := n.next(); !reflect.DeepEqual(m, nodeproto.Hello{Version: nodeproto.Version, Epoch: 1}) {
		t.Fatalf("first message %#v", m)
	}
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
	if got := r.node("node_1"); got.Status != StatusOnline || got.LastSeenAt == nil {
		t.Fatalf("node %+v", got)
	}
	var seen nodeproto.StreamHeader
	var mu sync.Mutex
	n.verdict = func(h nodeproto.StreamHeader) (byte, string) { mu.Lock(); seen = h; mu.Unlock(); return 0, "" }
	echo(t, r.hub, "node_1", "n1:")
	mu.Lock()
	defer mu.Unlock()
	if seen.Endpoint != nodeproto.EndpointProbe || seen.OrgID != "org_1" {
		t.Fatalf("header %+v", seen)
	}
}

func TestOpenRefused(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1")
	n := r.connect("node_1", priv, "")
	n.verdict = func(nodeproto.StreamHeader) (byte, string) { return 1, "endpoint not allowed" }
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
	_, err := r.hub.Open(context.Background(), "org_1", "node_1", nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDocker})
	if !errors.Is(err, ErrEndpointRefused) || !strings.Contains(err.Error(), "endpoint not allowed") {
		t.Fatalf("err %v", err)
	}
	// Control is never opened as a data stream, and unknown endpoints are refused here.
	for _, e := range []nodeproto.Endpoint{nodeproto.EndpointControl, "bogus"} {
		if _, err := r.hub.Open(context.Background(), "org_1", "node_1", nodeproto.StreamHeader{Endpoint: e}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", e, err)
		}
	}
}

func TestOpenRefusedReasonIsSanitized(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1")
	n := r.connect("node_1", priv, "")
	n.verdict = func(nodeproto.StreamHeader) (byte, string) { return 1, "bad \x1b[31mred\x00 \xff end" }
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
	_, err := r.hub.Open(context.Background(), "org_1", "node_1", nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDocker})
	if err == nil || !utf8.ValidString(err.Error()) || strings.ContainsAny(err.Error(), "\x1b\x00") || !strings.Contains(err.Error(), "bad [31mred  end") {
		t.Fatalf("err %q", err)
	}
}

func TestReconnectSupersedes(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1")
	first := r.connect("node_1", priv, "first:")
	if m := first.next(); m.(nodeproto.Hello).Epoch != 1 {
		t.Fatalf("first hello %#v", m)
	}
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
	// The old socket is half-open: the machine reconnects without closing it.
	second := r.connect("node_1", priv, "second:")
	if m := second.next(); m.(nodeproto.Hello).Epoch != 2 {
		t.Fatalf("second hello %#v", m)
	}
	first.closedWithin(time.Second)
	echo(t, r.hub, "node_1", "second:")
	// The old session ending leaves the new one online and recorded.
	select {
	case <-r.accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("first Accept did not return")
	}
	if got := r.node("node_1"); got.Status != StatusOnline {
		t.Fatalf("status %s", got.Status)
	}
	if s, err := r.store.Session(context.Background(), "node_1"); err != nil || s.Epoch != 2 {
		t.Fatalf("session %+v %v", s, err)
	}
}

func TestSessionEndSetsOffline(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1")
	n := r.connect("node_1", priv, "")
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
	_ = n.mux.Close()
	waitFor(t, "offline", func() bool { return !r.hub.Online("node_1") && r.node("node_1").Status == StatusOffline })
	if _, err := r.store.Session(context.Background(), "node_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session kept: %v", err)
	}
	var off ErrNodeOffline
	if _, err := r.hub.Open(context.Background(), "org_1", "node_1", nodeproto.StreamHeader{Endpoint: nodeproto.EndpointProbe}); !errors.As(err, &off) {
		t.Fatalf("err %v", err)
	}
}

func TestRevokeClosesWithinOneSecond(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1")
	n := r.connect("node_1", priv, "")
	n.next() // hello
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
	start := time.Now()
	if err := r.svc.Revoke(context.Background(), admin, "node_1"); err != nil {
		t.Fatal(err)
	}
	if m := n.next(); m != (nodeproto.Revoked{}) {
		t.Fatalf("message %#v", m)
	}
	n.closedWithin(time.Second - time.Since(start))
	waitFor(t, "session gone", func() bool { return !r.hub.Online("node_1") })
	if _, err := r.hub.Open(context.Background(), "org_1", "node_1", nodeproto.StreamHeader{Endpoint: nodeproto.EndpointProbe}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("open revoked: %v", err)
	}
}

func TestRevokedKeyCannotConnect(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1")
	if err := r.svc.Revoke(context.Background(), admin, "node_1"); err != nil {
		t.Fatal(err)
	}
	a := r.signed("node_1", priv)
	if err := r.refused(a); !errors.Is(err, ErrUnauthorized) || !errors.Is(err, ErrNodeRevoked) {
		t.Fatalf("err %v", err)
	}
	// The nonce is spent all the same, so the answer can't be replayed.
	if err := r.refused(a); errors.Is(err, ErrNodeRevoked) || !errors.Is(err, ErrNonceUsed) {
		t.Fatalf("replayed: %v", err)
	}
	// Someone without the key learns nothing about the revoke.
	_, other, _ := ed25519.GenerateKey(nil)
	if err := r.refused(r.signed("node_1", other)); !errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrNodeRevoked) {
		t.Fatalf("another key: %v", err)
	}
	if r.hub.Online("node_1") {
		t.Fatal("revoked node is online")
	}
	if _, err := r.store.Session(context.Background(), "node_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked node claimed a session: %v", err)
	}
}

func TestAcceptRefusesBadChallenges(t *testing.T) {
	store := newFakeStore()
	r := newHubRig(t, store, "a")
	priv := seedNode(t, store, "node_1", "web-1")
	otherPriv := seedNode(t, store, "node_2", "web-2")

	// A nonce is single use.
	a := r.signed("node_1", priv)
	r.connectWith(a, "")
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
	if err := r.refused(a); !errors.Is(err, ErrUnauthorized) || !errors.Is(err, ErrNonceUsed) {
		t.Fatalf("replay: %v", err)
	}

	cases := map[string]func() nodeproto.Assertion{
		// node_2's key cannot answer a challenge for node_1.
		"other key": func() nodeproto.Assertion {
			x, _ := nodeproto.Sign(otherPriv, r.hub.Challenge("node_1"))
			return x
		},
		// A nonce minted for node_2 cannot be used for node_1.
		"moved nonce": func() nodeproto.Assertion {
			c := r.hub.Challenge("node_1")
			c.Nonce = r.hub.Challenge("node_2").Nonce
			x, _ := nodeproto.Sign(priv, c)
			return x
		},
		// A nonce this hub never issued.
		"made-up nonce": func() nodeproto.Assertion {
			c := r.hub.Challenge("node_1")
			c.Nonce = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
			x, _ := nodeproto.Sign(priv, c)
			return x
		},
		// A nonce signed by another instance's challenge key.
		"foreign nonce": func() nodeproto.Assertion {
			other := NewHub(HubConfig{Audience: "https://loom.example.com", HopSecret: testHopSecret, ChallengeSecret: []byte("another-instance-challenge-key-0")})
			c := other.Challenge("node_1")
			x, _ := nodeproto.Sign(priv, c)
			return x
		},
		"wrong audience": func() nodeproto.Assertion {
			c := r.hub.Challenge("node_1")
			c.Audience = "https://evil.example.com"
			x, _ := nodeproto.Sign(priv, c)
			return x
		},
		"unknown node": func() nodeproto.Assertion {
			x, _ := nodeproto.Sign(priv, r.hub.Challenge("node_404"))
			return x
		},
	}
	for name, mk := range cases {
		if err := r.refused(mk()); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A nonce expires after 60 seconds.
	stale := r.signed("node_2", otherPriv)
	r.clock.advance(61 * time.Second)
	if err := r.refused(stale); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired: %v", err)
	}
	if r.hub.Online("node_2") {
		t.Fatal("refused node is online")
	}
}

func TestChallengeAudienceIsNormalized(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	if c := r.hub.Challenge("node_1"); c.Audience != "https://loom.example.com" || c.NodeID != "node_1" || c.Nonce == "" {
		t.Fatalf("challenge %+v", c)
	}
	if a, b := r.hub.Challenge("node_1").Nonce, r.hub.Challenge("node_1").Nonce; a == b {
		t.Fatal("nonces repeat")
	}
}

func TestBindRefusesIncompleteConfig(t *testing.T) {
	good := HubConfig{Audience: "https://loom.example.com", HopSecret: testHopSecret, ChallengeSecret: testChallengeSecret}
	if err := NewHub(good).Bind(newFakeStore(), &Service{}); err != nil {
		t.Fatal(err)
	}
	bad := []func(*HubConfig){
		func(c *HubConfig) { c.Audience = "" },
		func(c *HubConfig) { c.Audience = "loom.example.com" },
		func(c *HubConfig) { c.HopSecret = nil },
		func(c *HubConfig) { c.ChallengeSecret = nil },
		func(c *HubConfig) { c.AdvertiseURL = "not a url" },
	}
	for i, mut := range bad {
		c := good
		mut(&c)
		if err := NewHub(c).Bind(newFakeStore(), &Service{}); err == nil {
			t.Errorf("case %d: bound", i)
		}
	}
	if err := NewHub(good).Bind(nil, &Service{}); err == nil {
		t.Error("bound without a store")
	}
	if got, err := NormalizeAudience("HTTPS://Loom.Example.com:443/"); err != nil || got != "https://loom.example.com" {
		t.Fatalf("NormalizeAudience %q %v", got, err)
	}
}

func TestOfflineError(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	seedNode(t, r.store, "node_1", "web-1")
	var off ErrNodeOffline
	_, err := r.hub.Open(context.Background(), "org_1", "node_1", nodeproto.StreamHeader{Endpoint: nodeproto.EndpointProbe})
	if !errors.As(err, &off) || off.NodeID != "node_1" || off.Name != "web-1" {
		t.Fatalf("err %v", err)
	}
	// Another org sees no node at all.
	if _, err := r.hub.Open(context.Background(), "org_2", "node_1", nodeproto.StreamHeader{Endpoint: nodeproto.EndpointProbe}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other org: %v", err)
	}
}

func TestOfflineErrorNamesNode(t *testing.T) {
	err := ErrNodeOffline{NodeID: "node_1", Name: "web-1", Since: time.Date(2026, 11, 2, 9, 41, 30, 0, time.FixedZone("x", 3600))}
	if got := err.Error(); got != "node web-1 is offline since 08:41 UTC" {
		t.Fatalf("message %q", got)
	}
	r := newHubRig(t, newFakeStore(), "a")
	seedNode(t, r.store, "node_1", "web-1")
	seen := time.Date(2026, 11, 2, 7, 5, 0, 0, time.UTC)
	_ = r.store.Touch(context.Background(), "node_1", seen, nodeproto.Usage{})
	_, oerr := r.hub.Open(context.Background(), "org_1", "node_1", nodeproto.StreamHeader{Endpoint: nodeproto.EndpointProbe})
	if oerr == nil || oerr.Error() != "node web-1 is offline since 07:05 UTC" {
		t.Fatalf("err %v", oerr)
	}
}

func TestSweepMarksOffline(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	seedNode(t, r.store, "node_1", "web-1")
	seedNode(t, r.store, "node_2", "web-2")
	_ = r.store.SetStatus(context.Background(), "node_1", StatusOnline)
	_ = r.store.SetStatus(context.Background(), "node_2", StatusOnline)
	_ = r.store.Touch(context.Background(), "node_1", time.Now().Add(-time.Minute), nodeproto.Usage{})
	_ = r.store.Touch(context.Background(), "node_2", time.Now().Add(-10*time.Second), nodeproto.Usage{})
	_ = r.store.UseNonce(context.Background(), "old", time.Now().Add(-nonceSweepMargin-time.Second))
	// Just expired here, but a replica whose clock runs behind could still
	// take it, so it stays until the margin has passed.
	_ = r.store.UseNonce(context.Background(), "recent", time.Now().Add(-time.Second))
	_ = r.store.UseNonce(context.Background(), "live", time.Now().Add(time.Minute))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.hub.Sweep(ctx); close(done) }()
	waitFor(t, "sweep", func() bool { return r.node("node_1").Status == StatusOffline })
	cancel()
	<-done
	if r.node("node_2").Status != StatusOnline {
		t.Fatal("a fresh node went offline")
	}
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if _, ok := r.store.nonces["old"]; ok {
		t.Fatal("expired nonce kept")
	}
	if _, ok := r.store.nonces["live"]; !ok {
		t.Fatal("live nonce dropped")
	}
	if _, ok := r.store.nonces["recent"]; !ok {
		t.Fatal("a nonce that just expired was dropped inside the clock skew margin")
	}
}

func TestHeartbeatTouches(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1")
	n := r.connect("node_1", priv, "")
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
	r.store.mu.Lock()
	before := r.store.touches["node_1"]
	r.store.mu.Unlock()
	// The sweep marked it offline while heartbeats were late; one comes in.
	_ = r.store.SetStatus(context.Background(), "node_1", StatusOffline)
	n.send(nodeproto.Heartbeat{At: time.Now(), Usage: nodeproto.Usage{CPUPercent: 3}})
	waitFor(t, "touch", func() bool {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
		return r.store.touches["node_1"] > before
	})
	waitFor(t, "back online", func() bool { return r.node("node_1").Status == StatusOnline })
}

func TestOnOnlineFiresAfterFirstInventory(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1")
	fired := make(chan *Node, 8)
	r.hub.OnOnline(func(ctx context.Context, n *Node) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("hook has no deadline")
		}
		fired <- n
	})
	n := r.connect("node_1", priv, "")
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
	n.send(nodeproto.Heartbeat{At: time.Now()})
	select {
	case <-fired:
		t.Fatal("fired before any inventory")
	case <-time.After(100 * time.Millisecond):
	}
	n.send(nodeproto.InventoryReport{Inventory: nodeproto.Inventory{OS: "linux", CPUs: 4}})
	select {
	case got := <-fired:
		if got.Inventory.CPUs != 4 {
			t.Fatalf("fired before the inventory was stored: %+v", got.Inventory)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not fire")
	}
	n.send(nodeproto.InventoryReport{Inventory: nodeproto.Inventory{OS: "linux", CPUs: 8}})
	select {
	case <-fired:
		t.Fatal("fired twice in one session")
	case <-time.After(200 * time.Millisecond):
	}
	// A new session fires again.
	n2 := r.connect("node_1", priv, "")
	n2.send(nodeproto.InventoryReport{Inventory: nodeproto.Inventory{OS: "linux", CPUs: 8}})
	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("did not fire for the new session")
	}
}

// A node that floods its control stream is closed with a log line, and the
// message that went over the limit is not stored.
func TestHubClosesANodeThatFloodsControl(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1")
	n := r.connect("node_1", priv, "")
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
	for range controlBurst {
		n.send(nodeproto.InventoryReport{Inventory: nodeproto.Inventory{OS: "linux", Hostname: "within"}})
	}
	n.send(nodeproto.InventoryReport{Inventory: nodeproto.Inventory{OS: "linux", Hostname: "flood"}})
	n.closedWithin(3 * time.Second)
	waitFor(t, "the session to end", func() bool { return !r.hub.Online("node_1") })
	if got := r.node("node_1").Inventory.Hostname; got != "within" {
		t.Fatalf("stored hostname %q, want the last one inside the limit", got)
	}
	if !strings.Contains(r.logs.String(), "faster than allowed") {
		t.Fatalf("no log line:\n%s", r.logs.String())
	}
}

func TestControlBucketRefills(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	b := newBucket(start)
	for i := range controlBurst {
		if !b.take(start) {
			t.Fatalf("refused message %d of the burst", i)
		}
	}
	if b.take(start) {
		t.Fatal("took one past the burst")
	}
	if !b.take(start.Add(time.Second / controlPerSecond)) {
		t.Fatal("did not refill")
	}
	if b.take(start.Add(time.Second / controlPerSecond)) {
		t.Fatal("refilled more than the time allows")
	}
	// A long quiet spell refills to the burst, no further.
	later := start.Add(time.Hour)
	for range controlBurst {
		b.take(later)
	}
	if b.take(later) {
		t.Fatal("refilled past the burst")
	}
}

func TestOnInventoryFiresOnChange(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1")
	type change struct{ old, new nodeproto.Inventory }
	got := make(chan change, 8)
	r.hub.OnInventory(func(_ context.Context, _ *Node, old, cur nodeproto.Inventory) { got <- change{old, cur} })
	n := r.connect("node_1", priv, "")
	n.send(nodeproto.Hello{Version: nodeproto.Version, NodeVersion: "v1.9.0"})
	inv := nodeproto.Inventory{OS: "linux", PublicAddresses: []string{"203.0.113.9"}}
	n.send(nodeproto.InventoryReport{Inventory: inv})
	select {
	case c := <-got:
		if !reflect.DeepEqual(c.new, inv) || c.old.OS != "" {
			t.Fatalf("change %+v", c)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not fire")
	}
	if v := r.node("node_1").Version; v != "v1.9.0" {
		t.Fatalf("version %q", v)
	}
	n.send(nodeproto.InventoryReport{Inventory: inv})
	select {
	case c := <-got:
		t.Fatalf("fired without a change: %+v", c)
	case <-time.After(200 * time.Millisecond):
	}
	inv2 := inv
	inv2.PublicAddresses = []string{"203.0.113.10"}
	n.send(nodeproto.InventoryReport{Inventory: inv2})
	select {
	case c := <-got:
		if c.old.PublicAddresses[0] != "203.0.113.9" || c.new.PublicAddresses[0] != "203.0.113.10" {
			t.Fatalf("change %+v", c)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not fire on a change")
	}
}

func TestHookPanicKeepsSession(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_9", "web-1", nodeproto.EndpointProbe)
	r.hub.OnOnline(func(context.Context, *Node) { panic("adoption blew up") })
	r.hub.OnInventory(func(context.Context, *Node, nodeproto.Inventory, nodeproto.Inventory) {
		panic("address follow blew up")
	})
	n := r.connect("node_9", priv, "n:")
	n.send(nodeproto.InventoryReport{Inventory: nodeproto.Inventory{OS: "linux"}})
	waitFor(t, "panics logged", func() bool {
		l := r.logs.String()
		return strings.Contains(l, "adoption blew up") && strings.Contains(l, "address follow blew up")
	})
	if !r.hub.Online("node_9") {
		t.Fatal("a hook panic closed the session")
	}
	echo(t, r.hub, "node_9", "n:")
}

func TestInventoryDoesNotWiden(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1", nodeproto.EndpointProbe, nodeproto.EndpointDocker)
	// Narrowed to probe while the machine was away.
	if _, err := r.svc.Update(context.Background(), admin, "node_1", Update{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe}}); err != nil {
		t.Fatal(err)
	}
	n := r.connect("node_1", priv, "")
	if _, ok := n.next().(nodeproto.Hello); !ok {
		t.Fatal("hello first")
	}
	n.quiet(100 * time.Millisecond) // nothing until the first inventory
	n.send(nodeproto.InventoryReport{Inventory: nodeproto.Inventory{OS: "linux", Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe, nodeproto.EndpointDocker}}})
	if m := n.next(); !reflect.DeepEqual(m, nodeproto.Narrow{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe}}) {
		t.Fatalf("message %#v", m)
	}
	if got := r.node("node_1"); !slices.Equal(got.Endpoints, []nodeproto.Endpoint{nodeproto.EndpointProbe}) || len(got.Inventory.Endpoints) != 2 {
		t.Fatalf("node %+v", got)
	}
	// The ingress mode follows it, so a change made while the node was away lands.
	if m := n.next(); !reflect.DeepEqual(m, nodeproto.SetIngress{Mode: nodeproto.IngressNone}) {
		t.Fatalf("message %#v", m)
	}
	// Later inventories don't send them again.
	n.send(nodeproto.InventoryReport{Inventory: nodeproto.Inventory{OS: "linux"}})
	n.quiet(150 * time.Millisecond)
	// An owner narrowing to the same set still sends it (ruling R13).
	if _, err := r.svc.Update(context.Background(), admin, "node_1", Update{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe}}); err != nil {
		t.Fatal(err)
	}
	if m := n.next(); !reflect.DeepEqual(m, nodeproto.Narrow{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe}}) {
		t.Fatalf("message %#v", m)
	}
}

func TestNarrowToNothingSendsEmptyList(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1")
	n := r.connect("node_1", priv, "")
	n.next()
	n.send(nodeproto.InventoryReport{Inventory: nodeproto.Inventory{OS: "linux"}})
	m, ok := n.next().(nodeproto.Narrow)
	if !ok || m.Endpoints == nil || len(m.Endpoints) != 0 {
		t.Fatalf("narrow %#v", m)
	}
}

func TestAllowedWidens(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1", nodeproto.EndpointProbe)
	n := r.connect("node_1", priv, "")
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
	n.send(nodeproto.Allowed{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker, nodeproto.EndpointControl, "bogus"}})
	waitFor(t, "widened", func() bool {
		return slices.Equal(r.node("node_1").Endpoints, []nodeproto.Endpoint{nodeproto.EndpointProbe, nodeproto.EndpointDocker})
	})
	waitFor(t, "audit", func() bool {
		for _, e := range r.audit.list() {
			if e.action == "node.allowed" && e.actor == "node_1" {
				return true
			}
		}
		return false
	})
}

func TestLeaveRevokes(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1")
	seedNode(t, r.store, "node_2", "web-2")
	n := r.connect("node_1", priv, "")
	n.next()
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
	n.send(nodeproto.Leave{})
	if m := n.next(); m != (nodeproto.Revoked{}) {
		t.Fatalf("message %#v", m)
	}
	n.closedWithin(time.Second)
	got := r.node("node_1")
	if got.Status != StatusRevoked || got.RevokedBy != "node_1" {
		t.Fatalf("node %+v", got)
	}
	if r.node("node_2").Status == StatusRevoked {
		t.Fatal("leave revoked another node")
	}
	found := false
	for _, e := range r.audit.list() {
		found = found || (e.action == "node.left" && e.actor == "node_1")
	}
	if !found {
		t.Fatalf("audit %+v", r.audit.list())
	}
}

func TestUpdateNotifiesLiveNode(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1", nodeproto.EndpointProbe, nodeproto.EndpointDocker)
	n := r.connect("node_1", priv, "")
	n.next()
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
	mode := "relay"
	if _, err := r.svc.Update(context.Background(), admin, "node_1", Update{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}, IngressMode: &mode}); err != nil {
		t.Fatal(err)
	}
	if m := n.next(); !reflect.DeepEqual(m, nodeproto.Narrow{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}}) {
		t.Fatalf("first %#v", m)
	}
	if m := n.next(); m != (nodeproto.SetIngress{Mode: "relay"}) {
		t.Fatalf("second %#v", m)
	}
}

func TestRevokeDuringAcceptClosesSession(t *testing.T) {
	store := newFakeStore()
	r := newHubRig(t, store, "a")
	priv := seedNode(t, store, "node_1", "web-1")
	// The revoke commits after the claim and before the session registers,
	// so its Notify finds no session anywhere.
	var once sync.Once
	store.afterClaim = func(id string) {
		once.Do(func() {
			if err := r.svc.Revoke(context.Background(), admin, id); err != nil {
				t.Error(err)
			}
		})
	}
	start := time.Now()
	ws := r.dial(r.signed("node_1", priv))
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	mux, err := yamux.Server(NewWSConn(ws), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer mux.Close()
	// The session lives a few milliseconds, so the control stream may or may
	// not be accepted here first; what matters is that it ends.
	select {
	case <-mux.CloseChan():
	case <-time.After(time.Second - time.Since(start)):
		t.Fatal("a session revoked during Accept is still open after 1s")
	}
	select {
	case err := <-r.accepted:
		if err != nil {
			t.Fatalf("Accept refused instead of closing: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Accept did not return")
	}
	if r.hub.Online("node_1") {
		t.Fatal("revoked node still online here")
	}
}

func TestOnInventoryIgnoresNilVersusEmpty(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1")
	// The stored copy has empty slices, as a write from Go can leave it.
	// The report decodes from the wire with nil ones. Same inventory.
	r.store.mu.Lock()
	r.store.nodes["node_1"].Inventory = nodeproto.Inventory{OS: "linux", GPUs: []nodeproto.GPU{}, Endpoints: []nodeproto.Endpoint{}, PublicAddresses: []string{}}
	r.store.mu.Unlock()
	fired := make(chan struct{}, 8)
	r.hub.OnInventory(func(context.Context, *Node, nodeproto.Inventory, nodeproto.Inventory) { fired <- struct{}{} })
	online := make(chan struct{}, 1)
	r.hub.OnOnline(func(context.Context, *Node) { online <- struct{}{} })
	n := r.connect("node_1", priv, "")
	n.send(nodeproto.InventoryReport{Inventory: nodeproto.Inventory{OS: "linux"}})
	select {
	case <-online:
	case <-time.After(3 * time.Second):
		t.Fatal("inventory not handled")
	}
	select {
	case <-fired:
		t.Fatal("fired for nil versus empty")
	case <-time.After(200 * time.Millisecond):
	}
	n.send(nodeproto.InventoryReport{Inventory: nodeproto.Inventory{OS: "darwin"}})
	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("did not fire on a real change")
	}
}

func TestAllowedIsAcknowledgedWithNarrow(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1", nodeproto.EndpointProbe)
	n := r.connect("node_1", priv, "")
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
	n.next() // hello
	n.send(nodeproto.Allowed{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}})
	want := nodeproto.Narrow{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe, nodeproto.EndpointDocker}, Ack: []nodeproto.Endpoint{nodeproto.EndpointDocker}}
	if m := n.next(); !reflect.DeepEqual(m, want) {
		t.Fatalf("message %#v", m)
	}
	// Allowing what is already allowed is acknowledged too.
	n.send(nodeproto.Allowed{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}})
	if m := n.next(); !reflect.DeepEqual(m, want) {
		t.Fatalf("message %#v", m)
	}
}

func TestAllowedRefusalIsAcknowledged(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1", nodeproto.EndpointProbe)
	n := r.connect("node_1", priv, "")
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
	n.next() // hello
	n.send(nodeproto.Allowed{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointControl, "bogus"}})
	want := nodeproto.Narrow{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe}, Ack: []nodeproto.Endpoint{nodeproto.EndpointControl, "bogus"}}
	if m := n.next(); !reflect.DeepEqual(m, want) {
		t.Fatalf("message %#v", m)
	}
	// A mix: the taken one is stored, all are acknowledged.
	n.send(nodeproto.Allowed{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker, "bogus"}})
	want = nodeproto.Narrow{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe, nodeproto.EndpointDocker}, Ack: []nodeproto.Endpoint{nodeproto.EndpointDocker, "bogus"}}
	if m := n.next(); !reflect.DeepEqual(m, want) {
		t.Fatalf("message %#v", m)
	}
}

func TestAllowedTransientErrorSendsNoAck(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1", nodeproto.EndpointProbe)
	n := r.connect("node_1", priv, "")
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
	n.next() // hello
	r.store.mu.Lock()
	r.store.allowErr = errors.New("database is down")
	r.store.mu.Unlock()
	n.send(nodeproto.Allowed{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}})
	n.quiet(200 * time.Millisecond)
	r.store.mu.Lock()
	r.store.allowErr = nil
	r.store.mu.Unlock()
	n.send(nodeproto.Allowed{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}})
	if m, ok := n.next().(nodeproto.Narrow); !ok || len(m.Ack) != 1 {
		t.Fatalf("message %#v", m)
	}
}

func TestFirstInventorySendsIngressMode(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1", nodeproto.EndpointProbe)
	mode := nodeproto.IngressRelay
	if _, err := r.svc.Update(context.Background(), admin, "node_1", Update{IngressMode: &mode}); err != nil {
		t.Fatal(err)
	}
	n := r.connect("node_1", priv, "")
	n.next() // hello
	n.send(nodeproto.InventoryReport{Inventory: nodeproto.Inventory{OS: "linux"}})
	n.next() // narrow
	if m := n.next(); !reflect.DeepEqual(m, nodeproto.SetIngress{Mode: nodeproto.IngressRelay}) {
		t.Fatalf("message %#v", m)
	}
}

func TestAllowedAuditsOnlyAChange(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1", nodeproto.EndpointProbe)
	n := r.connect("node_1", priv, "")
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
	allowed := func() int {
		c := 0
		for _, e := range r.audit.list() {
			if e.action == "node.allowed" {
				c++
			}
		}
		return c
	}
	n.send(nodeproto.Allowed{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe}})
	n.send(nodeproto.Allowed{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}})
	waitFor(t, "audit", func() bool { return allowed() >= 1 })
	n.send(nodeproto.Allowed{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}})
	n.send(nodeproto.Heartbeat{At: time.Now()}) // read in order, so the allow above is done after it
	r.store.mu.Lock()
	before := r.store.touches["node_1"]
	r.store.mu.Unlock()
	waitFor(t, "heartbeat", func() bool {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
		return r.store.touches["node_1"] > before
	})
	if c := allowed(); c != 1 {
		t.Fatalf("node.allowed recorded %d times, want 1", c)
	}
}

func TestSilentSessionIsClosed(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1")
	n := r.connect("node_1", priv, "")
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
	sweep := func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r.hub.Sweep(ctx) // one pass
	}
	r.clock.advance(30 * time.Second)
	sweep()
	if !r.hub.Online("node_1") {
		t.Fatal("closed a session inside the window")
	}
	n.send(nodeproto.Heartbeat{At: time.Now()})
	waitFor(t, "heartbeat seen", func() bool {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
		return r.store.touches["node_1"] >= 2
	})
	r.clock.advance(40 * time.Second) // 40 s since that heartbeat
	sweep()
	if !r.hub.Online("node_1") {
		t.Fatal("a heartbeat did not reset the window")
	}
	r.clock.advance(6 * time.Second)
	sweep()
	n.closedWithin(time.Second)
	if m, ok := <-n.msgs; ok && m != (nodeproto.Hello{Version: nodeproto.Version, Epoch: 1}) {
		t.Fatalf("a silent session was sent %#v", m)
	}
}

// A node that leaves must always learn it was revoked: either it reads
// Revoked, or the WebSocket closes with the unauthorized code. The hub may
// not close the connection under the write.
func TestLeaveAlwaysTellsTheNode(t *testing.T) {
	for i := range 40 {
		r := newHubRig(t, newFakeStore(), "a")
		priv := seedNode(t, r.store, "node_1", "web-1")
		n := r.connect("node_1", priv, "")
		n.next()
		waitFor(t, "online", func() bool { return r.hub.Online("node_1") })
		n.send(nodeproto.Leave{})
		select {
		case m, ok := <-n.msgs:
			if !ok {
				t.Fatalf("round %d: the control stream closed before Revoked arrived", i)
			}
			if m != (nodeproto.Revoked{}) {
				t.Fatalf("round %d: message %#v", i, m)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("round %d: neither Revoked nor a close", i)
		}
	}
}

// A node can't store more than the bounds through its hello or inventory.
func TestHubBoundsWhatANodeReports(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1", nodeproto.EndpointProbe)
	n := r.connect("node_1", priv, "a:")
	_ = n.next() // hello
	n.send(nodeproto.Hello{Version: nodeproto.Version, NodeVersion: strings.Repeat("9", 1000)})
	n.send(nodeproto.InventoryReport{Inventory: nodeproto.Inventory{OS: "linux", Hostname: strings.Repeat("h", 10000)}})
	waitFor(t, "the inventory", func() bool { return r.node("node_1").Inventory.OS == "linux" })
	got := r.node("node_1")
	if len(got.Version) > nodeproto.MaxVersionLen || len(got.Inventory.Hostname) > nodeproto.MaxFieldLen {
		t.Fatalf("stored a %d byte version and a %d byte hostname", len(got.Version), len(got.Inventory.Hostname))
	}
}

// Replicas sharing a database with no advertise URL can't reach each
// other's nodes. The hub says so at startup, and warns once when it meets a
// session it can't hop to.
func TestHubWarnsWhenAReplicaCannotBeReached(t *testing.T) {
	store := newFakeStore()
	var logs syncLog
	h := NewHub(HubConfig{Audience: "https://lp.example.com", Replica: "b", HopSecret: []byte("hop-secret-for-tests-only-000000"),
		ChallengeSecret: []byte("challenge-secret-for-tests-00000"), Log: slog.New(slog.NewTextHandler(&logs, nil))})
	if err := h.Bind(store, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "nodes.advertise_url is not set") {
		t.Fatalf("no startup note:\n%s", logs.String())
	}
	seedNode(t, store, "node_1", "web-1", nodeproto.EndpointProbe)
	if _, err := store.ClaimSession(context.Background(), Session{NodeID: "node_1", Replica: "replica:a", ConnectedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		var off ErrNodeOffline
		if _, err := h.Open(context.Background(), "org_1", "node_1", nodeproto.StreamHeader{Endpoint: nodeproto.EndpointProbe}); !errors.As(err, &off) {
			t.Fatalf("open: %v", err)
		}
	}
	if got := strings.Count(logs.String(), "has no nodes.advertise_url"); got != 1 {
		t.Fatalf("warned %d times:\n%s", got, logs.String())
	}
}

// After a restart on a single replica, a node's session row still names the
// dead process until the node reconnects. That row was written before this
// hub started, so it is no sign of a replica that can't be reached.
func TestHubDoesNotWarnAboutItsOwnPredecessor(t *testing.T) {
	store := newFakeStore()
	var logs syncLog
	seedNode(t, store, "node_1", "web-1", nodeproto.EndpointProbe)
	if _, err := store.ClaimSession(context.Background(), Session{NodeID: "node_1", Replica: "replica:host-1:4242", ConnectedAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	h := NewHub(HubConfig{Audience: "https://lp.example.com", Replica: "host-1:4343", HopSecret: []byte("hop-secret-for-tests-only-000000"),
		ChallengeSecret: []byte("challenge-secret-for-tests-00000"), Log: slog.New(slog.NewTextHandler(&logs, nil))})
	if err := h.Bind(store, nil); err != nil {
		t.Fatal(err)
	}
	var off ErrNodeOffline
	if _, err := h.Open(context.Background(), "org_1", "node_1", nodeproto.StreamHeader{Endpoint: nodeproto.EndpointProbe}); !errors.As(err, &off) {
		t.Fatalf("open: %v", err)
	}
	if strings.Contains(logs.String(), "has no nodes.advertise_url") {
		t.Fatalf("warned about the process this one replaced:\n%s", logs.String())
	}
}

// A live replica on another host is no predecessor, however long ago its
// sessions connected: in a rolling deploy they all predate the new process.
func TestHubWarnsAboutAnOlderReplicaOnAnotherHost(t *testing.T) {
	store := newFakeStore()
	var logs syncLog
	seedNode(t, store, "node_1", "web-1", nodeproto.EndpointProbe)
	if _, err := store.ClaimSession(context.Background(), Session{NodeID: "node_1", Replica: "replica:host-2:4242", ConnectedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	h := NewHub(HubConfig{Audience: "https://lp.example.com", Replica: "host-1:4343", HopSecret: []byte("hop-secret-for-tests-only-000000"),
		ChallengeSecret: []byte("challenge-secret-for-tests-00000"), Log: slog.New(slog.NewTextHandler(&logs, nil))})
	if err := h.Bind(store, nil); err != nil {
		t.Fatal(err)
	}
	var off ErrNodeOffline
	if _, err := h.Open(context.Background(), "org_1", "node_1", nodeproto.StreamHeader{Endpoint: nodeproto.EndpointProbe}); !errors.As(err, &off) {
		t.Fatalf("open: %v", err)
	}
	if !strings.Contains(logs.String(), "has no nodes.advertise_url") {
		t.Fatalf("no warning about the replica on host-2:\n%s", logs.String())
	}
}

func TestReplicaHost(t *testing.T) {
	for id, want := range map[string]string{
		"replica:host-1:4242":        "host-1",
		"replica:10.0.0.7:80":        "10.0.0.7",
		"replica:a":                  "a",
		"replica:[fe80::1%eth0]:999": "[fe80::1%eth0]",
	} {
		if got := replicaHost(id); got != want {
			t.Errorf("replicaHost(%q) = %q, want %q", id, got, want)
		}
	}
}

type syncLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
