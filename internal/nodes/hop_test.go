package nodes

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

func TestHopForwards(t *testing.T) {
	store := newFakeStore()
	a := newHubRig(t, store, "a")
	b := newHubRig(t, store, "b")
	priv := seedNode(t, store, "node_1", "web-1", nodeproto.EndpointProbe, nodeproto.EndpointDocker)
	n := a.connect("node_1", priv, "via-a:")
	n.next() // hello
	waitFor(t, "online on a", func() bool { return a.hub.Online("node_1") })
	if b.hub.Online("node_1") {
		t.Fatal("b claims a's session")
	}

	echo(t, b.hub, "node_1", "via-a:")

	n.verdict = func(h nodeproto.StreamHeader) (byte, string) {
		if h.Endpoint == nodeproto.EndpointDocker {
			return 1, "endpoint not allowed"
		}
		return 0, ""
	}
	_, err := b.hub.Open(context.Background(), "org_1", "node_1", nodeproto.StreamHeader{Endpoint: nodeproto.EndpointDocker})
	if !errors.Is(err, ErrEndpointRefused) || !strings.HasSuffix(err.Error(), ": endpoint not allowed") {
		t.Fatalf("refused through hop: %v", err)
	}
	if _, err := b.hub.Open(context.Background(), "org_2", "node_1", nodeproto.StreamHeader{Endpoint: nodeproto.EndpointProbe}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other org through hop: %v", err)
	}

	// A narrow made on b reaches the node held by a.
	if _, err := b.svc.Update(context.Background(), admin, "node_1", Update{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe}}); err != nil {
		t.Fatal(err)
	}
	if m := n.next(); !reflect.DeepEqual(m, nodeproto.Narrow{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe}}) {
		t.Fatalf("narrow through hop: %#v", m)
	}

	// So does a revoke, within a second.
	start := time.Now()
	if err := b.svc.Revoke(context.Background(), admin, "node_1"); err != nil {
		t.Fatal(err)
	}
	if m := n.next(); m != (nodeproto.Revoked{}) {
		t.Fatalf("revoke through hop: %#v", m)
	}
	n.closedWithin(time.Second - time.Since(start))
}

func TestHopToReplicaWithoutSessionIsOffline(t *testing.T) {
	store := newFakeStore()
	a := newHubRig(t, store, "a")
	b := newHubRig(t, store, "b")
	seedNode(t, store, "node_1", "web-1")
	// The row names a, but a's connection is gone.
	if _, err := store.ClaimSession(context.Background(), Session{NodeID: "node_1", Replica: a.hub.self, ConnectedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	var off ErrNodeOffline
	if _, err := b.hub.Open(context.Background(), "org_1", "node_1", nodeproto.StreamHeader{Endpoint: nodeproto.EndpointProbe}); !errors.As(err, &off) || off.Name != "web-1" {
		t.Fatalf("err %v", err)
	}
}

// hopCall is one request to a rig's hop handler, signed unless tampered with.
type hopCall struct {
	secret  []byte
	replica string
	node    string
	org     string
	kind    string
	ts      time.Time
	payload []byte
	mac     string // overrides the computed MAC when set
}

func (r *hubRig) hopStatus(c hopCall) int {
	r.t.Helper()
	if c.secret == nil {
		c.secret = testHopSecret
	}
	if c.replica == "" {
		c.replica = r.hub.self
	}
	if c.kind == "" {
		c.kind = hopKindOpen
	}
	ts := strconv.FormatInt(c.ts.Unix(), 10)
	mac := c.mac
	if mac == "" {
		mac = hex.EncodeToString(hopMAC(c.secret, c.replica, c.node, c.org, ts, c.payload))
	}
	req, err := http.NewRequestWithContext(r.t.Context(), http.MethodGet, r.srv.URL+HopPath, http.NoBody)
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", hopUpgrade)
	req.Header.Set(hdrHopNode, c.node)
	req.Header.Set(hdrHopOrg, c.org)
	req.Header.Set(hdrHopKind, c.kind)
	req.Header.Set(hdrHopTimestamp, ts)
	req.Header.Set(hdrHopPayload, base64.RawURLEncoding.EncodeToString(c.payload))
	req.Header.Set(hdrHopAuth, mac)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func probeHeader(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(nodeproto.StreamHeader{Endpoint: nodeproto.EndpointProbe, OrgID: "org_1"})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHopRejectsBadMAC(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	seedNode(t, r.store, "node_1", "web-1")
	hdr := probeHeader(t)
	good := hopCall{node: "node_1", org: "org_1", ts: time.Now(), payload: hdr}
	// A signed request for a node with no session gets past auth to 503.
	if got := r.hopStatus(good); got != http.StatusServiceUnavailable {
		t.Fatalf("good request: %d", got)
	}
	bad := map[string]func(c *hopCall){
		"wrong secret":      func(c *hopCall) { c.secret = []byte("not-the-hop-secret-not-the-hop-s") },
		"another replica":   func(c *hopCall) { c.replica = "http://10.0.0.9:8080" },
		"garbage mac":       func(c *hopCall) { c.mac = "zz" },
		"empty mac":         func(c *hopCall) { c.mac = "00" },
		"no node":           func(c *hopCall) { c.node = "" },
		"notify kind, open": func(c *hopCall) { c.kind = hopKindNotify },
	}
	for name, mut := range bad {
		c := good
		mut(&c)
		if got := r.hopStatus(c); got != http.StatusUnauthorized && got != http.StatusBadRequest {
			t.Errorf("%s: %d", name, got)
		}
	}
	// Signed for one node or header, sent with another.
	c := good
	c.mac = hex.EncodeToString(hopMAC(testHopSecret, r.hub.self, "node_2", "org_1", strconv.FormatInt(c.ts.Unix(), 10), hdr))
	if got := r.hopStatus(c); got != http.StatusUnauthorized {
		t.Fatalf("other node's mac: %d", got)
	}
	c = good
	c.mac = hex.EncodeToString(hopMAC(testHopSecret, r.hub.self, "node_1", "org_1", strconv.FormatInt(c.ts.Unix(), 10), bytes.Replace(hdr, []byte("probe"), []byte("docker"), 1)))
	if got := r.hopStatus(c); got != http.StatusUnauthorized {
		t.Fatalf("other header's mac: %d", got)
	}
	// A header whose org disagrees with the signed org is refused.
	c = good
	c.org = "org_2"
	if got := r.hopStatus(c); got == http.StatusSwitchingProtocols || got == http.StatusServiceUnavailable {
		t.Fatalf("org mismatch: %d", got)
	}
	// A notify can only carry a message the control plane sends.
	hello := new(bytes.Buffer)
	_ = nodeproto.EncodeControl(hello, nodeproto.Hello{Version: 1})
	c = hopCall{node: "node_1", kind: hopKindNotify, ts: time.Now(), payload: append([]byte(hopNotifyPrefix), hello.Bytes()...)}
	if got := r.hopStatus(c); got != http.StatusBadRequest {
		t.Fatalf("notify hello: %d", got)
	}
}

func TestHopRejectsOld(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	seedNode(t, r.store, "node_1", "web-1")
	hdr := probeHeader(t)
	for _, d := range []time.Duration{-31 * time.Second, 31 * time.Second, -time.Hour} {
		if got := r.hopStatus(hopCall{node: "node_1", org: "org_1", ts: time.Now().Add(d), payload: hdr}); got != http.StatusUnauthorized {
			t.Errorf("%v: %d", d, got)
		}
	}
	// Inside the window it gets through to the session lookup.
	if got := r.hopStatus(hopCall{node: "node_1", org: "org_1", ts: time.Now().Add(-25 * time.Second), payload: hdr}); got != http.StatusServiceUnavailable {
		t.Fatalf("fresh: %d", got)
	}
}

func TestHopRejectsReplay(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	seedNode(t, r.store, "node_1", "web-1")
	c := hopCall{node: "node_1", org: "org_1", ts: time.Now(), payload: probeHeader(t)}
	if got := r.hopStatus(c); got != http.StatusServiceUnavailable {
		t.Fatalf("first: %d", got)
	}
	if got := r.hopStatus(c); got != http.StatusUnauthorized {
		t.Fatalf("replay: %d", got)
	}
}
