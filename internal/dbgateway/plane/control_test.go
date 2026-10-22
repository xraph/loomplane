package plane

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// requestMessage is the documented message a request signature covers, built
// here from scratch so the test doesn't lean on SignRequest.
func requestMessage(method, uri, timestamp string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte("loomplane/dbgateway/request/v1\x00" + method + "\n" + uri + "\n" + timestamp + "\n" + hex.EncodeToString(sum[:]))
}

// checkSignedRequest reports why req was not signed by pub as gateway id, or
// "" when it was.
func checkSignedRequest(req *http.Request, body []byte, pub ed25519.PublicKey, id string, now time.Time) string {
	if got := req.Header.Get("Loomplane-Gateway"); got != id {
		return "Loomplane-Gateway = " + got
	}
	ts := req.Header.Get("Loomplane-Timestamp")
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return "Loomplane-Timestamp is not unix seconds: " + ts
	}
	if d := now.Sub(time.Unix(n, 0)); d > time.Minute || d < -time.Minute {
		return "Loomplane-Timestamp is far from now: " + ts
	}
	sig, err := base64.StdEncoding.DecodeString(req.Header.Get("Loomplane-Signature"))
	if err != nil {
		return "Loomplane-Signature is not base64"
	}
	if !ed25519.Verify(pub, requestMessage(req.Method, req.RequestURI, ts, body), sig) {
		return "signature does not verify"
	}
	return ""
}

func TestSignRequestVerifies(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 22, 9, 30, 0, 0, time.UTC)
	body := []byte(`{"label":"orders--dev"}`)
	req, err := http.NewRequest(http.MethodPost, "https://control.example.com/api/v1/db-gateways/gw-1/hold?x=1", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	SignRequest(req, body, "gw-1", priv, now)

	if got := req.Header.Get("Loomplane-Gateway"); got != "gw-1" {
		t.Fatalf("Loomplane-Gateway = %q", got)
	}
	ts := req.Header.Get("Loomplane-Timestamp")
	if ts != strconv.FormatInt(now.Unix(), 10) {
		t.Fatalf("Loomplane-Timestamp = %q, want %d", ts, now.Unix())
	}
	sig, err := base64.StdEncoding.DecodeString(req.Header.Get("Loomplane-Signature"))
	if err != nil {
		t.Fatalf("Loomplane-Signature is not base64: %v", err)
	}
	// The whole request URI is signed: the path and the query.
	const uri = "/api/v1/db-gateways/gw-1/hold?x=1"
	msg := requestMessage("POST", uri, ts, body)
	if !ed25519.Verify(pub, msg, sig) {
		t.Fatal("signature does not verify over the documented message")
	}
	for name, other := range map[string][]byte{
		"another body":   requestMessage("POST", uri, ts, []byte("{}")),
		"another method": requestMessage("GET", uri, ts, body),
		"another path":   requestMessage("POST", "/api/v1/db-gateways/gw-2/hold?x=1", ts, body),
		"another query":  requestMessage("POST", "/api/v1/db-gateways/gw-1/hold?x=2", ts, body),
		"query dropped":  requestMessage("POST", "/api/v1/db-gateways/gw-1/hold", ts, body),
		"another time":   requestMessage("POST", uri, "1", body),
	} {
		if ed25519.Verify(pub, other, sig) {
			t.Errorf("signature also verifies over %s", name)
		}
	}
}

// fakeControl is a control plane that checks every request's signature and
// records what the gateway sent.
type fakeControl struct {
	t     *testing.T
	srv   *httptest.Server
	pub   ed25519.PublicKey
	gwID  string
	trust TrustKey
	priv  ed25519.PrivateKey // signs snapshots

	mu      sync.Mutex
	polls   []uint64
	reports []Report
	holds   []string
	live    int
	// snapshot answers a poll after version N; nil answers 204.
	snapshot func(after uint64) *Snapshot
	// holdStatus is the status Hold gets; 0 means 204.
	holdStatus int
}

func newFakeControl(t *testing.T, gwID string, gwPub ed25519.PublicKey) *fakeControl {
	t.Helper()
	trust, priv := newCurrentTrustKey(t)
	c := &fakeControl{t: t, pub: gwPub, gwID: gwID, trust: trust, priv: priv, live: 1}
	c.srv = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *fakeControl) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	if why := checkSignedRequest(req, body, c.pub, c.gwID, time.Now()); why != "" {
		c.t.Errorf("%s %s: %s", req.Method, req.URL.Path, why)
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	prefix := "/api/v1/db-gateways/" + c.gwID + "/"
	leaf, ok := strings.CutPrefix(req.URL.Path, prefix)
	if !ok {
		http.NotFound(w, req)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case leaf == "snapshot" && req.Method == http.MethodGet:
		after, _ := strconv.ParseUint(req.URL.Query().Get("after"), 10, 64)
		c.polls = append(c.polls, after)
		if c.snapshot == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s := c.snapshot(after)
		if s == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode(s)
	case leaf == "report" && req.Method == http.MethodPost:
		var r Report
		if err := json.Unmarshal(body, &r); err != nil {
			c.t.Errorf("report body: %v", err)
		}
		c.reports = append(c.reports, r)
		_ = json.NewEncoder(w).Encode(map[string]int{"live_instances": c.live})
	case leaf == "hold" && req.Method == http.MethodPost:
		var b struct {
			Label string `json:"label"`
		}
		if err := json.Unmarshal(body, &b); err != nil {
			c.t.Errorf("hold body: %v", err)
		}
		c.holds = append(c.holds, b.Label)
		if c.holdStatus != 0 {
			w.WriteHeader(c.holdStatus)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, req)
	}
}

func (c *fakeControl) pollCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.polls)
}

func (c *fakeControl) lastReport() (Report, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reports) == 0 {
		return Report{}, false
	}
	return c.reports[len(c.reports)-1], true
}

// newCurrentTrustKey is newTrustKey with a window around the real clock,
// because Run verifies snapshots against time.Now.
func newCurrentTrustKey(t *testing.T) (TrustKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return TrustKey{PublicKey: pub, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}, priv
}

func newClient(t *testing.T) (*ControlClient, *fakeControl) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ctrl := newFakeControl(t, "gw-1", pub)
	return NewControlClient(ctrl.srv.URL+"/", "gw-1", priv), ctrl
}

func TestControlClientPoll(t *testing.T) {
	c, ctrl := newClient(t)
	want := signed(t, sampleSnapshot(7), ctrl.trust, ctrl.priv)
	ctrl.snapshot = func(after uint64) *Snapshot {
		if after >= 7 {
			return nil
		}
		return &want
	}

	got, err := c.Poll(context.Background(), 3)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got == nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("Poll = %+v, want %+v", got, want)
	}

	got, err = c.Poll(context.Background(), 7)
	if err != nil || got != nil {
		t.Fatalf("Poll after the newest = %v, %v; want nil, nil (204)", got, err)
	}
	if ctrl.polls[0] != 3 || ctrl.polls[1] != 7 {
		t.Fatalf("after values = %v, want [3 7]", ctrl.polls)
	}
}

func TestControlClientPollErrors(t *testing.T) {
	// A control plane that answers with something else is an error, and a
	// redirect is not followed with the signed request.
	for name, h := range map[string]http.HandlerFunc{
		"500":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		"404":      func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) },
		"not json": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) },
		"redirect": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://127.0.0.1:1/elsewhere", http.StatusFound)
		},
	} {
		srv := httptest.NewServer(h)
		bad := NewControlClient(srv.URL, "gw-1", ed25519.NewKeyFromSeed(make([]byte, 32)))
		if s, err := bad.Poll(context.Background(), 0); err == nil || s != nil {
			t.Errorf("%s: Poll = %v, %v; want an error", name, s, err)
		}
		srv.Close()
	}

	// A cancelled context ends the poll.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := newClient(t)
	if _, err := c.Poll(ctx, 0); err == nil {
		t.Error("Poll with a cancelled context succeeded")
	}
}

func TestControlClientReport(t *testing.T) {
	c, ctrl := newClient(t)
	ctrl.live = 3
	r := Report{
		InstanceID: "inst-1", SnapshotVersion: 9, Draining: true,
		Routes: []RouteStats{{RouteID: "route-a", BackendConns: 2, Pinned: 1, PinRate: 0.5, WaitP95: 12 * time.Millisecond, Reachable: true, MaxConnections: 100}},
	}
	resp, err := c.Report(context.Background(), r)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if resp.LiveInstances != 3 {
		t.Fatalf("LiveInstances = %d, want 3", resp.LiveInstances)
	}
	got, _ := ctrl.lastReport()
	if got.InstanceID != "inst-1" || got.SnapshotVersion != 9 || !got.Draining || len(got.Routes) != 1 || got.Routes[0] != r.Routes[0] {
		t.Fatalf("control plane got %+v, want %+v", got, r)
	}

	// The wire names are snake_case.
	b, err := json.Marshal(Report{InstanceID: "i", SnapshotVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"instance_id":"i","snapshot_version":1,"draining":false,"routes":null}` {
		t.Fatalf("Report JSON = %s", b)
	}
	// A report with no routes still sends a list.
	if _, err := c.Report(context.Background(), Report{InstanceID: "i"}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	bad := NewControlClient(srv.URL, "gw-1", ed25519.NewKeyFromSeed(make([]byte, 32)))
	if _, err := bad.Report(context.Background(), r); err == nil {
		t.Fatal("Report succeeded against a 500")
	}
}

func TestControlClientHold(t *testing.T) {
	c, ctrl := newClient(t)
	l, err := ParseLabel("orders--dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Hold(context.Background(), l); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if len(ctrl.holds) != 1 || ctrl.holds[0] != "orders--dev" {
		t.Fatalf("holds = %v, want [orders--dev]", ctrl.holds)
	}

	// Any status outside 2xx is an error. 404 means no hold handler.
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError, http.StatusForbidden} {
		ctrl.mu.Lock()
		ctrl.holdStatus = status
		ctrl.mu.Unlock()
		if err := c.Hold(context.Background(), l); err == nil {
			t.Errorf("Hold succeeded against a %d", status)
		}
	}
	ctrl.mu.Lock()
	ctrl.holdStatus = http.StatusOK
	ctrl.mu.Unlock()
	if err := c.Hold(context.Background(), l); err != nil {
		t.Errorf("Hold against a 200: %v", err)
	}
}
