//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/forge"

	infraext "github.com/xraph/loomplane/extensions/cloudinfra"
	nodesext "github.com/xraph/loomplane/extensions/nodes"
	"github.com/xraph/loomplane/internal/buildinfo"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/nodes"
	apiclient "github.com/xraph/loomplane/pkg/client"
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

// An admin sees the join tokens no machine has used yet, with who made them
// and from which key, never a secret, and can cancel one so no machine can
// join with it. Joins from one address are rate-limited on the limiter every
// replica shares.
func TestNodes_listCancelAndLimitJoinTokens(t *testing.T) {
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := nodesConfig(t, "http://"+l.Addr().String())
	a := startReplica(t, cfg, l)
	api := adminKey(t, &client{t: t, base: a.base, key: cfg.Identity.PublishableKey}, a.base)
	keep := mintJoinToken(t, api)
	var scoped struct {
		Token string `json:"token"`
	}
	api.must(http.StatusCreated, http.MethodPost, "/api/v1/nodes/join-tokens", map[string]any{"labels": map[string]string{"zone": "a"}, "endpoints": []string{"docker"}}, &scoped)

	type pending struct {
		ID            string               `json:"id"`
		MintedBy      string               `json:"minted_by"`
		MintedByKeyID string               `json:"minted_by_key_id"`
		Labels        map[string]string    `json:"labels"`
		Endpoints     []nodeproto.Endpoint `json:"endpoints"`
		ExpiresAt     time.Time            `json:"expires_at"`
	}
	var list struct {
		Tokens []pending `json:"tokens"`
	}
	api.must(http.StatusOK, http.MethodGet, "/api/v1/nodes/join-tokens", nil, &list)
	if len(list.Tokens) != 2 {
		t.Fatalf("listed %d tokens, want 2", len(list.Tokens))
	}
	newest := list.Tokens[0]
	if newest.MintedBy == "" || newest.MintedByKeyID == "" || newest.Labels["zone"] != "a" || len(newest.Endpoints) != 1 || newest.ExpiresAt.IsZero() {
		t.Fatalf("newest token %+v", newest)
	}

	api.must(http.StatusNoContent, http.MethodDelete, "/api/v1/nodes/join-tokens/"+newest.ID, nil, nil)
	api.must(http.StatusNotFound, http.MethodDelete, "/api/v1/nodes/join-tokens/"+newest.ID, nil, nil)
	api.must(http.StatusOK, http.MethodGet, "/api/v1/nodes/join-tokens", nil, &list)
	if len(list.Tokens) != 1 || list.Tokens[0].ID == newest.ID {
		t.Fatalf("after the cancel: %+v", list.Tokens)
	}

	join := func(token string) int {
		pub, priv, kerr := ed25519.GenerateKey(rand.Reader)
		if kerr != nil {
			t.Fatal(kerr)
		}
		return api.do(http.MethodPost, "/api/v1/nodes/join", nodesext.NodeJoin{
			Token: token, PublicKey: base64.StdEncoding.EncodeToString(pub),
			Signature: base64.StdEncoding.EncodeToString(nodeproto.SignJoin(priv, token, "")),
			Inventory: nodeproto.Inventory{OS: "linux", Arch: "amd64"}, NodeVersion: buildinfo.Version, ProtocolVersion: nodeproto.Version,
		}, nil)
	}
	if got := join(scoped.Token); got != http.StatusUnauthorized {
		t.Fatalf("join with a cancelled token: %d", got)
	}
	if got := join(keep); got != http.StatusCreated {
		t.Fatalf("join with the kept token: %d", got)
	}
	// Two joins counted so far; the rest of this address's minute goes on
	// spent tokens, then it is refused before the token is looked at.
	for i := 2; i < cfg.Nodes.JoinRateLimit; i++ {
		if got := join(keep); got != http.StatusUnauthorized {
			t.Fatalf("join %d: %d", i, got)
		}
	}
	if got := join(keep); got != http.StatusTooManyRequests {
		t.Fatalf("join past the limit of %d: %d", cfg.Nodes.JoinRateLimit, got)
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

// e2eControlPlane is one run of the control plane the node test stops and
// starts again on the same address.
type e2eControlPlane struct {
	run     *running
	hub     *nodes.Hub
	base    string
	stopped bool
}

func (c *e2eControlPlane) stop() {
	if !c.stopped {
		c.stopped = true
		c.run.stop()
	}
}

// bootE2EControlPlane starts Loomplane on l. Route checks resolve names
// through resolver, the zone's own nameserver, as they would in public DNS.
func bootE2EControlPlane(t *testing.T, cfg config.Config, l net.Listener, resolver cloudinfra.DomainResolver) *e2eControlPlane {
	t.Helper()
	r := start(t, cfg, func(srv *httptest.Server) {
		_ = srv.Listener.Close()
		srv.Listener = l
	})
	cp := &e2eControlPlane{run: r, base: "http://127.0.0.1:" + strconv.Itoa(l.Addr().(*net.TCPAddr).Port)} //nolint:forcetypeassert // a tcp listener
	t.Cleanup(cp.stop)
	app := r.app.(forge.App) //nolint:forcetypeassert // start returns a forge.App
	// The service exists once the app has started. Nothing has asked it
	// to check a route yet, so it reads the resolver only from here on.
	svc, err := forge.InjectType[*cloudinfra.Service](app.Container())
	if err != nil {
		t.Fatalf("the cloud infrastructure service is not in the container: %v", err)
	}
	svc.Resolver = resolver
	hub, err := forge.InjectType[*nodes.Hub](app.Container())
	if err != nil {
		t.Fatalf("the nodes hub is not in the container: %v", err)
	}
	cp.hub = hub
	return cp
}

// e2eStep runs one step of the node test as a subtest and logs how long it
// took. A failed step ends the test.
func e2eStep(t *testing.T, name string, step func(t *testing.T)) {
	t.Helper()
	begin := time.Now()
	if !t.Run(name, step) {
		t.FailNow()
	}
	t.Logf("step %q passed in %s", name, time.Since(begin).Round(time.Second))
}

// waitE2E polls cond every two seconds until it holds or limit passes, and
// reports why it last didn't.
func waitE2E(t *testing.T, limit time.Duration, what string, cond func() (bool, string)) time.Duration {
	t.Helper()
	begin := time.Now()
	last := ""
	for time.Since(begin) < limit {
		ok, why := cond()
		if ok {
			return time.Since(begin)
		}
		if why != last {
			t.Logf("waiting for %s: %s", what, why)
			last = why
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("%s did not happen within %s; last: %s", what, limit, last)
	return 0
}

// waitE2EDeployment waits for a plan's deployment to settle.
func waitE2EDeployment(t *testing.T, c *client, plan planBody) deploymentBody {
	t.Helper()
	var d deploymentBody
	waitE2E(t, 20*time.Minute, "the deployment", func() (bool, string) {
		d = deploymentBody{}
		if c.do(http.MethodGet, "/api/v1/plans/"+plan.ID+"/deployment", nil, &d) != http.StatusOK {
			return false, "no deployment yet"
		}
		return d.Status != "running" && d.Status != "", d.Status
	})
	return d
}

// e2ePlan plans a manifest for production, approves it and waits for the
// deployment, which must succeed.
func e2ePlan(t *testing.T, c *client, project, manifest string) deploymentBody {
	t.Helper()
	base := "/api/v1/projects/" + project
	mustE2E(t, c, http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": manifest}, nil)
	var plan planBody
	mustE2E(t, c, http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if plan.Status != "ready" || !plan.Executable {
		t.Fatalf("plan %+v", plan)
	}
	mustE2E(t, c, http.StatusOK, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": plan.Revision}, nil)
	d := waitE2EDeployment(t, c, plan)
	if d.Status != "succeeded" {
		t.Fatalf("deployment %+v", d)
	}
	return d
}

// A node joins a Cloud from Docker in Docker, as a user would run it, and
// is adopted. A service builds from source on it, with no registry, and a
// domain in a managed zone serves it over HTTPS with a certificate from an
// ACME CA. When the node's public address changes, the record follows, and
// HTTPS keeps answering with the control plane stopped. Started again, the
// control plane gets the node back.
//
// It runs on Linux only, where the test reaches containers by address as a
// control plane reaches a node, and only when LOOMPLANE_TEST_NODE_E2E=1.
func TestNodeDeploysAndServes(t *testing.T) {
	if os.Getenv("LOOMPLANE_TEST_NODE_E2E") != "1" {
		t.Skip("set LOOMPLANE_TEST_NODE_E2E=1 to run the node end to end test (Docker in Docker, Pebble, a real build)")
	}
	if runtime.GOOS != "linux" {
		t.Skipf("the node end to end test needs Linux, where this machine reaches containers by their addresses; on %s, run it in a Linux container on the same Docker with %s set to that container's name", runtime.GOOS, e2eSelfEnv)
	}
	if os.Getenv("LOOMPLANE_TEST_DATABASE_URL") == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL to run the integration tests")
	}
	arch := dockerArch(t)
	loomctl := buildLoomctl(t, arch)
	f := newE2EFixture(t)
	f.startCloudflare(arch)
	f.startPebble()
	// Loomplane checks a route's certificate against the system roots, and
	// a visitor would too: both trust the CA's issuing root here.
	issuedRoot := f.issuedRoot()
	trustForThisProcess(t, issuedRoot)
	issued := x509.NewCertPool()
	issued.AppendCertsFromPEM(issuedRoot)
	t.Logf("networks: control %s, public A %s, public B %s; Pebble at %s, the zone's nameserver at %s, the control plane at %s", f.ctl.Subnet, f.pubA.Subnet, f.pubB.Subnet, f.pebbleIP, f.cfIP, f.controlHost)

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // a tcp listener
	publicURL := "http://" + net.JoinHostPort(f.controlHost, strconv.Itoa(port))
	cfg := nodesConfig(t, publicURL)
	cfg.GitHub.APIURL = fakeGitHubRepos(t, map[string][]byte{"acme/hello": helloSource(t)}).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Clouds.CloudflareEndpoint = "http://" + net.JoinHostPort(f.cfIP, "8081")
	cfg.Clouds.ACMEDirectory = "https://" + net.JoinHostPort(f.pebbleIP, "14000") + "/dir"
	cfg.Clouds.ACMERootsFile = f.caRootFile
	cp := bootE2EControlPlane(t, cfg, l, f.zoneResolver())
	c := &client{t: t, base: cp.base, key: cfg.Identity.PublishableKey}
	api := adminKey(t, c, cp.base)

	var cloud struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}
	mustE2E(t, c, http.StatusCreated, http.MethodPost, "/api/v1/clouds", map[string]string{"name": "e2e"}, &cloud)
	cloudBase := "/api/v1/clouds/" + cloud.ID
	addressA, addressB := f.pubA.host(10), f.pubB.host(10)
	const nodeName = "e2e-node"
	var node nodes.Node

	e2eStep(t, "1 a node joins the e2e Cloud", func(t *testing.T) {
		f.startNode()
		f.docker("cp", loomctl, f.node+":/loomctl")
		// What a user runs on the machine: node join with Loomplane's
		// address and their admin key in the environment. It mints the join
		// token, scoped to the Cloud, and spends it.
		f.docker("exec", "-d", "-e", "LOOMPLANE_URL="+publicURL, "-e", "LOOMPLANE_API_KEY="+api.token, f.node, "sh", "-c",
			"exec /loomctl node join --cloud "+cloud.ID+" --builder dedicated --allow docker,dial,probe --name "+nodeName+" --state-dir /var/lib/e2e-node --foreground >/var/log/loomplane-node.log 2>&1")
		took := waitE2E(t, 2*time.Minute, "the node coming online", func() (bool, string) {
			var list struct {
				Nodes []nodes.Node `json:"nodes"`
			}
			mustE2E(t, c, http.StatusOK, http.MethodGet, "/api/v1/nodes", nil, &list)
			for _, n := range list.Nodes {
				if n.Name == nodeName {
					node = n
					return n.Status == nodes.StatusOnline, string(n.Status)
				}
			}
			return false, "not joined"
		})
		t.Logf("node %s joined and came online after %s, allowing %v", node.ID, took.Round(time.Second), node.Endpoints)
	})

	var host cloudinfra.Server
	e2eStep(t, "2 the node is adopted as a server", func(t *testing.T) {
		took := waitE2E(t, 5*time.Minute, "the adoption", func() (bool, string) {
			var adoptions infraext.NodeAdoptionList
			mustE2E(t, c, http.StatusOK, http.MethodGet, cloudBase+"/node-adoptions", nil, &adoptions)
			if len(adoptions.Adoptions) != 1 {
				return false, fmt.Sprintf("%d adoptions", len(adoptions.Adoptions))
			}
			a := adoptions.Adoptions[0]
			if a.Status != cloudinfra.NodeAdoptionApplied {
				return false, a.Status + ": " + a.Reason
			}
			var servers struct {
				Servers []cloudinfra.Server `json:"servers"`
			}
			mustE2E(t, c, http.StatusOK, http.MethodGet, cloudBase+"/servers", nil, &servers)
			for _, s := range servers.Servers {
				if s.ID == a.ServerID {
					host = s
					return s.Status == "ready", "server " + s.Status
				}
			}
			return false, "no server yet"
		})
		if host.Ownership != "adopted" || host.Name != nodeName || host.Health.DockerID == "" {
			t.Fatalf("adopted server %+v", host)
		}
		t.Logf("adopted as server %s (%s, Docker %s) after %s", host.ID, host.Ownership, host.Health.DockerID, took.Round(time.Second))
		took = waitE2E(t, 4*time.Minute, "the node's public address confirmed", func() (bool, string) {
			node = getNode(t, c, node.ID)
			return node.PublicAddress == addressA && node.PublicAddressConfirmedAt != nil && node.IngressMode == "direct",
				fmt.Sprintf("address %q confirmed %v, ingress %q (%s), candidates %v", node.PublicAddress, node.PublicAddressConfirmedAt, node.IngressMode, node.IngressDetail, node.Inventory.PublicAddresses)
		})
		t.Logf("public address %s confirmed from outside after another %s; ingress mode %s", node.PublicAddress, took.Round(time.Second), node.IngressMode)
	})

	var project, environment string
	var image string
	e2eStep(t, "3 hello builds from source on the node with no registry", func(t *testing.T) {
		var p struct {
			ID string `json:"id"`
		}
		mustE2E(t, c, http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "hello", "repository_url": "https://github.com/acme/hello"}, &p)
		project = p.ID
		base := "/api/v1/projects/" + project
		analyze(t, cfg, c, project)
		var envs struct {
			Environments []struct{ ID, Name string } `json:"environments"`
		}
		mustE2E(t, c, http.StatusOK, http.MethodGet, base+"/environments", nil, &envs)
		for _, e := range envs.Environments {
			if e.Name == "production" {
				environment = e.ID
			}
		}
		mustE2E(t, c, http.StatusCreated, http.MethodPost, base+"/variables", map[string]any{"key": "GREETING", "value": "built here", "environment": "production"}, nil)
		begin := time.Now()
		d := e2ePlan(t, c, project, helloManifest(host.RuntimeConnectionID))
		image = d.Steps[0].Image
		t.Logf("deployment %s built and ran hello in %s: image %s", d.ID, time.Since(begin).Round(time.Second), image)
		var conns struct {
			Connections []struct {
				Cloud string `json:"cloud"`
			} `json:"connections"`
		}
		mustE2E(t, c, http.StatusOK, http.MethodGet, "/api/v1/connections", nil, &conns)
		for _, conn := range conns.Connections {
			if conn.Cloud == "registry" {
				t.Fatal("the organization has a registry connection")
			}
		}
		// The image is on the node's own Docker, and so is the container
		// running it.
		f.docker("exec", f.node, "docker", "image", "inspect", image)
		running := f.docker("exec", f.node, "docker", "ps", "-q", "--filter", "label=loomplane.project="+project)
		if running == "" {
			t.Fatal("no hello container on the node")
		}
		answer := f.docker("exec", f.node, "docker", "exec", strings.Fields(running)[0], "wget", "-qO-", "http://127.0.0.1:8080/")
		if answer != "hello from a node: built here" {
			t.Fatalf("hello answered %q", answer)
		}
		t.Logf("hello answers on the node: %q", answer)
	})

	hostname := "web." + e2eZone
	var route cloudinfra.Route
	e2eStep(t, "4 a route in a managed zone serves HTTPS with a Pebble certificate", func(t *testing.T) {
		base := "/api/v1/projects/" + project
		var dns struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		mustE2E(t, c, http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "cloudflare", "name": "e2e DNS", "credential": e2eCloudflareToken, "settings": map[string]string{"account_id": e2eAccount}}, &dns)
		if dns.Status != "ready" {
			t.Fatalf("Cloudflare connection is %s", dns.Status)
		}
		var zone cloudinfra.Zone
		mustE2E(t, c, http.StatusCreated, http.MethodPost, "/api/v1/dns-zones", cloudinfra.ZoneInput{Name: e2eZone, Mode: "managed", ConnectionID: dns.ID, ProjectIDs: []string{project}}, &zone)
		if zone.VerifiedAt.IsZero() {
			mustE2E(t, c, http.StatusOK, http.MethodPost, "/api/v1/dns-zones/"+zone.ID+"/verify", nil, &zone)
		}
		// A route reaches a service placed through a Cloud: a pool on the
		// node's server, a target on the pool, and the environment on the
		// Cloud. A server joins a pool only once its direct workloads are
		// gone, so the direct hello goes first. The routed service, web,
		// runs an image pinned by digest, as a Cloud placement requires;
		// hello's local build has no digest without a registry.
		e2ePlan(t, c, project, "version: 1\nservices: {}\n")
		var pool cloudinfra.Pool
		mustE2E(t, c, http.StatusOK, http.MethodPost, cloudBase+"/pools", cloudinfra.PoolInput{Name: "nodes", Members: []cloudinfra.PoolMember{{ServerID: host.ID, CPUMillis: 500, MemoryBytes: 256 << 20}}}, &pool)
		mustE2E(t, c, http.StatusOK, http.MethodPut, cloudBase+"/projects/"+project, map[string]any{"target_ids": []string{}}, nil)
		var target cloudprofiles.Target
		mustE2E(t, c, http.StatusCreated, http.MethodPost, base+"/clouds/"+cloud.ID+"/targets", cloudprofiles.CreateTargetInput{Name: "node", Provider: "server", Runtime: "server", PoolID: pool.ID}, &target)
		var preview cloudprofiles.Preview
		mustE2E(t, c, http.StatusOK, http.MethodPost, base+"/environments/"+environment+"/cloud/preview", apiclient.EnvironmentCloudInput{CloudID: cloud.ID}, &preview)
		if result := waitCloudAPI(t, c, base, applyCloudAPI(t, c, base, &preview, "assign-e2e").ID); result.Status != cloudprofiles.SyncSucceeded {
			t.Fatalf("the environment's Cloud assignment: %+v", result)
		}
		mustE2E(t, c, http.StatusCreated, http.MethodPost, base+"/variables", map[string]any{"key": "WHOAMI_NAME", "value": webName, "environment": "production"}, nil)
		e2ePlan(t, c, project, webManifest)
		var op cloudinfra.RouteOperation
		mustE2E(t, c, http.StatusOK, http.MethodPost, base+"/domain-routes/preview", cloudinfra.RouteInput{ZoneID: zone.ID, CloudID: cloud.ID, EnvironmentID: environment, Service: "web", Hostname: hostname, Port: 80}, &op)
		if op.DesiredRecord.Type != "A" || op.DesiredRecord.Value != addressA {
			t.Fatalf("the route would write %+v, want an A record for %s", op.DesiredRecord, addressA)
		}
		mustE2E(t, c, http.StatusAccepted, http.MethodPost, base+"/route-operations/"+op.ID+"/apply", cloudinfra.ApplyInput{Review: op.Review}, &op)
		took := waitE2E(t, 6*time.Minute, "the route", func() (bool, string) {
			mustE2E(t, c, http.StatusOK, http.MethodGet, base+"/route-operations/"+op.ID, nil, &op)
			if op.Status == "waiting" {
				c.do(http.MethodPost, base+"/route-operations/"+op.ID+"/retry", nil, nil)
			}
			if op.Status == "failed" {
				t.Fatalf("the route operation failed: %s", op.Message)
			}
			return op.Status == "succeeded", op.Status + ": " + op.Message
		})
		route = op.Route
		t.Logf("route %s ready after %s: %s -> %s", route.ID, took.Round(time.Second), hostname, route.Binding.Address)
		addrs, lookupErr := f.zoneResolver().LookupHost(t.Context(), hostname)
		if lookupErr != nil || len(addrs) != 1 || addrs[0] != addressA {
			t.Fatalf("the zone answers %v %v for %s, want %s", addrs, lookupErr, hostname, addressA)
		}
		var body string
		var leaf *x509.Certificate
		waitE2E(t, 2*time.Minute, "HTTPS at "+addressA, func() (bool, string) {
			var getErr error
			body, leaf, getErr = nodeHTTPS(t, addressA, hostname, issued)
			if getErr != nil {
				return false, getErr.Error()
			}
			return true, ""
		})
		if !strings.Contains(body, "Name: "+webName) {
			t.Fatalf("HTTPS answered %q", body)
		}
		t.Logf("https://%s at %s answers as %q with a certificate for %v from %q, which chains to Pebble's root", hostname, addressA, webName, leaf.DNSNames, leaf.Issuer.CommonName)
	})

	e2eStep(t, "5 the record follows the node's new address", func(t *testing.T) {
		// The machine gets a second public address, then loses the first.
		f.docker("network", "connect", "--ip", addressB, f.pubB.Name, f.node)
		waitE2E(t, 3*time.Minute, "the node reporting both addresses", func() (bool, string) {
			n := getNode(t, c, node.ID)
			return slices.Contains(n.Inventory.PublicAddresses, addressA) && slices.Contains(n.Inventory.PublicAddresses, addressB), fmt.Sprint(n.Inventory.PublicAddresses)
		})
		begin := time.Now()
		f.docker("network", "disconnect", f.pubA.Name, f.node)
		waitE2E(t, 6*time.Minute, "the route following the node", func() (bool, string) {
			n := getNode(t, c, node.ID)
			var routes infraext.DomainRouteList
			mustE2E(t, c, http.StatusOK, http.MethodGet, "/api/v1/projects/"+project+"/domain-routes", nil, &routes)
			current := cloudinfra.Route{}
			for _, r := range routes.Routes {
				if r.ID == route.ID {
					current = *r
				}
			}
			var status cloudinfra.RouteStatus
			mustE2E(t, c, http.StatusOK, http.MethodGet, "/api/v1/projects/"+project+"/domain-routes/"+route.ID+"/status", nil, &status)
			addrs, _ := f.zoneResolver().LookupHost(t.Context(), hostname)
			done := n.PublicAddress == addressB && current.Binding.Address == addressB && slices.Equal(addrs, []string{addressB})
			return done, fmt.Sprintf("node address %s (%s), route %s at %s (%s), zone answers %v", n.PublicAddress, n.IngressDetail, current.Status, current.Binding.Address, status.Message, addrs)
		})
		t.Logf("the node's address, the route and the zone's A record moved to %s after %s", addressB, time.Since(begin).Round(time.Second))
		waitE2E(t, time.Minute, "HTTPS at "+addressB, func() (bool, string) {
			body, _, getErr := nodeHTTPS(t, addressB, hostname, issued)
			if getErr != nil {
				return false, getErr.Error()
			}
			return strings.Contains(body, "Name: "+webName), body
		})
		if _, _, err := nodeHTTPS(t, addressA, hostname, issued); err == nil {
			t.Fatalf("the old address %s still answers", addressA)
		}
	})

	e2eStep(t, "6 HTTPS answers with the control plane stopped", func(t *testing.T) {
		cp.stop()
		for range 3 {
			body, _, err := nodeHTTPS(t, addressB, hostname, issued)
			if err != nil || !strings.Contains(body, "Name: "+webName) {
				t.Fatalf("with the control plane stopped, HTTPS answered %q %v", body, err)
			}
			time.Sleep(2 * time.Second)
		}
		t.Logf("https://%s kept answering at %s with no control plane", hostname, addressB)
	})

	e2eStep(t, "7 the node reconnects to the restarted control plane", func(t *testing.T) {
		again, listenErr := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "0.0.0.0:"+strconv.Itoa(port))
		if listenErr != nil {
			t.Fatal(listenErr)
		}
		begin := time.Now()
		restarted := bootE2EControlPlane(t, cfg, again, f.zoneResolver())
		c.base = restarted.base
		took := waitE2E(t, 3*time.Minute, "the node reconnecting", func() (bool, string) {
			n := getNode(t, c, node.ID)
			return restarted.hub.Online(node.ID) && n.Status == nodes.StatusOnline, string(n.Status)
		})
		t.Logf("the node reconnected %s after the control plane started again (%s since the restart began)", took.Round(time.Second), time.Since(begin).Round(time.Second))
		if body, _, err := nodeHTTPS(t, addressB, hostname, issued); err != nil || !strings.Contains(body, "Name: "+webName) {
			t.Fatalf("after the restart, HTTPS answered %q %v", body, err)
		}
	})
}

// helloManifest places hello on the node's server connection, built from
// source there.
func helloManifest(connection string) string {
	return fmt.Sprintf(`version: 1
services:
  hello:
    source: go:cmd/hello
    provider: server
    target: %s
    public: false
    port: 8080
    health_path: /
    build: {context: .}
`, connection)
}

// webName is the name web answers with.
const webName = "served-from-a-node"

// webManifest places web on the node through the Cloud's target. It runs
// the whoami image the shared Cloud tests use, pinned by digest.
const webManifest = `version: 1
services:
  web:
    source: go:cmd/hello
    cloud_target: node
    public: false
    port: 80
    health_path: /
    size: {cpu: 0.2, memory: 128}
    build: {image: traefik/whoami:v1.11.0@sha256:200689790a0a0ea48ca45992e0450bc26ccab5307375b41c84dfc4f2475937ab}
`
