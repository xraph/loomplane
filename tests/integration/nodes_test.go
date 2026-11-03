//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/forge"

	nodesext "github.com/xraph/loomplane/extensions/nodes"
	"github.com/xraph/loomplane/internal/buildinfo"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

const (
	// nodesImage is small and pinned by tag and digest.
	nodesImage = "alpine:3.20.3@sha256:1e42bbe2508154c9126d48c2b8a75420c3544343bf86fd041fb7527e017a4b4a"
	// nodeStateDir is where the node in the container keeps its identity.
	nodeStateDir = "/var/lib/e2e-node"
	// revokedExit is the exit code of a node that Loomplane revoked (R20).
	revokedExit = 77
	// revokeSlack is how long a revoked node has to exit. The requirement is
	// 2 seconds; the test allows more for a loaded CI machine and prints
	// the time it measured.
	revokeSlack = 5 * time.Second
)

// replica is one Loomplane app that shares a database with the others.
type replica struct {
	run  *running
	hub  *nodes.Hub
	base string // where this replica listens, as the host sees it
}

// startReplica boots one app on l. cfg is copied. The hub is read from the
// app's container, so the test can open a stream to a node the way a later
// extension will.
func startReplica(t *testing.T, cfg config.Config, l net.Listener) *replica {
	t.Helper()
	r := start(t, cfg, func(srv *httptest.Server) {
		_ = srv.Listener.Close()
		srv.Listener = l
	})
	t.Cleanup(r.stop)
	app, ok := r.app.(forge.App)
	if !ok {
		t.Fatalf("the app is a %T, not a forge.App", r.app)
	}
	hub, err := forge.InjectType[*nodes.Hub](app.Container())
	if err != nil {
		t.Fatalf("the nodes hub is not in the container: %v", err)
	}
	return &replica{run: r, hub: hub, base: "http://127.0.0.1:" + strconv.Itoa(l.Addr().(*net.TCPAddr).Port)} //nolint:forcetypeassert // a tcp listener
}

// nodesConfig is a config every replica of one instance shares: one
// database, one secret_key and one public URL, which is what nodes sign.
func nodesConfig(t *testing.T, publicURL string) config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.PublicURL = publicURL
	cfg.Dashboard.Dir = t.TempDir()
	if err := os.WriteFile(cfg.Dashboard.Dir+"/index.html", []byte("<div id=root>loomplane</div>"), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// adminKey signs Ada up on c, in her organization, and returns a client that
// acts with a WRITE API key she made. Join tokens and revokes use the key.
func adminKey(t *testing.T, session *client, base string) *client {
	t.Helper()
	signedIn(t, session)
	var k keyBody
	session.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "nodes", "access": "write"}, &k)
	return &client{t: t, base: base, key: session.key, token: k.Secret}
}

func mintJoinToken(t *testing.T, c *client) string {
	t.Helper()
	var jt struct {
		Token string `json:"token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/nodes/join-tokens", map[string]any{}, &jt)
	if !strings.HasPrefix(jt.Token, "lpj_") {
		t.Fatalf("join token %q does not look like one", jt.Token)
	}
	return jt.Token
}

// installLine is the command the API renders for a join token, taken apart
// the way node.sh would run it: the token from LOOMPLANE_JOIN_TOKEN, and the
// arguments node.sh passes on to loomctl node join.
type installLine struct {
	token string
	args  []string
}

// mintInstallLine mints a join token scoped to body and parses the command
// that comes back. It fails the test if the command is not the one-liner
// node.sh expects, or if the token shows up anywhere but the environment.
func mintInstallLine(t *testing.T, c *client, publicURL string, body map[string]any) installLine {
	t.Helper()
	var jt struct {
		Token   string `json:"token"`
		Command string `json:"command"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/nodes/join-tokens", body, &jt)
	prefix := "curl -fsSL " + publicURL + "/install/node.sh | "
	rest, ok := strings.CutPrefix(jt.Command, prefix)
	if !ok {
		t.Fatalf("command %q does not start with %q", jt.Command, prefix)
	}
	fields := strings.Fields(rest)
	if len(fields) < 3 || fields[0] != "LOOMPLANE_JOIN_TOKEN="+jt.Token || fields[1] != "sh" || fields[2] != "-s" {
		t.Fatalf("command %q is not LOOMPLANE_JOIN_TOKEN=... sh -s [-- args]", jt.Command)
	}
	args := fields[3:]
	if len(args) > 0 {
		if args[0] != "--" {
			t.Fatalf("command %q passes arguments without --", jt.Command)
		}
		args = args[1:]
	}
	if strings.Count(jt.Command, jt.Token) != 1 {
		t.Fatalf("the token appears more than once in %q", jt.Command)
	}
	return installLine{token: jt.Token, args: args}
}

func getNode(t *testing.T, c *client, id string) nodes.Node {
	t.Helper()
	var n nodes.Node
	c.must(http.StatusOK, http.MethodGet, "/api/v1/nodes/"+id, nil, &n)
	return n
}

// waitUntil polls cond until it holds or limit passes.
func waitUntil(t *testing.T, limit time.Duration, what string, cond func() bool) time.Duration {
	t.Helper()
	begin := time.Now()
	for time.Since(begin) < limit {
		if cond() {
			return time.Since(begin)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s", what, limit)
	return 0
}

// Two people cannot both spend one join token: exactly one join succeeds, and
// the other is told the token is spent. This runs against real Postgres, where
// two requests really do race.
func TestNodes_oneJoinTokenOneNode(t *testing.T) {
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := nodesConfig(t, "http://"+l.Addr().String())
	a := startReplica(t, cfg, l)
	api := adminKey(t, &client{t: t, base: a.base, key: cfg.Identity.PublishableKey}, a.base)
	token := mintJoinToken(t, api)

	const joiners = 8
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		statuses = map[int]int{}
		created  []string
	)
	begin := make(chan struct{})
	for i := range joiners {
		pub, priv, kerr := ed25519.GenerateKey(rand.Reader)
		if kerr != nil {
			t.Fatal(kerr)
		}
		name := fmt.Sprintf("racer-%d", i)
		body, merr := json.Marshal(nodesext.NodeJoin{
			Token: token, Name: name, PublicKey: base64.StdEncoding.EncodeToString(pub),
			Signature: base64.StdEncoding.EncodeToString(nodeproto.SignJoin(priv, token, name)),
			Inventory: nodeproto.Inventory{OS: "linux", Arch: "amd64"}, NodeVersion: buildinfo.Version, ProtocolVersion: nodeproto.Version,
		})
		if merr != nil {
			t.Fatal(merr)
		}
		wg.Go(func() {
			<-begin
			req, rerr := http.NewRequestWithContext(t.Context(), http.MethodPost, a.base+"/api/v1/nodes/join", bytes.NewReader(body))
			if rerr != nil {
				t.Error(rerr)
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Publishable-Key", cfg.Identity.PublishableKey)
			res, derr := http.DefaultClient.Do(req)
			if derr != nil {
				t.Error(derr)
				return
			}
			defer res.Body.Close()
			raw, _ := io.ReadAll(res.Body)
			mu.Lock()
			defer mu.Unlock()
			statuses[res.StatusCode]++
			if res.StatusCode == http.StatusCreated {
				created = append(created, string(raw))
			}
		})
	}
	close(begin)
	wg.Wait()
	t.Logf("%d concurrent joins with one token: statuses %v", joiners, statuses)
	if statuses[http.StatusCreated] != 1 || statuses[http.StatusUnauthorized] != joiners-1 {
		t.Fatalf("statuses %v, want one 201 and %d 401", statuses, joiners-1)
	}
	var list struct {
		Nodes []nodes.Node `json:"nodes"`
	}
	api.must(http.StatusOK, http.MethodGet, "/api/v1/nodes", nil, &list)
	if len(list.Nodes) != 1 {
		t.Fatalf("%d nodes after the race, want 1 (created: %v)", len(list.Nodes), created)
	}
}

// dockerArch maps what the Docker daemon reports to a GOARCH.
func dockerArch(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "docker", "info", "--format", "{{.Architecture}}").Output()
	if err != nil {
		t.Skipf("docker is not available: %v", err)
	}
	switch arch := strings.TrimSpace(string(out)); arch {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		t.Skipf("docker reports the unsupported architecture %q", arch)
		return ""
	}
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// buildLoomctl builds the Linux loomctl, for the architecture of the Docker
// daemon, into a temporary directory.
func buildLoomctl(t *testing.T, arch string) string {
	t.Helper()
	bin := t.TempDir() + "/loomctl"
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./cmd/loomctl")
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build loomctl for linux/%s: %v\n%s", arch, err, out)
	}
	return bin
}

// A real node process joins, holds its connection to one replica, and is used
// through the other; then it is allowed more, and revoked.
func TestNodes_joinHopAllowRevoke(t *testing.T) {
	arch := dockerArch(t)
	if os.Getenv("LOOMPLANE_TEST_DATABASE_URL") == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL to run the integration tests")
	}
	loomctl := buildLoomctl(t, arch)
	docker(t, "image", "inspect", nodesImage)

	// Replica A listens on every interface, so the container can reach it as
	// host.docker.internal. Replica B only needs the host.
	lc := &net.ListenConfig{}
	la, err := lc.Listen(t.Context(), "tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	lb, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	portA := la.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // a tcp listener
	// The public URL is what nodes sign, and it is the address the node dials.
	// Both replicas serve the one instance, so both carry it.
	publicURL := "http://host.docker.internal:" + strconv.Itoa(portA)
	cfg := nodesConfig(t, publicURL)

	// 1. Two replicas share Postgres, each with its own advertise URL. They
	// reach each other over plain http on loopback, so nodes.hop_insecure is
	// set (the R15 flag); without it the hop is refused.
	cfgA, cfgB := cfg, cfg
	cfgA.Nodes = config.NodesConfig{AdvertiseURL: "http://127.0.0.1:" + strconv.Itoa(portA), HopInsecure: true}
	cfgB.Nodes = config.NodesConfig{AdvertiseURL: "http://" + lb.Addr().String(), HopInsecure: true}
	a := startReplica(t, cfgA, la)
	b := startReplica(t, cfgB, lb)
	if cfgA.Nodes.AdvertiseURL == cfgB.Nodes.AdvertiseURL {
		t.Fatal("the replicas share an advertise URL")
	}
	t.Logf("replica A %s (advertises %s), replica B %s (advertises %s)", a.base, cfgA.Nodes.AdvertiseURL, b.base, cfgB.Nodes.AdvertiseURL)

	session := &client{t: t, base: a.base, key: cfg.Identity.PublishableKey}
	apiA := adminKey(t, session, a.base)
	apiB := &client{t: t, base: b.base, key: cfg.Identity.PublishableKey, token: apiA.token}

	// 2. A node process in a Linux container joins through replica A with a
	// token an admin's write key minted, scoped to probe as the Add node
	// panel would. It runs what the rendered command hands loomctl: the
	// token in LOOMPLANE_JOIN_TOKEN and node.sh's arguments, plus --url as
	// node.sh adds it. --foreground, --name and --state-dir are this test's
	// own. It runs as root, which is fine here.
	line := mintInstallLine(t, apiA, publicURL, map[string]any{"endpoints": []string{"probe"}})
	if !slices.Equal(line.args, []string{"--allow", "probe"}) {
		t.Fatalf("the rendered command passes %q, want --allow probe", line.args)
	}
	name := "e2e-" + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
	join := append([]string{"create", "--add-host=host.docker.internal:host-gateway", "-e", "LOOMPLANE_JOIN_TOKEN=" + line.token, nodesImage,
		"/loomctl", "node", "join", "--url", publicURL}, line.args...)
	join = append(join, "--foreground", "--name", name, "--state-dir", nodeStateDir)
	container := docker(t, join...)
	t.Cleanup(func() {
		out, rerr := exec.CommandContext(context.Background(), "docker", "rm", "-f", container).CombinedOutput()
		if rerr != nil {
			t.Logf("remove the container: %v\n%s", rerr, out)
		}
	})
	docker(t, "cp", loomctl, container+":/loomctl")
	docker(t, "start", container)
	logs := func() string {
		out, _ := exec.CommandContext(context.Background(), "docker", "logs", container).CombinedOutput()
		return string(out)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("node container output:\n%s", logs())
		}
	})

	var node nodes.Node
	joined := waitUntil(t, 30*time.Second, "the node coming online", func() bool {
		var list struct {
			Nodes []nodes.Node `json:"nodes"`
		}
		apiB.must(http.StatusOK, http.MethodGet, "/api/v1/nodes", nil, &list)
		for _, n := range list.Nodes {
			if n.Name == name && n.Status == nodes.StatusOnline {
				node = n
				return true
			}
		}
		return false
	})
	t.Logf("the node joined and came online after %s as %s", joined.Round(time.Millisecond), node.ID)
	if !slices.Equal(node.Endpoints, []nodeproto.Endpoint{nodeproto.EndpointProbe}) {
		t.Fatalf("the node allows %v, want the token's scope [probe]", node.Endpoints)
	}
	if !a.hub.Online(node.ID) || b.hub.Online(node.ID) {
		t.Fatalf("the node's session: replica A holds it %v, replica B holds it %v; want A only", a.hub.Online(node.ID), b.hub.Online(node.ID))
	}

	// 3. From replica B, which holds no session for the node, a probe stream
	// opens through the hop to replica A and returns the node's inventory.
	t.Run("opens a probe stream through the other replica", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		conn, oerr := b.hub.Open(ctx, node.OrgID, node.ID, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointProbe})
		if oerr != nil {
			t.Fatalf("open a probe stream from replica B: %v", oerr)
		}
		defer conn.Close()
		var inv nodeproto.Inventory
		if derr := json.NewDecoder(conn).Decode(&inv); derr != nil {
			t.Fatalf("read the inventory: %v", derr)
		}
		if !strings.Contains(strings.ToLower(inv.OS), "linux") || inv.Arch != arch || inv.Kernel == "" || inv.CPUs < 1 {
			t.Fatalf("inventory %+v is not a Linux %s container's", inv, arch)
		}
		t.Logf("replica B read the inventory through the hop: %s/%s kernel %s, %d cpus, %d MiB", inv.OS, inv.Arch, inv.Kernel, inv.CPUs, inv.MemoryMiB)
		// The same call on replica A, which holds the session, agrees.
		local, lerr := a.hub.Open(ctx, node.OrgID, node.ID, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointProbe})
		if lerr != nil {
			t.Fatalf("open a probe stream on replica A: %v", lerr)
		}
		defer local.Close()
		var direct nodeproto.Inventory
		if derr := json.NewDecoder(local).Decode(&direct); derr != nil {
			t.Fatalf("read the inventory on A: %v", derr)
		}
		if direct.Hostname != inv.Hostname || direct.Kernel != inv.Kernel {
			t.Fatalf("A saw %+v, B saw %+v", direct, inv)
		}
	})

	// 5. loomctl node allow docker, in the container, through the control
	// socket: on Linux that is the SO_PEERCRED check, for real. Either
	// replica then sees docker among the node's endpoints.
	t.Run("allows docker through the control socket", func(t *testing.T) {
		if slices.Contains(node.Endpoints, nodeproto.EndpointDocker) {
			t.Fatalf("docker was allowed before the test asked: %v", node.Endpoints)
		}
		out := docker(t, "exec", container, "/loomctl", "node", "allow", "docker", "--state-dir", nodeStateDir)
		t.Logf("loomctl node allow docker: %s", out)
		if !strings.Contains(out, "The running node told Loomplane") {
			t.Fatalf("the allow did not go through the running node: %q", out)
		}
		took := waitUntil(t, 10*time.Second, "docker appearing in the node's endpoints", func() bool {
			return slices.Contains(getNode(t, apiB, node.ID).Endpoints, nodeproto.EndpointDocker) &&
				slices.Contains(getNode(t, apiA, node.ID).Endpoints, nodeproto.EndpointDocker)
		})
		t.Logf("docker showed in Node.Endpoints on both replicas after %s: %v", took.Round(time.Millisecond), getNode(t, apiB, node.ID).Endpoints)
	})

	// 6. Revoking through replica B, which does not hold the node's session,
	// makes the node exit with 77 on its own.
	t.Run("revokes through the other replica and the node exits 77", func(t *testing.T) {
		exited := make(chan string, 1)
		go func() {
			out, werr := exec.CommandContext(context.Background(), "docker", "wait", container).CombinedOutput()
			if werr != nil {
				out = []byte("error: " + werr.Error() + " " + string(out))
			}
			exited <- strings.TrimSpace(string(out))
		}()
		begin := time.Now()
		apiB.must(http.StatusOK, http.MethodPost, "/api/v1/nodes/"+node.ID+"/revoke", nil, nil)
		select {
		case code := <-exited:
			took := time.Since(begin)
			t.Logf("revoked on replica B; the node container exited with %s after %s", code, took.Round(time.Millisecond))
			if code != strconv.Itoa(revokedExit) {
				t.Fatalf("exit code %s, want %d", code, revokedExit)
			}
			if took > revokeSlack {
				t.Fatalf("the node took %s to exit, want within 2s (slack %s)", took, revokeSlack)
			}
			if took > 2*time.Second {
				t.Errorf("the node took %s to exit, over the 2s target", took)
			}
		case <-time.After(revokeSlack + 10*time.Second):
			t.Fatalf("the node did not exit after it was revoked")
		}
		if n := getNode(t, apiA, node.ID); n.Status != nodes.StatusRevoked || n.RevokedAt == nil {
			t.Fatalf("node after revoke: status %q, revoked_at %v", n.Status, n.RevokedAt)
		}
		if _, oerr := b.hub.Open(t.Context(), node.OrgID, node.ID, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointProbe}); oerr == nil {
			t.Fatal("a stream opened to a revoked node")
		}
	})
}
