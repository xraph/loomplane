package node

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/nodes/nodestest"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// syncBuf is a log sink tests read while the node writes to it.
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

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

type rig struct {
	env  *testEnv
	srv  *httptest.Server
	st   State
	reg  *Registry
	logs *syncBuf
	pub  atomic.Int32 // public streams the registry served
}

// newRig starts a real hub behind an httptest server and an empty node state.
func newRig(t *testing.T) *rig {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	r := &rig{srv: srv, st: State{Dir: filepath.Join(t.TempDir(), "state")}, logs: &syncBuf{}}
	r.env = newTestEnv(t, "http://"+srv.Listener.Addr().String())
	srv.Config.Handler = r.env.Handler()
	srv.Start()
	t.Cleanup(srv.Close)
	r.reg = &Registry{Log: slog.New(slog.NewTextHandler(r.logs, nil))}
	r.reg.Register(nodeproto.EndpointDocker, func(context.Context, nodeproto.StreamHeader, net.Conn) error { return nil })
	r.reg.Register(nodeproto.EndpointPublic, func(_ context.Context, _ nodeproto.StreamHeader, c net.Conn) error {
		r.pub.Add(1)
		_, err := c.Write([]byte("public-ok"))
		return err
	})
	return r
}

func quietDetect() DetectOptions {
	return DetectOptions{
		DockerSockets: []string{}, PodmanSockets: []string{},
		NvidiaSMI: func(context.Context) ([]byte, error) { return nil, errors.New("no nvidia-smi") },
		Hostname:  func() (string, error) { return "box", nil },
		Ports:     func() string { return "free" },
	}
}

func (r *rig) join(t *testing.T, eps ...nodeproto.Endpoint) Config {
	t.Helper()
	cfg, err := Join(t.Context(), JoinOptions{URL: r.srv.URL, Token: "lpj_live_x", Name: "web-1", Endpoints: eps,
		State: r.st, Out: io.Discard, Detect: quietDetect()})
	if err != nil {
		t.Fatal(err)
	}
	return *cfg
}

func (r *rig) runtime(t *testing.T) *Runtime {
	t.Helper()
	rt, err := NewRuntime(r.st, r.reg)
	if err != nil {
		t.Fatal(err)
	}
	rt.Log = slog.New(slog.NewTextHandler(r.logs, nil))
	rt.minDelay, rt.maxDelay = 10*time.Millisecond, 50*time.Millisecond
	rt.detectBase = quietDetect()
	return rt
}

// startCancelable is start with a way to stop Run early.
func startCancelable(t *testing.T, rt *Runtime) (context.CancelFunc, chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { _ = rt.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel, done
}

// start runs rt until the test ends, and returns what Run returned.
func start(t *testing.T, rt *Runtime) chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	stopped := make(chan struct{})
	go func() { done <- rt.Run(ctx); close(stopped) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("Run did not stop after its context ended")
		}
	})
	return done
}

func (r *rig) online(id string) bool {
	n := r.env.Store.Node(id)
	return n != nil && n.Status == nodes.StatusOnline && r.env.Hub().Online(id)
}

func (r *rig) open(ep nodeproto.Endpoint, id string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.env.Hub().Open(ctx, nodestest.Org, id, nodeproto.StreamHeader{Endpoint: ep})
}

func TestJoinThenRunOnline(t *testing.T) {
	r := newRig(t)
	cfg := r.join(t, nodeproto.EndpointProbe)
	if cfg.NodeID == "" || cfg.OrgID != nodestest.Org || cfg.URL != r.srv.URL || cfg.IngressMode != nodeproto.IngressNone {
		t.Fatalf("config: %+v", cfg)
	}
	saved, err := r.st.LoadConfig()
	if err != nil || saved.NodeID != cfg.NodeID {
		t.Fatalf("saved config: %+v %v", saved, err)
	}
	rt := r.runtime(t)
	rt.nodeVersion = "v9.9.9-test"
	start(t, rt)
	eventually(t, "the node to be online", func() bool { return r.online(cfg.NodeID) })

	c, err := r.open(nodeproto.EndpointProbe, cfg.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	var inv nodeproto.Inventory
	if err = json.NewDecoder(c).Decode(&inv); err != nil || inv.Arch == "" {
		t.Fatalf("inventory: %+v %v", inv, err)
	}
	// The hub reads the node's own Hello, so the control plane learns its version.
	eventually(t, "the node version to be recorded", func() bool { return r.env.Store.Node(cfg.NodeID).Version == "v9.9.9-test" })
}

func TestRunNarrow(t *testing.T) {
	r := newRig(t)
	cfg := r.join(t, nodeproto.EndpointProbe, nodeproto.EndpointDocker)
	start(t, r.runtime(t))
	eventually(t, "the node to be online", func() bool { return r.online(cfg.NodeID) })
	if c, err := r.open(nodeproto.EndpointDocker, cfg.NodeID); err != nil {
		t.Fatalf("docker before the narrow: %v", err)
	} else {
		_ = c.Close()
	}

	if _, err := r.env.Svc.Update(t.Context(), nodestest.Principal, cfg.NodeID, nodes.Update{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "node.json to lose docker", func() bool {
		c, err := r.st.LoadConfig()
		return err == nil && len(c.Endpoints) == 1 && c.Endpoints[0] == nodeproto.EndpointProbe
	})
	_, err := r.open(nodeproto.EndpointDocker, cfg.NodeID)
	if !errors.Is(err, nodes.ErrEndpointRefused) || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("docker after the narrow: %v", err)
	}
}

func TestRunSetIngress(t *testing.T) {
	r := newRig(t)
	cfg := r.join(t, nodeproto.EndpointProbe)
	start(t, r.runtime(t))
	eventually(t, "the node to be online", func() bool { return r.online(cfg.NodeID) })

	if _, err := r.open(nodeproto.EndpointPublic, cfg.NodeID); !errors.Is(err, nodes.ErrEndpointRefused) {
		t.Fatalf("public with ingress none: %v", err)
	}
	set := func(mode string) {
		if _, err := r.env.Svc.Update(t.Context(), nodestest.Principal, cfg.NodeID, nodes.Update{IngressMode: &mode}); err != nil {
			t.Fatal(err)
		}
	}
	set(nodeproto.IngressRelay)
	eventually(t, "public to reach the registry", func() bool {
		c, err := r.open(nodeproto.EndpointPublic, cfg.NodeID)
		if err != nil {
			return false
		}
		defer func() { _ = c.Close() }()
		b, _ := io.ReadAll(c)
		return string(b) == "public-ok"
	})
	eventually(t, "the ingress mode to be saved", func() bool {
		c, err := r.st.LoadConfig()
		return err == nil && c.IngressMode == nodeproto.IngressRelay
	})
	set(nodeproto.IngressNone)
	eventually(t, "public to be refused", func() bool {
		c, err := r.open(nodeproto.EndpointPublic, cfg.NodeID)
		if err == nil {
			_ = c.Close()
		}
		return errors.Is(err, nodes.ErrEndpointRefused)
	})
}

func TestRunIgnoresBadIngressMode(t *testing.T) {
	r := newRig(t)
	cfg := r.join(t, nodeproto.EndpointProbe)
	rt := r.runtime(t)
	start(t, rt)
	eventually(t, "the node to be online", func() bool { return r.online(cfg.NodeID) })
	// The hub only sends valid modes, so drive the node's handler directly.
	rt.handle(nil, nodeproto.SetIngress{Mode: "everywhere"})
	if c := rt.live.Load(); c.IngressMode != nodeproto.IngressNone {
		t.Fatalf("a bad mode was applied: %q", c.IngressMode)
	}
}

func TestRunRevokedExits3(t *testing.T) {
	r := newRig(t)
	cfg := r.join(t, nodeproto.EndpointProbe)
	done := start(t, r.runtime(t))
	eventually(t, "the node to be online", func() bool { return r.online(cfg.NodeID) })
	if err := r.env.Svc.Revoke(t.Context(), nodestest.Principal, cfg.NodeID); err != nil {
		t.Fatal(err)
	}
	assertRevoked(t, r, done)
}

func TestRunRevokedAtConnectExits3(t *testing.T) {
	r := newRig(t)
	r.join(t, nodeproto.EndpointProbe)
	// Revoked after the assertion checks out and before the session is claimed.
	r.env.Store.BeforeClaim = func(id string) { r.env.Store.Revoke(id) }
	done := start(t, r.runtime(t))
	assertRevoked(t, r, done)
}

func assertRevoked(t *testing.T, r *rig, done chan error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRevoked) || ExitCode(err) != 3 || err.Error() != "this node was revoked in Loomplane" {
			t.Fatalf("Run returned %v (exit %d)", err, ExitCode(err))
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Run did not exit after the node was revoked; logs:\n%s", r.logs.String())
	}
	if _, err := os.Stat(filepath.Join(r.st.Dir, "node.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("node.json should be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.st.Dir, "identity.key")); err != nil {
		t.Fatalf("the key must stay: %v", err)
	}
}

func TestRunReconnectsAfterServerRestart(t *testing.T) {
	r := newRig(t)
	cfg := r.join(t, nodeproto.EndpointProbe)
	start(t, r.runtime(t))
	eventually(t, "the node to be online", func() bool { return r.online(cfg.NodeID) })
	before := r.env.Hub()

	r.env.RestartHub(t)
	eventually(t, "the node to reconnect to the new hub", func() bool {
		return r.env.Hub() != before && r.env.Hub().Online(cfg.NodeID)
	})
	c, err := r.open(nodeproto.EndpointProbe, cfg.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}

func TestRunRetriesWhenKeyRefused(t *testing.T) {
	r := newRig(t)
	cfg := r.join(t, nodeproto.EndpointProbe)
	// The control plane now knows a different key for this node.
	other, _, _ := ed25519.GenerateKey(nil)
	n := r.env.Store.Node(cfg.NodeID)
	n.PublicKey = other
	if err := r.env.Store.Update(t.Context(), n); err != nil {
		t.Fatal(err)
	}
	start(t, r.runtime(t))
	want := "Loomplane refused this node's key; if it was revoked, run loomctl node leave"
	eventually(t, "the refusal to be logged twice", func() bool { return strings.Count(r.logs.String(), want) >= 2 })
}

func TestRunStopsOnUpdateNeeded(t *testing.T) {
	var connects atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/node/v1/challenge", func(w http.ResponseWriter, req *http.Request) {
		_ = json.NewEncoder(w).Encode(nodeproto.Challenge{NodeID: req.URL.Query().Get("node"), Nonce: "n", IssuedAt: time.Now()})
	})
	mux.HandleFunc("/node/v1/connect", func(w http.ResponseWriter, _ *http.Request) {
		connects.Add(1)
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(`{"error":"update this node"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	st := State{Dir: filepath.Join(t.TempDir(), "state")}
	if err := st.SaveConfig(Config{URL: srv.URL, NodeID: "node_1", OrgID: "org_1", IngressMode: nodeproto.IngressNone}); err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(st, &Registry{Log: discardLog()})
	if err != nil {
		t.Fatal(err)
	}
	rt.minDelay = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err = rt.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "update this node") || ExitCode(err) == 3 {
		t.Fatalf("Run: %v", err)
	}
	if connects.Load() != 1 {
		t.Fatalf("connected %d times; a 426 must not retry", connects.Load())
	}
}

func TestRunUsesProxy(t *testing.T) {
	srv := httptest.NewUnstartedServer(nil)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	// A loopback address never goes through a proxy, so name the server.
	url := "https://example.com:" + port
	env := newTestEnv(t, url)
	srv.Config.Handler = env.Handler()
	srv.StartTLS()
	defer srv.Close()

	var connects atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodConnect {
			http.Error(w, "only CONNECT", http.StatusMethodNotAllowed)
			return
		}
		connects.Add(1)
		up, err := (&net.Dialer{}).DialContext(req.Context(), "tcp", srv.Listener.Addr().String())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		down, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			_ = up.Close()
			return
		}
		_, _ = down.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() { _, _ = io.Copy(up, down); _ = up.Close() }()
		go func() { _, _ = io.Copy(down, up); _ = down.Close() }()
	}))
	defer proxy.Close()
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("https_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	st := State{Dir: filepath.Join(t.TempDir(), "state")}
	seedNode(t, env, st, url)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	reg := &Registry{Log: discardLog()}
	rt, err := NewRuntime(st, reg)
	if err != nil {
		t.Fatal(err)
	}
	rt.tls = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	rt.detectBase = quietDetect()
	start(t, rt)
	eventually(t, "the node to be online through the proxy", func() bool {
		return env.Hub().Online("node_1") && connects.Load() >= 2
	})
}

// seedNode puts a node in the store with the key the state directory holds,
// and writes the matching node.json, so a test can skip Join.
func seedNode(t *testing.T, env *testEnv, st State, url string) {
	t.Helper()
	priv, err := st.Identity()
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("not an ed25519 key")
	}
	now := time.Now().UTC()
	if err = env.Store.Create(t.Context(), &nodes.Node{ID: "node_1", OrgID: nodestest.Org, Name: "web-1", PublicKey: pub,
		KeyFingerprint: nodeproto.Fingerprint(pub), Status: nodes.StatusPending, Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe},
		IngressMode: nodeproto.IngressNone, Role: "node", Version: "v1.8.0", ProtocolVersion: nodeproto.Version, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = st.SaveConfig(Config{URL: url, NodeID: "node_1", OrgID: nodestest.Org, Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe}, IngressMode: nodeproto.IngressNone}); err != nil {
		t.Fatal(err)
	}
}

func TestAllowWhileConnected(t *testing.T) {
	r := newRig(t)
	cfg := r.join(t, nodeproto.EndpointProbe)
	rt := r.runtime(t)
	start(t, rt)
	eventually(t, "the node to be online", func() bool { return r.online(cfg.NodeID) })
	// The hub's first Narrow follows its first inventory; a widen made
	// before it arrives would be undone by it.
	eventually(t, "the first narrow", func() bool { return rt.narrows.Load() >= 1 })

	if err := rt.Allow([]nodeproto.Endpoint{nodeproto.EndpointDocker}); err != nil {
		t.Fatal(err)
	}
	if c, err := r.st.LoadConfig(); err != nil || len(c.Endpoints) != 2 {
		t.Fatalf("saved: %+v %v", c, err)
	}
	eventually(t, "the control plane to learn of docker", func() bool {
		return fmt.Sprint(r.env.Store.Node(cfg.NodeID).Endpoints) == "[probe docker]"
	})
	if c, err := r.open(nodeproto.EndpointDocker, cfg.NodeID); err != nil {
		t.Fatalf("docker after allow: %v", err)
	} else {
		_ = c.Close()
	}
	if err := rt.Allow([]nodeproto.Endpoint{nodeproto.EndpointPublic}); err == nil {
		t.Fatal("public cannot be allowed")
	}
	if err := rt.Allow([]nodeproto.Endpoint{"nonsense"}); err == nil {
		t.Fatal("an unknown endpoint cannot be allowed")
	}
}

func nodeJSON(r *rig) Config {
	c, _ := r.st.LoadConfig()
	return c
}

func TestAllowBeforeFirstNarrowSurvives(t *testing.T) {
	r := newRig(t)
	cfg := r.join(t, nodeproto.EndpointProbe)
	rt := r.runtime(t)
	start(t, rt)
	// No waiting for the hub's first Narrow: it carries the old set.
	if err := rt.Allow([]nodeproto.Endpoint{nodeproto.EndpointDocker}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the hub to store docker", func() bool {
		return fmt.Sprint(r.env.Store.Node(cfg.NodeID).Endpoints) == "[probe docker]"
	})
	eventually(t, "the acknowledgement to clear pending", func() bool {
		c := nodeJSON(r)
		return len(c.PendingAllowed) == 0 && fmt.Sprint(c.Endpoints) == "[probe docker]"
	})
	if c := rt.live.Load(); fmt.Sprint(c.Endpoints) != "[probe docker]" || len(c.PendingAllowed) != 0 {
		t.Fatalf("live config: %+v", c)
	}
	if c, err := r.open(nodeproto.EndpointDocker, cfg.NodeID); err != nil {
		t.Fatalf("docker after allow: %v", err)
	} else {
		_ = c.Close()
	}
}

func TestAllowSurvivesRestart(t *testing.T) {
	r := newRig(t)
	cfg := r.join(t, nodeproto.EndpointProbe)
	// Allowed while no connection was up, by a process that then died.
	if err := r.runtime(t).Allow([]nodeproto.Endpoint{nodeproto.EndpointDocker}); err != nil {
		t.Fatal(err)
	}
	if c := nodeJSON(r); fmt.Sprint(c.PendingAllowed) != "[docker]" || fmt.Sprint(c.Endpoints) != "[probe docker]" {
		t.Fatalf("saved: %+v", c)
	}
	rt := r.runtime(t) // a new process, reading node.json
	rt.narrows.Store(0)
	start(t, rt)
	eventually(t, "the hub to learn of docker", func() bool {
		return fmt.Sprint(r.env.Store.Node(cfg.NodeID).Endpoints) == "[probe docker]"
	})
	eventually(t, "pending to clear", func() bool { return len(nodeJSON(r).PendingAllowed) == 0 })
	if c := rt.live.Load(); fmt.Sprint(c.Endpoints) != "[probe docker]" {
		t.Fatalf("live config: %+v", c)
	}
}

func TestAdminNarrowRemovesAcknowledgedEndpoint(t *testing.T) {
	r := newRig(t)
	cfg := r.join(t, nodeproto.EndpointProbe)
	rt := r.runtime(t)
	start(t, rt)
	if err := rt.Allow([]nodeproto.Endpoint{nodeproto.EndpointDocker}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "pending to clear", func() bool {
		c := nodeJSON(r)
		return len(c.PendingAllowed) == 0 && len(c.Endpoints) == 2
	})
	if _, err := r.env.Svc.Update(t.Context(), nodestest.Principal, cfg.NodeID, nodes.Update{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "docker to go", func() bool { return fmt.Sprint(nodeJSON(r).Endpoints) == "[probe]" })
}

func TestSetIngressReconciledOnConnect(t *testing.T) {
	r := newRig(t)
	cfg := r.join(t, nodeproto.EndpointProbe)
	set := func(mode string) {
		if _, err := r.env.Svc.Update(t.Context(), nodestest.Principal, cfg.NodeID, nodes.Update{IngressMode: &mode}); err != nil {
			t.Fatal(err)
		}
	}
	set(nodeproto.IngressRelay)
	rt := r.runtime(t)
	cancel, done := startCancelable(t, rt)
	eventually(t, "relay to be saved", func() bool { return nodeJSON(r).IngressMode == nodeproto.IngressRelay })
	cancel()
	<-done

	// Changed while the node is away: nobody can tell it.
	set(nodeproto.IngressNone)
	rt2 := r.runtime(t)
	if rt2.live.Load().IngressMode != nodeproto.IngressRelay {
		t.Fatal("the stopped node should still say relay")
	}
	start(t, rt2)
	eventually(t, "none to be saved", func() bool { return nodeJSON(r).IngressMode == nodeproto.IngressNone })
	if got := rt2.live.Load().IngressMode; got != nodeproto.IngressNone {
		t.Fatalf("live ingress mode %q", got)
	}
	if _, err := r.open(nodeproto.EndpointPublic, cfg.NodeID); !errors.Is(err, nodes.ErrEndpointRefused) {
		t.Fatalf("public after reconcile: %v", err)
	}
}

func TestFailedSaveKeepsChangeAndSendsNoAck(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	r := newRig(t)
	r.join(t, nodeproto.EndpointProbe, nodeproto.EndpointDocker)
	rt := r.runtime(t)
	if err := os.Chmod(r.st.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(r.st.Dir, 0o700) }()
	// A nil session records no acks, so a nil error from handle means the
	// failed save was logged and swallowed rather than sent on.
	if err := rt.handle(nil, nodeproto.Narrow{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe}}); err != nil {
		t.Fatal(err)
	}
	if got := rt.live.Load().Endpoints; fmt.Sprint(got) != "[probe]" {
		t.Fatalf("the in-memory narrow was dropped: %v", got)
	}
	if !strings.Contains(r.logs.String(), "saving a narrowed allowlist") {
		t.Fatalf("the failure was not logged:\n%s", r.logs.String())
	}
}

func TestRunReportsInventoryChange(t *testing.T) {
	r := newRig(t)
	cfg := r.join(t, nodeproto.EndpointProbe)
	rt := r.runtime(t)
	var cpus atomic.Int32
	cpus.Store(2)
	rt.detect = func(context.Context) nodeproto.Inventory {
		return nodeproto.Inventory{OS: "linux", Arch: "amd64", CPUs: int(cpus.Load())}
	}
	rt.detectEvery = 20 * time.Millisecond
	start(t, rt)
	eventually(t, "the first inventory", func() bool { return r.env.Store.Node(cfg.NodeID).Inventory.CPUs == 2 })
	cpus.Store(8)
	eventually(t, "the changed inventory", func() bool { return r.env.Store.Node(cfg.NodeID).Inventory.CPUs == 8 })
}

func TestRunSendsHeartbeats(t *testing.T) {
	r := newRig(t)
	cfg := r.join(t, nodeproto.EndpointProbe)
	rt := r.runtime(t)
	rt.heartbeatEvery = 20 * time.Millisecond
	start(t, rt)
	eventually(t, "the node to be online", func() bool { return r.online(cfg.NodeID) })
	first := r.env.Store.Node(cfg.NodeID).LastSeenAt
	eventually(t, "last seen to advance", func() bool {
		n := r.env.Store.Node(cfg.NodeID)
		return first != nil && n.LastSeenAt != nil && n.LastSeenAt.After(*first)
	})
}

func TestJoinClockSkew(t *testing.T) {
	var joins atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/node/v1/challenge", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Date", time.Now().Add(3*time.Minute).UTC().Format(http.TimeFormat))
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/api/v1/nodes/join", func(http.ResponseWriter, *http.Request) { joins.Add(1) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	st := State{Dir: filepath.Join(t.TempDir(), "state")}
	var out strings.Builder
	_, err := Join(t.Context(), JoinOptions{URL: srv.URL, Token: "x", State: st, Out: &out, Detect: quietDetect(), Endpoints: []nodeproto.Endpoint{}})
	if err == nil || !strings.HasPrefix(err.Error(), "this machine's clock is off by 2m") && !strings.HasPrefix(err.Error(), "this machine's clock is off by 3m") ||
		!strings.HasSuffix(err.Error(), "; fix NTP and join again") {
		t.Fatalf("Join: %v", err)
	}
	if joins.Load() != 0 {
		t.Fatal("the join token was spent although the clock is off")
	}
	if _, statErr := os.Stat(filepath.Join(st.Dir, "node.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("no config should be saved: %v", statErr)
	}
}

func TestJoinPrintsPreflightAndPrompts(t *testing.T) {
	r := newRig(t)
	var out strings.Builder
	var asked atomic.Int32
	cfg, err := Join(t.Context(), JoinOptions{URL: r.srv.URL + "/", Token: "x", Name: "web-1", State: r.st, Out: &out, Detect: quietDetect(),
		Prompt: func(inv nodeproto.Inventory) ([]nodeproto.Endpoint, error) {
			asked.Add(1)
			if inv.Arch == "" {
				t.Error("the prompt got no inventory")
			}
			return []nodeproto.Endpoint{nodeproto.EndpointProbe}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if asked.Load() != 1 || len(cfg.Endpoints) != 1 {
		t.Fatalf("asked %d, endpoints %v", asked.Load(), cfg.Endpoints)
	}
	for _, want := range []string{"runtime", "gpus", "arch", "cpu", "memory", "disk", "ports 80/443", "clock"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the preflight table lacks %q:\n%s", want, out.String())
		}
	}
	// A second join on the same machine is refused, not overwritten.
	if _, err = Join(t.Context(), JoinOptions{URL: r.srv.URL, Token: "y", State: r.st, Out: io.Discard, Detect: quietDetect(), Endpoints: []nodeproto.Endpoint{}}); err == nil ||
		!strings.Contains(err.Error(), "already joined") {
		t.Fatalf("second join: %v", err)
	}
}

func TestJoinSurfacesServerRefusals(t *testing.T) {
	for _, tc := range []struct {
		status int
		msg    string
	}{
		{http.StatusUpgradeRequired, "update this node"},
		{http.StatusUnauthorized, "that join token isn't valid; mint a new join token and try again"},
		{http.StatusServiceUnavailable, "try again"},
	} {
		mux := http.NewServeMux()
		mux.HandleFunc("/node/v1/challenge", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
		mux.HandleFunc("/api/v1/nodes/join", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"error":"` + tc.msg + `"}`))
		})
		srv := httptest.NewServer(mux)
		st := State{Dir: filepath.Join(t.TempDir(), "state")}
		_, err := Join(t.Context(), JoinOptions{URL: srv.URL, Token: "x", State: st, Out: io.Discard, Detect: quietDetect(), Endpoints: []nodeproto.Endpoint{}})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), tc.msg) {
			t.Fatalf("%d: %v", tc.status, err)
		}
		if _, statErr := os.Stat(filepath.Join(st.Dir, "node.json")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%d: no config should be saved: %v", tc.status, statErr)
		}
	}
}
