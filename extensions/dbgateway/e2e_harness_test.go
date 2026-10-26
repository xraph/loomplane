//go:build integration

package dbgateway

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/dbgateway/plane"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/security"
)

// e2eSuffix is the zone the end-to-end gateway names its databases under, so
// the route for resource orders is orders.test.db.example.com.
const e2eSuffix = "test.db.example.com"

// e2eHost is the orders route's logical host.
const e2eHost = "orders." + e2eSuffix

// fakeInstancePrefix marks the instances the rig reports on its own, which
// stand in for other running copies of the gateway.
const fakeInstancePrefix = "e2e-peer-"

// e2eRig is a real control plane (the Service over the Store on its own test
// database, behind the real routes) with one gateway recorded and its
// orders route pointed at a throwaway database, and the gateway program
// built from dbgateway.Source().
type e2eRig struct {
	t       *testing.T
	store   *dbgateway.Store
	svc     *dbgateway.Service
	router  forge.Router
	handler swapHandler // serves router, swapped when the control plane restarts
	secrets memSecrets
	gw      dbgateway.Gateway
	route   dbgateway.RouteRecord
	bin     string
	env     []string
	vars    map[string]string // env as a map, before the trust keys change it

	// The backend: a role and a database of their own.
	user, password, database string
	direct                   string // the backend DSN, straight to Postgres
}

// newE2ERig sets everything up. peers live instances are reported for the
// gateway besides its own, every few seconds for as long as the test runs,
// which shrinks each instance's share of the backend's connections.
func newE2ERig(t *testing.T, peers int) *e2eRig {
	t.Helper()
	g := &e2eRig{t: t}
	g.store = dbgateway.NewStore(testDB(t))
	g.bin = buildGateway(t)
	g.backend()
	ctx := t.Context()

	// The control plane's signing key, in a version 1 file.
	now := time.Now().UTC()
	g.secrets = memSecrets{deployments.ResourceURLKey("org_1", "env_1", "orders"): []byte(g.direct)}
	srv := httptest.NewServer(&g.handler)
	t.Cleanup(srv.Close)
	g.restartControlPlane(newE2EKey(t, now.Add(-time.Hour), now.Add(24*time.Hour)).fileV1())

	g.mintGateway(ctx, srv.URL)

	for i := range peers {
		g.reportPeer(ctx, i)
	}
	if peers > 0 {
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			tick := time.NewTicker(5 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tick.C:
					for i := range peers {
						g.reportPeer(context.Background(), i)
					}
				}
			}
		}()
		t.Cleanup(func() { close(stop); wg.Wait() })
	}
	return g
}

// swapHandler serves whichever handler was stored last.
type swapHandler struct{ h atomic.Pointer[http.Handler] }

func (s *swapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*s.h.Load()).ServeHTTP(w, r)
}

// e2eKey is a control-plane signing key and the window it's written with.
type e2eKey struct {
	seed        []byte
	from, until time.Time
}

func newE2EKey(t *testing.T, from, until time.Time) e2eKey {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	return e2eKey{seed: seed, from: from, until: until}
}

// id is the key's ID, as snapshots name it.
func (k e2eKey) id() string {
	return plane.TrustKey{PublicKey: ed25519.NewKeyFromSeed(k.seed).Public().(ed25519.PublicKey)}.ID()
}

func (k e2eKey) entry() map[string]any {
	return map[string]any{"private_key": base64.StdEncoding.EncodeToString(k.seed), "not_before": k.from, "not_after": k.until}
}

// fileV1 is a version 1 key file holding k.
func (k e2eKey) fileV1() map[string]any {
	m := k.entry()
	m["version"] = 1
	return m
}

// e2eKeyFile is a version 2 key file holding keys.
func e2eKeyFile(keys ...e2eKey) map[string]any {
	entries := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		entries = append(entries, k.entry())
	}
	return map[string]any{"version": 2, "keys": entries}
}

// restartControlPlane starts the control plane afresh with the signing key
// file body, read the way the extension reads dbgateway.signing_key_file,
// as Loomplane does when it restarts. The store, and with it every
// snapshot version, carries over; requests already in flight finish on the
// control plane they started on.
func (g *e2eRig) restartControlPlane(body map[string]any) {
	t := g.t
	t.Helper()
	keyFile := filepath.Join(t.TempDir(), "signing-key.json")
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := dbgateway.LoadSigningKeys(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	svc := dbgateway.NewService(g.store, resourceURLs{secrets: g.secrets}, keys, time.Now)
	svc.Authz = catalogAuthz{"usr_admin": security.RoleAdmin}
	svc.OnSkip = func(_ string, skipped []dbgateway.Skipped) { t.Errorf("routes left out of the snapshot: %+v", skipped) }
	router := forge.NewRouter()
	// The long-poll waits 2s rather than 25s, and looks every 25ms. A
	// gateway that stops mid-request cancels it, which is no fault of the
	// control plane's.
	if err = mount(router, &handlers{svc: svc, wait: 2 * time.Second, every: 25 * time.Millisecond, now: time.Now,
		warn: func(msg string, werr error) {
			if !errors.Is(werr, context.Canceled) {
				t.Errorf("%s: %v", msg, werr)
			}
		}}); err != nil {
		t.Fatal(err)
	}
	if err = mountManagement(router, nil, svc); err != nil {
		t.Fatal(err)
	}
	g.svc, g.router = svc, router
	h := router.Handler()
	g.handler.h.Store(&h)
}

// redeploy gives the gateways started from now on the trust keys the
// control plane hands out now, as a deploy does. Gateways already running
// keep the keys they started with.
func (g *e2eRig) redeploy() {
	t := g.t
	t.Helper()
	trust, err := json.Marshal(g.svc.TrustKeys())
	if err != nil {
		t.Fatal(err)
	}
	g.vars["LOOMPLANE_DBGW_TRUST_KEYS"] = string(trust)
	if _, err = plane.ConfigFromEnv(func(k string) string { return g.vars[k] }); err != nil {
		t.Fatalf("the gateway's environment: %v", err)
	}
	g.env = g.env[:0]
	for k, v := range g.vars {
		g.env = append(g.env, k+"="+v)
	}
}

// reportPeer reports one stand-in instance as live.
func (g *e2eRig) reportPeer(ctx context.Context, i int) {
	if _, err := g.svc.Report(ctx, &g.gw, plane.Report{InstanceID: fmt.Sprintf("%s%02d", fakeInstancePrefix, i)}); err != nil && ctx.Err() == nil {
		g.t.Errorf("report a stand-in instance: %v", err)
	}
}

// backend creates a login role and a database it owns in the test Postgres,
// and drops both when the test ends.
func (g *e2eRig) backend() {
	t := g.t
	dsn := testDSN(t)
	ctx := t.Context()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	g.user, g.database = "e2e_app_"+suffix, "e2e_orders_"+suffix
	pw := make([]byte, 16)
	if _, err = rand.Read(pw); err != nil {
		t.Fatal(err)
	}
	g.password = hex.EncodeToString(pw)
	role, db := pgx.Identifier{g.user}.Sanitize(), pgx.Identifier{g.database}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE ROLE "+role+" LOGIN PASSWORD '"+g.password+"'"); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		if _, derr := admin.Exec(bg, "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)"); derr != nil {
			t.Logf("drop the backend database %s: %v", g.database, derr)
		}
		if _, derr := admin.Exec(bg, "DROP ROLE IF EXISTS "+role); derr != nil {
			t.Logf("drop the backend role %s: %v", g.user, derr)
		}
		_ = admin.Close(bg)
	})
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+db+" OWNER "+role); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(g.user, g.password)
	u.Path = "/" + g.database
	u.RawQuery = "sslmode=disable"
	g.direct = u.String()
}

// testDSN is the test Postgres. The tests skip without one.
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the database gateway's end-to-end tests")
	}
	return dsn
}

// mintGateway records the gateway and its orders route and builds its
// environment, the way a deployment does: keys minted under a fresh gateway
// ID, the public halves recorded, the private halves in the environment, and
// the whole checked by the gateway's own config reader.
func (g *e2eRig) mintGateway(ctx context.Context, controlURL string) {
	t := g.t
	id, err := dbgateway.NewGatewayID()
	if err != nil {
		t.Fatal(err)
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err = rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	signing := ed25519.NewKeyFromSeed(seed)
	sealing, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gw, created, err := g.store.EnsureGateway(ctx, dbgateway.Gateway{
		OrgID: "org_1", ID: id, ProjectID: "proj_1", EnvironmentID: "env_1", Name: "main",
		Provider: "cloud-run", Region: "us-central1", Suffix: e2eSuffix,
		SigningPublicKey: signing.Public().(ed25519.PublicKey), SealingPublicKey: sealing.PublicKey(),
	})
	if err != nil || !created || gw.ID != id {
		t.Fatalf("EnsureGateway: %+v %v %v", gw, created, err)
	}
	g.gw = gw
	if g.route, err = g.store.EnsureRoute(ctx, "org_1", gw.ID, "orders"); err != nil {
		t.Fatal(err)
	}
	g.vars = map[string]string{
		"LOOMPLANE_DBGW_ID":          gw.ID,
		"LOOMPLANE_DBGW_CONTROL_URL": controlURL,
		"LOOMPLANE_DBGW_SUFFIX":      e2eSuffix,
		"LOOMPLANE_DBGW_SIGNING_KEY": base64.StdEncoding.EncodeToString(seed),
		"LOOMPLANE_DBGW_SEALING_KEY": base64.StdEncoding.EncodeToString(sealing.Bytes()),
	}
	g.redeploy()
}

// buildGateway unpacks dbgateway.Source() and builds it, as the cloud's
// builder would, and returns the binary.
func buildGateway(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the gateway, fetching its modules")
	}
	data, _, err := dbgateway.Source()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	for {
		h, nerr := tr.Next()
		if errors.Is(nerr, io.EOF) {
			break
		}
		if nerr != nil {
			t.Fatal(nerr)
		}
		p := filepath.Join(dir, filepath.FromSlash(h.Name))
		if h.Typeflag == tar.TypeDir {
			if err = os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		b, rerr := io.ReadAll(tr)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if err = os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "dbgateway-bin")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".")
	cmd.Dir = filepath.Join(dir, "dbgateway")
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOWORK=off", "CGO_ENABLED=0")
	if out, berr := cmd.CombinedOutput(); berr != nil {
		t.Fatalf("go build: %v\n%s", berr, out)
	}
	return bin
}

// gatewayProc is one running gateway instance.
type gatewayProc struct {
	t    *testing.T
	cmd  *exec.Cmd
	port int
	done chan struct{}
	err  error // the exit, once done is closed

	mu    sync.Mutex
	lines []string
	grew  chan struct{} // closed and replaced whenever a line arrives
}

// start runs the gateway with its state in stateDir and waits until it
// serves: /healthz says ok once it has applied a snapshot.
func (g *e2eRig) start(stateDir string) *gatewayProc {
	t := g.t
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	p := &gatewayProc{t: t, port: port, done: make(chan struct{}), grew: make(chan struct{})}
	// Not t.Context(): that ends before the cleanups run, and this test
	// decides when the gateway gets SIGTERM.
	p.cmd = exec.CommandContext(context.WithoutCancel(t.Context()), g.bin)
	p.cmd.Env = append(append([]string{"PATH=" + os.Getenv("PATH")}, g.env...),
		"PORT="+strconv.Itoa(port), "LOOMPLANE_DBGW_STATE_DIR="+stateDir)
	stderr, err := p.cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	p.cmd.Stdout = io.Discard
	if err = p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	logged := make(chan struct{})
	go func() {
		defer close(logged)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			p.mu.Lock()
			p.lines = append(p.lines, sc.Text())
			close(p.grew)
			p.grew = make(chan struct{})
			p.mu.Unlock()
		}
	}()
	go func() {
		<-logged
		p.err = p.cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = p.cmd.Process.Kill()
			<-p.done
		}
		if t.Failed() {
			p.mu.Lock()
			t.Logf("gateway on :%d logged:\n%s", port, strings.Join(p.lines, "\n"))
			p.mu.Unlock()
		}
	})
	p.waitHealth(http.StatusOK, 20*time.Second)
	return p
}

// healthz returns the gateway's /healthz status and body, or 0 when it
// doesn't answer.
func (p *gatewayProc) healthz() (int, string) {
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", p.port)) //nolint:noctx // a local probe
	if err != nil {
		return 0, ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

func (p *gatewayProc) waitHealth(status int, within time.Duration) string {
	p.t.Helper()
	deadline := time.Now().Add(within)
	for {
		got, body := p.healthz()
		if got == status {
			return body
		}
		select {
		case <-p.done:
			p.t.Fatalf("the gateway exited (%v) before /healthz said %d", p.err, status)
		default:
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("/healthz said %d %q for %s, want %d", got, body, within, status)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// appliedPattern is the gateway's log line for a snapshot it took.
var appliedPattern = regexp.MustCompile(`database gateway applied (?:a|the last-good) snapshot version=(\d+)`)

// applied is the highest snapshot version the gateway has logged applying.
func (p *gatewayProc) applied() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	var v uint64
	for _, l := range p.lines {
		if m := appliedPattern.FindStringSubmatch(l); m != nil {
			n, _ := strconv.ParseUint(m[1], 10, 64)
			v = max(v, n)
		}
	}
	return v
}

// waitApplied waits until the gateway logs applying a version past after.
func (p *gatewayProc) waitApplied(after uint64, within time.Duration) uint64 {
	p.t.Helper()
	deadline := time.NewTimer(within)
	defer deadline.Stop()
	for {
		p.mu.Lock()
		grew := p.grew
		p.mu.Unlock()
		if v := p.applied(); v > after {
			return v
		}
		select {
		case <-grew:
		case <-p.done:
			p.t.Fatalf("the gateway exited (%v) before applying a snapshot past %d", p.err, after)
		case <-deadline.C:
			p.t.Fatalf("the gateway applied nothing past version %d in %s", after, within)
		}
	}
}

// waitLogged reports whether the gateway logs a line containing text
// within the time given.
func (p *gatewayProc) waitLogged(text string, within time.Duration) bool {
	deadline := time.NewTimer(within)
	defer deadline.Stop()
	for {
		p.mu.Lock()
		grew, found := p.grew, slices.ContainsFunc(p.lines, func(l string) bool { return strings.Contains(l, text) })
		p.mu.Unlock()
		if found {
			return true
		}
		select {
		case <-grew:
		case <-p.done:
			return false
		case <-deadline.C:
			return false
		}
	}
}

// terminate sends SIGTERM, as Cloud Run and ECS do to stop a container.
func (p *gatewayProc) terminate() {
	p.t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		p.t.Fatal(err)
	}
}

// wait waits for the gateway to exit and returns how it did.
func (p *gatewayProc) wait(within time.Duration) error {
	p.t.Helper()
	select {
	case <-p.done:
		return p.err
	case <-time.After(within):
		p.t.Fatalf("the gateway was still running %s after SIGTERM", within)
		return nil
	}
}

// pgConfig is a pgx config for the orders route through the gateway's
// WebSocket at /v2, as user with password. TLS is the WebSocket's job, so
// the Postgres connection inside it asks for none.
func (p *gatewayProc) pgConfig(user, password, database string) *pgx.ConnConfig {
	p.t.Helper()
	cfg, err := pgx.ParseConfig(fmt.Sprintf("postgres://%s:%s@%s:5432/%s?sslmode=disable",
		url.PathEscape(user), url.PathEscape(password), e2eHost, url.PathEscape(database)))
	if err != nil {
		p.t.Fatal(err)
	}
	// Nothing resolves the logical host; the gateway reads it from address.
	cfg.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	cfg.DialFunc = func(ctx context.Context, _, addr string) (net.Conn, error) {
		u := fmt.Sprintf("ws://127.0.0.1:%d/v2?address=%s", p.port, url.QueryEscape(addr))
		c, resp, err := websocket.DefaultDialer.DialContext(ctx, u, nil)
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if errors.Is(err, websocket.ErrBadHandshake) && resp != nil {
			return nil, &handshakeError{status: resp.StatusCode, err: err}
		}
		if err != nil {
			return nil, err
		}
		return &wsNetConn{c: c}, nil
	}
	return cfg
}

// connect opens a pgx connection through the gateway and closes it when the
// test ends.
func (p *gatewayProc) connect(g *e2eRig) *pgx.Conn {
	p.t.Helper()
	c, err := pgx.ConnectConfig(p.t.Context(), p.pgConfig(g.user, g.password, g.database))
	if err != nil {
		p.t.Fatalf("connect through the gateway: %v", err)
	}
	p.t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// handshakeError is a WebSocket upgrade the gateway answered with an HTTP
// status instead, as it does with 503 while it drains.
type handshakeError struct {
	status int
	err    error
}

func (e *handshakeError) Error() string { return fmt.Sprintf("%v (HTTP %d)", e.err, e.status) }
func (e *handshakeError) Unwrap() error { return e.err }

// wsNetConn is a client WebSocket as the byte stream pgx expects: every
// Write is one binary message, and Read carries on across messages.
type wsNetConn struct {
	c   *websocket.Conn
	cur io.Reader
	wmu sync.Mutex
}

func (w *wsNetConn) Read(b []byte) (int, error) {
	for {
		if w.cur == nil {
			kind, r, err := w.c.NextReader()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					return 0, io.EOF
				}
				return 0, err
			}
			if kind != websocket.BinaryMessage {
				return 0, errors.New("the gateway sent a text message")
			}
			w.cur = r
		}
		n, err := w.cur.Read(b)
		if errors.Is(err, io.EOF) {
			w.cur = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (w *wsNetConn) Write(b []byte) (int, error) {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	if err := w.c.WriteMessage(websocket.BinaryMessage, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (w *wsNetConn) Close() error                       { return w.c.Close() }
func (w *wsNetConn) LocalAddr() net.Addr                { return w.c.LocalAddr() }
func (w *wsNetConn) RemoteAddr() net.Addr               { return w.c.RemoteAddr() }
func (w *wsNetConn) SetReadDeadline(t time.Time) error  { return w.c.SetReadDeadline(t) }
func (w *wsNetConn) SetWriteDeadline(t time.Time) error { return w.c.SetWriteDeadline(t) }
func (w *wsNetConn) SetDeadline(t time.Time) error {
	return errors.Join(w.c.SetReadDeadline(t), w.c.SetWriteDeadline(t))
}

// ownInstance returns the gateway's own last report, leaving out the
// stand-in instances, once one satisfies ok.
func (g *e2eRig) ownInstance(within time.Duration, ok func(dbgateway.InstanceRecord) bool) dbgateway.InstanceRecord {
	g.t.Helper()
	deadline := time.Now().Add(within)
	for {
		all, err := g.store.Instances(g.t.Context(), "org_1", g.gw.ID)
		if err != nil {
			g.t.Fatal(err)
		}
		for _, in := range all {
			if !strings.HasPrefix(in.InstanceID, fakeInstancePrefix) && ok(in) {
				return in
			}
		}
		if time.Now().After(deadline) {
			g.t.Fatalf("no report from the gateway matched in %s: %+v", within, all)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
