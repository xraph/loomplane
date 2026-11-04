//go:build integration

package server_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	extnodes "github.com/xraph/loomplane/extensions/nodes"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/node"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/nodes/nodestest"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

// The node fixtures stand a real hub up behind the real node routes, over
// an in-memory store, and put the node's row in it with the node's public
// key, so the node connects with the state it is given and no join.
func init() {
	server.NodeFixture = nodeFixture
	server.ContainerNodeFixture = containerNodeFixture
}

// nodeEndpoints is what the fixture's node serves: its ceiling, its
// node.json and its row all allow these.
var nodeEndpoints = []nodeproto.Endpoint{nodeproto.EndpointDocker, nodeproto.EndpointDial, nodeproto.EndpointProbe}

// containerStateDir is where a node in a container keeps its state.
const containerStateDir = "/var/lib/loomplane-node"

// testHub is a hub serving one node.
type testHub struct {
	hub    *nodes.Hub
	nodeID string
	url    string

	mu sync.Mutex
	// online is closed when the node's next session has sent its first
	// inventory.
	online chan struct{}
}

// startHub serves a hub on listener, for a node that reaches it at url, and
// adds node nodeID with key pub to its store.
func startHub(t *testing.T, listener net.Listener, url, nodeID string, pub ed25519.PublicKey) *testHub {
	t.Helper()
	store := nodestest.NewStore()
	svc := nodestest.NewService(store, &nodestest.Tokens{})
	hub := nodestest.NewHub(t, url, store, svc)
	svc.Notify = hub.Notify
	ctx, cancel := context.WithCancel(context.Background())
	routes, err := extnodes.NodeHandler(ctx, hub, svc, url)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	srv := &http.Server{Handler: routes, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
	})
	h := &testHub{hub: hub, nodeID: nodeID, url: url, online: make(chan struct{})}
	hub.OnOnline(func(_ context.Context, n *nodes.Node) {
		if n.ID != h.nodeID {
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		select {
		case <-h.online:
		default:
			close(h.online)
		}
	})
	now := time.Now().UTC()
	if err = store.Create(t.Context(), &nodes.Node{
		ID: h.nodeID, OrgID: nodestest.Org, Name: "parity-" + strconv.FormatInt(now.UnixNano()%1_000_000, 10),
		PublicKey: pub, KeyFingerprint: nodeproto.Fingerprint(pub), IdentityGeneration: 1,
		Status: nodes.StatusOffline, Endpoints: nodeEndpoints, IngressMode: "none", Role: "node",
		Labels: map[string]string{}, JoinedBy: nodestest.User, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return h
}

// nodeState writes a joined node's state into dir: its key, node.json and an
// allow.json ceiling, all for nodeID. It returns the node's public key.
func nodeState(t *testing.T, dir, url, nodeID, socket string) ed25519.PublicKey {
	t.Helper()
	st := node.State{Dir: dir}
	key, err := st.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SaveConfig(node.Config{URL: url, NodeID: nodeID, OrgID: nodestest.Org, IdentityGeneration: 1,
		Endpoints: nodeEndpoints, IngressMode: "none", DockerSocket: socket, Name: "parity"}); err != nil {
		t.Fatal(err)
	}
	if err = st.SaveCeiling(node.Ceiling{NodeID: nodeID, Endpoints: nodeEndpoints}, -1); err != nil {
		t.Fatal(err)
	}
	pub, ok := key.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("the node's key is not ed25519")
	}
	return pub
}

// expectSession makes waitOnline wait for a session that starts after now.
func (h *testHub) expectSession() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.online = make(chan struct{})
}

// waitOnline waits for the node's session to send its first inventory.
func (h *testHub) waitOnline(t testing.TB, logs func() string) {
	t.Helper()
	h.mu.Lock()
	online := h.online
	h.mu.Unlock()
	select {
	case <-online:
	case <-time.After(90 * time.Second):
		t.Fatalf("the node never came online at %s:\n%s", h.url, logs())
	}
}

// connect waits for the node to come online and returns a Client that
// reaches its daemon through the hub.
func (h *testHub) connect(t *testing.T, socket, dockerID string, logs func() string) *server.Client {
	t.Helper()
	h.waitOnline(t, logs)
	cfg, err := server.ParseConfig(server.NodeCredential(nodestest.Org, h.nodeID), map[string]string{
		"node_id": h.nodeID, "identity_generation": "1", "docker_socket": socket,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Details["docker_id"] = dockerID
	c, err := server.Connect(t.Context(), cfg, h.hub)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// nodeFixture runs a node in this process against the daemon at socket.
func nodeFixture(t *testing.T, socket string) *server.Client {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + listener.Addr().String()
	// The node's control socket lives in its state dir, and macOS caps a
	// Unix socket path at 104 bytes, so the dir stays short.
	dir, err := os.MkdirTemp("", "lpn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	state := filepath.Join(dir, "state")
	nodeID := "node_" + uuid.NewString()
	h := startHub(t, listener, url, nodeID, nodeState(t, state, url, nodeID, socket))

	dockerID := daemonID(t, socket)
	journal, err := node.OpenFenceJournal(state, dockerID)
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	// What loomctl node run registers for a node that may use docker and
	// dial; NewRuntime registers the probes itself.
	reg := &node.Registry{Log: log}
	reg.Register(nodeproto.EndpointDocker, node.DockerHandler(socket, journal))
	reg.Register(nodeproto.EndpointDial, node.DialHandler(socket))
	rt, err := node.NewRuntime(node.State{Dir: state}, reg)
	if err != nil {
		t.Fatal(err)
	}
	rt.Log = log
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- rt.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if runErr := <-stopped; runErr != nil {
			t.Errorf("the node stopped with %v", runErr)
		}
		if t.Failed() {
			t.Logf("node log:\n%s", logs.String())
		}
	})
	return h.connect(t, socket, dockerID, logs.String)
}

// containerNodeFixture runs loomctl node run inside container, against the
// container's own daemon, so the node dials from the container's network
// and its Linux probes run on Linux. The container must reach this host as
// host.docker.internal.
func containerNodeFixture(t *testing.T, container string) *server.Client {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://host.docker.internal:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port) //nolint:forcetypeassert // a TCP listener
	const socket = "/var/run/docker.sock"
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	nodeID := "node_" + uuid.NewString()
	h := startHub(t, listener, url, nodeID, nodeState(t, state, url, nodeID, socket))

	dockerCLI(t, "cp", state, container+":"+containerStateDir)
	// The node runs as root there, so its state is root's, and allow.json
	// root's too, as a service install leaves it.
	dockerCLI(t, "exec", container, "sh", "-c", "chown -R 0:0 "+containerStateDir+" && chmod 700 "+containerStateDir+" && chmod 600 "+containerStateDir+"/node.json "+containerStateDir+"/identity.key && chmod 640 "+containerStateDir+"/allow.json")
	logs := func() string {
		out, _ := exec.CommandContext(context.Background(), "docker", "exec", container, "cat", "/var/log/loomplane-node.log").CombinedOutput()
		return string(out)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("node log:\n%s", logs())
		}
	})
	// A node's fence checks an owned proxy's image against the list it was
	// built with, where the SSH helper reads the test's pinned image with
	// each request. So the node is built trusting the image pinned now, and
	// rebuilt and restarted whenever the test pins another.
	run := func(t testing.TB, image string) {
		dockerCLI(t, "cp", buildLoomctl(t, dir, image), container+":/usr/local/bin/loomctl")
		dockerCLI(t, "exec", "-d", container, "sh", "-c", "echo $$ >/run/loomplane-node.pid && exec /usr/local/bin/loomctl node run --state-dir "+containerStateDir+" >>/var/log/loomplane-node.log 2>&1")
	}
	run(t, server.PinnedIngressImage())
	server.PinNodeImages(t, func(t testing.TB, image string) {
		t.Helper()
		h.expectSession()
		dockerCLI(t, "exec", container, "sh", "-c", "pid=$(cat /run/loomplane-node.pid) && kill $pid && while kill -0 $pid 2>/dev/null; do sleep 0.1; done")
		run(t, image)
		h.waitOnline(t, logs)
	})
	return h.connect(t, socket, dockerCLI(t, "exec", container, "docker", "info", "--format", "{{.ID}}"), logs)
}

// lockedBuffer is a log the node's goroutines write and a test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// buildLoomctl builds loomctl for Linux on the Docker daemon's architecture
// into dir, trusting image as the DNS-capable proxy.
func buildLoomctl(t testing.TB, dir, image string) string {
	t.Helper()
	var arch string
	switch reported := dockerCLI(t, "info", "--format", "{{.Architecture}}"); reported {
	case "x86_64", "amd64":
		arch = "amd64"
	case "aarch64", "arm64":
		arch = "arm64"
	default:
		t.Fatalf("Docker reports the unsupported architecture %q", reported)
	}
	bin := filepath.Join(dir, "loomctl")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-ldflags", "-X github.com/xraph/loomplane/internal/clouds/server/fencerules.DNSImage="+image, "-o", bin, "./cmd/loomctl")
	cmd.Dir = "../../.."
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build loomctl for linux/%s: %v\n%s", arch, err, out)
	}
	return bin
}

func dockerCLI(t testing.TB, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// daemonID is the ID the daemon at socket reports.
func daemonID(t *testing.T, socket string) string {
	t.Helper()
	api := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://docker/info", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := api.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var info struct{ ID string }
	if err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&info); err != nil || info.ID == "" {
		t.Fatal(errors.Join(errors.New("the daemon reports no ID"), err))
	}
	return info.ID
}
