package nodes

import (
	"bufio"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

func wsURL(base string) string { return "ws" + strings.TrimPrefix(base, "http") + "/node/v1/connect" }

// challenge asks the server for a challenge the way a node does.
func challenge(t *testing.T, client *http.Client, base, node string) nodeproto.Challenge {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/node/v1/challenge?node="+url.QueryEscape(node), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var c nodeproto.Challenge
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&c) != nil {
		t.Fatalf("challenge: %d", resp.StatusCode)
	}
	return c
}

// status sends a bodiless request and returns the status.
func status(t *testing.T, client *http.Client, method, target string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func serve(t *testing.T, r *rig) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(r.router)
	t.Cleanup(srv.Close)
	return srv
}

func TestChallengeRoute(t *testing.T) {
	r := newRig(t)
	srv := serve(t, r)
	c := challenge(t, srv.Client(), srv.URL, "node_1")
	if c.NodeID != "node_1" || c.Audience != publicURL || c.Nonce == "" {
		t.Fatalf("challenge %+v", c)
	}
	for _, path := range []string{"/node/v1/challenge", "/node/v1/challenge?node=" + strings.Repeat("a", 300)} {
		if code := status(t, srv.Client(), http.MethodGet, srv.URL+path); code != http.StatusBadRequest {
			t.Errorf("%s: %d", path, code)
		}
	}
	if code := status(t, srv.Client(), http.MethodPost, srv.URL+"/node/v1/challenge?node=node_1"); code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", code)
	}
}

// dialWith connects with an Authorization header value ("" sends none).
func dialWith(base, authorization string) (*websocket.Conn, *http.Response, error) {
	h := http.Header{}
	if authorization != "" {
		h.Set("Authorization", authorization)
	}
	return websocket.DefaultDialer.Dial(wsURL(base), h)
}

// A refused node gets a literal HTTP 401 and is never upgraded.
func TestConnectBadAssertionIs401(t *testing.T) {
	r := newRig(t)
	priv := r.seed(t, nodeproto.EndpointProbe)
	srv := serve(t, r)

	sign := func(c nodeproto.Challenge, key ed25519.PrivateKey) string {
		a, err := nodeproto.Sign(key, c)
		if err != nil {
			t.Fatal(err)
		}
		v, err := nodeproto.EncodeAuthorization(a)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	_, other, _ := ed25519.GenerateKey(nil)
	for name, v := range map[string]string{
		"another key":   sign(challenge(t, srv.Client(), srv.URL, "node_1"), other),
		"unknown node":  sign(challenge(t, srv.Client(), srv.URL, "node_404"), priv),
		"made-up nonce": sign(nodeproto.Challenge{NodeID: "node_1", Audience: publicURL, Nonce: "AAAA", IssuedAt: time.Now()}, priv),
		"no header":     "",
		"other scheme":  "Bearer abc",
		"not base64":    nodeproto.AuthScheme + " !!!",
		"not json":      nodeproto.AuthScheme + " aGVsbG8",
		"too long":      nodeproto.AuthScheme + " " + strings.Repeat("A", nodeproto.MaxAuthorizationSize),
	} {
		t.Run(name, func(t *testing.T) {
			ws, resp, err := dialWith(srv.URL, v)
			if err == nil {
				_ = ws.Close()
				t.Fatal("the connection was upgraded")
			}
			if resp == nil || resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != nodeproto.AuthScheme {
				t.Fatalf("response %v, %v", resp, err)
			}
			_ = resp.Body.Close()
		})
	}
	if got := r.store.status("node_1"); got != nodes.StatusPending {
		t.Fatalf("a refused node became %s", got)
	}
}

func TestConnectRefusesAReplayedAssertion(t *testing.T) {
	r := newRig(t)
	priv := r.seed(t, nodeproto.EndpointProbe)
	srv := serve(t, r)
	a, err := nodeproto.Sign(priv, challenge(t, srv.Client(), srv.URL, "node_1"))
	if err != nil {
		t.Fatal(err)
	}
	v, _ := nodeproto.EncodeAuthorization(a)
	ws, resp, err := dialWith(srv.URL, v)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	defer func() { _ = ws.Close() }()
	if _, resp, err = dialWith(srv.URL, v); err == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replay: %v %v", resp, err)
	}
	_ = resp.Body.Close()
}

// A request that is not a WebSocket upgrade is answered before the
// assertion is checked, so it cannot spend a challenge.
func TestConnectPlainRequestKeepsTheChallenge(t *testing.T) {
	r := newRig(t)
	priv := r.seed(t, nodeproto.EndpointProbe)
	srv := serve(t, r)
	a, _ := nodeproto.Sign(priv, challenge(t, srv.Client(), srv.URL, "node_1"))
	v, _ := nodeproto.EncodeAuthorization(a)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/node/v1/connect", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", v)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("plain GET: %d", resp.StatusCode)
	}
	ws, resp, err := dialWith(srv.URL, v)
	if err != nil {
		t.Fatalf("the challenge was spent: %v", err)
	}
	_ = resp.Body.Close()
	_ = ws.Close()
}

func TestConnectRefusesABrowserOrigin(t *testing.T) {
	srv := serve(t, newRig(t))
	h := http.Header{"Origin": []string{"https://evil.example"}}
	_, resp, err := websocket.DefaultDialer.Dial(wsURL(srv.URL), h)
	if err == nil {
		t.Fatal("a browser origin connected")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("response %v, %v", resp, err)
	}
	_ = resp.Body.Close()
}

// openSession connects as node through base with client, and returns the
// first control message the hub sends.
func openSession(t *testing.T, dialer *websocket.Dialer, client *http.Client, base, node string, priv ed25519.PrivateKey) nodeproto.Hello {
	t.Helper()
	a, err := nodeproto.Sign(priv, challenge(t, client, base, node))
	if err != nil {
		t.Fatal(err)
	}
	v, err := nodeproto.EncodeAuthorization(a)
	if err != nil {
		t.Fatal(err)
	}
	ws, resp, err := dialer.Dial(wsURL(base), http.Header{"Authorization": []string{v}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	t.Cleanup(func() { _ = ws.Close() })
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	mux, err := yamux.Server(nodes.NewWSConn(ws), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mux.Close() })
	ctrl, err := mux.AcceptStream()
	if err != nil {
		t.Fatalf("no control stream: %v", err)
	}
	sc := bufio.NewScanner(ctrl)
	if !sc.Scan() {
		t.Fatalf("no hello: %v", sc.Err())
	}
	msg, err := nodeproto.DecodeControl(sc.Bytes())
	hello, ok := msg.(nodeproto.Hello)
	if err != nil || !ok {
		t.Fatalf("first message %#v (%v)", msg, err)
	}
	return hello
}

func TestConnectAcceptsASignedAssertion(t *testing.T) {
	r := newRig(t)
	priv := r.seed(t, nodeproto.EndpointProbe)
	srv := serve(t, r)
	if h := openSession(t, websocket.DefaultDialer, srv.Client(), srv.URL, "node_1", priv); h.Epoch != 1 || h.Version != nodeproto.Version {
		t.Fatalf("hello %+v", h)
	}
	waitFor(t, "online", func() bool { return r.ext.hub.Online("node_1") && r.store.status("node_1") == nodes.StatusOnline })
}

// A TLS-terminating proxy hands the plain server an http request. The node
// still authenticates, because the audience is Loomplane's configured public
// URL and no client certificate is involved.
func TestConnectThroughTLSTerminatingProxy(t *testing.T) {
	r := newRig(t)
	priv := r.seed(t, nodeproto.EndpointProbe)
	backend := serve(t, r)
	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
	t.Cleanup(proxy.Close)
	pool := x509.NewCertPool()
	pool.AddCert(proxy.Certificate())
	tlsCfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	dialer := &websocket.Dialer{TLSClientConfig: tlsCfg}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}

	if h := openSession(t, dialer, client, proxy.URL, "node_1", priv); h.Epoch != 1 {
		t.Fatalf("hello %+v", h)
	}
	waitFor(t, "online through the proxy", func() bool { return r.ext.hub.Online("node_1") })
}

func TestHopRouteIsMountedAndAuthenticated(t *testing.T) {
	srv := serve(t, newRig(t))
	if code := status(t, srv.Client(), http.MethodGet, srv.URL+"/_/nodes/hop"); code != http.StatusUnauthorized {
		t.Fatalf("an unsigned hop answered %d", code)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
