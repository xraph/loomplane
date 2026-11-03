package node

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"golang.org/x/net/http/httpproxy"

	"github.com/xraph/loomplane/internal/buildinfo"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

const (
	// ExitRevoked is the process exit code of a node that Loomplane revoked:
	// EX_NOPERM. Forge uses 3 for its own internal errors.
	ExitRevoked = 77

	defaultMinDelay    = time.Second
	defaultMaxDelay    = time.Minute
	defaultStableAfter = time.Minute
	defaultHeartbeat   = 15 * time.Second
	defaultDetectEvery = time.Minute
	handshakeTimeout   = 30 * time.Second
	controlWriteWait   = 10 * time.Second
	maxControlLine     = 1 << 20
	servingGrace       = 10 * time.Second

	refusedKeyMessage = "Loomplane refused this node's key; if it was revoked, run loomctl node leave"
)

// errKeyRefused is a 401 on connect. It is logged where it happens.
var errKeyRefused = errors.New("loomplane refused the node's key")

// ErrRevoked is what Run returns once Loomplane no longer trusts this node.
// node.json is gone by then and the key stays.
var ErrRevoked = errors.New("this node was revoked in Loomplane")

// UpgradeError is what Run returns when Loomplane answers 426: this node is
// too old for it, or newer than it. Its text is the server's.
type UpgradeError struct{ Message string }

func (e *UpgradeError) Error() string { return e.Message }

// ExitCode is the exit code for an error from Run: 0 for none, 77 for a
// revoked node and 1 for anything else.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrRevoked):
		return ExitRevoked
	}
	return 1
}

// Runtime is a node's long-lived connection to Loomplane. Build one with
// NewRuntime, call Run, and call Allow from the local control socket.
type Runtime struct {
	// Log receives what happens on the connection. Nil means the registry's
	// logger, then slog.Default.
	Log *slog.Logger

	st   State
	reg  *Registry
	live *Live

	// cfgMu makes "change the config, then save it" one step, so two
	// changes cannot save out of order.
	cfgMu sync.Mutex

	mu  sync.Mutex
	cur *session

	// leaving is set once Leave has asked Loomplane to revoke this node, and
	// cleared by the next connection that Loomplane accepts.
	leaving atomic.Bool
	// revoked is closed when Run decides this node was revoked.
	revoked     chan struct{}
	revokedOnce sync.Once

	// Tunables, set by tests.
	minDelay, maxDelay, stableAfter time.Duration
	heartbeatEvery, detectEvery     time.Duration
	nodeVersion                     string
	narrows                         atomic.Int32 // Narrow messages applied; tests wait on it
	detectBase                      DetectOptions
	detect                          func(context.Context) nodeproto.Inventory
	tls                             *tls.Config
}

// NewRuntime reads node.json and returns a Runtime that serves streams from
// reg. It registers ProbeHandler on reg for the probe endpoint, replacing
// any handler there; the caller registers the other handlers.
func NewRuntime(st State, reg *Registry) (*Runtime, error) {
	// ReadConfig never creates or changes the state dir: only join does that.
	cfg, err := st.ReadConfig()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errors.New("this machine hasn't joined; run loomctl node join first")
		}
		return nil, fmt.Errorf("reading this node's config (run loomctl node join if this machine has not joined): %w", err)
	}
	r := &Runtime{st: st, reg: reg, nodeVersion: buildinfo.Version, revoked: make(chan struct{})}
	r.live = NewLive(r.sanitized(cfg))
	reg.Register(nodeproto.EndpointProbe, ProbeHandler(r.detectOptions))
	return r, nil
}

// sanitized drops endpoints this build cannot allow from the allowlist and
// from PendingAllowed, as a hand-edited node.json or an older build may have
// left them, and pending ones that are no longer in the allowlist.
func (r *Runtime) sanitized(c Config) Config {
	var dropped []string
	keep := func(eps []nodeproto.Endpoint, what string) []nodeproto.Endpoint {
		var out []nodeproto.Endpoint
		for _, e := range eps {
			if slices.Contains(nodeproto.AllowableEndpoints(), e) && !slices.Contains(out, e) {
				out = append(out, e)
			} else {
				dropped = append(dropped, what+":"+sanitize(string(e)))
			}
		}
		return out
	}
	c.Endpoints = nonNil(keep(c.Endpoints, "endpoints"))
	c.PendingAllowed = keep(c.PendingAllowed, "pending_allowed")
	pending := c.PendingAllowed[:0:0]
	for _, e := range c.PendingAllowed {
		if slices.Contains(c.Endpoints, e) {
			pending = append(pending, e)
		}
	}
	c.PendingAllowed = pending
	if len(dropped) > 0 {
		r.log().Warn("loomplane-node: dropped endpoints this node cannot allow from node.json", "dropped", dropped)
	}
	return c
}

// Run keeps the connection to Loomplane until ctx ends (it then returns nil),
// the node is revoked (ErrRevoked, exit code 77) or Loomplane answers 426
// (*UpgradeError). Anything else is retried with backoff.
func Run(ctx context.Context, st State, reg *Registry) error {
	r, err := NewRuntime(st, reg)
	if err != nil {
		return err
	}
	return r.Run(ctx)
}

func (r *Runtime) log() *slog.Logger {
	switch {
	case r.Log != nil:
		return r.Log
	case r.reg != nil && r.reg.Log != nil:
		return r.reg.Log
	}
	return slog.Default()
}

func orDur(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func (r *Runtime) detectOptions() DetectOptions {
	o := r.detectBase
	o.Config = r.live.Load()
	if o.DockerSockets == nil && o.Config.DockerSocket != "" {
		o.DockerSockets = []string{o.Config.DockerSocket}
	}
	return o
}

func (r *Runtime) inventory(ctx context.Context) nodeproto.Inventory {
	if r.detect != nil {
		return r.detect(ctx)
	}
	return Detect(ctx, r.detectOptions())
}

// Run connects, serves and reconnects. See the package func Run.
func (r *Runtime) Run(ctx context.Context) error {
	stop, err := r.serveControl(ctx)
	if err != nil {
		return err
	}
	defer stop()
	minDelay, maxDelay := orDur(r.minDelay, defaultMinDelay), orDur(r.maxDelay, defaultMaxDelay)
	delay := minDelay
	for {
		connected, err := r.once(ctx)
		if ctx.Err() != nil {
			return nil
		}
		var up *UpgradeError
		// A node that asked to leave and is now refused was revoked by that
		// request, even if the Revoked notice was lost with the connection.
		revoked := errors.Is(err, ErrRevoked) || (r.leaving.Load() && errors.Is(err, errKeyRefused))
		switch {
		case revoked:
			r.revokedOnce.Do(func() { close(r.revoked) })
			if derr := r.st.DeleteConfig(); derr != nil {
				r.log().Error("loomplane-node: removing node.json after a revoke", "error", derr)
			}
			return ErrRevoked
		case errors.As(err, &up):
			return up
		}
		if connected >= orDur(r.stableAfter, defaultStableAfter) {
			delay = minDelay
		}
		wait := jitter(delay)
		if err != nil && !errors.Is(err, errKeyRefused) { // that one logged itself
			r.log().Warn("loomplane-node: the connection to Loomplane ended", "error", err, "retry_in", wait.Round(time.Millisecond))
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		delay = min(delay*2, maxDelay)
	}
}

// jitter spreads d by plus or minus 20%, so a fleet that lost its control
// plane together does not come back together.
func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64())) //nolint:gosec // jitter needs no secrecy
}

// proxy picks the proxy for a request from the environment (HTTPS_PROXY,
// HTTP_PROXY and NO_PROXY), read each time so a change is seen.
func (r *Runtime) proxy(req *http.Request) (*url.URL, error) {
	return httpproxy.FromEnvironment().ProxyFunc()(req.URL)
}

// httpClient is for the challenge request. It honours the proxy environment.
func (r *Runtime) httpClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		Proxy: r.proxy, TLSClientConfig: r.tls, TLSHandshakeTimeout: 15 * time.Second, DisableKeepAlives: true,
	}}
}

// challenge asks Loomplane for something to sign.
func (r *Runtime) challenge(ctx context.Context, base, nodeID string) (nodeproto.Challenge, error) {
	var ch nodeproto.Challenge
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/node/v1/challenge?node="+url.QueryEscape(nodeID), http.NoBody)
	if err != nil {
		return ch, err
	}
	res, err := r.httpClient().Do(req)
	if err != nil {
		return ch, err
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return ch, err
	}
	if res.StatusCode == http.StatusUpgradeRequired {
		return ch, &UpgradeError{Message: serverMessage(body)}
	}
	if res.StatusCode != http.StatusOK {
		return ch, fmt.Errorf("asking Loomplane for a challenge: %s: %s", res.Status, serverMessage(body))
	}
	if err = json.Unmarshal(body, &ch); err != nil {
		return ch, fmt.Errorf("reading Loomplane's challenge: %w", err)
	}
	if ch.Nonce == "" {
		return ch, errors.New("Loomplane sent a challenge with no nonce") //nolint:staticcheck // names the product
	}
	return ch, nil
}

func wsURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/node/v1/connect"
	return u.String(), nil
}

// once makes one connection and serves it until it ends. It returns how long
// the connection stood and why it ended.
func (r *Runtime) once(ctx context.Context) (time.Duration, error) {
	cfg := r.live.Load()
	base, err := nodeproto.NormalizeAudience(cfg.URL)
	if err != nil {
		return 0, fmt.Errorf("node.json has no usable url: %w", err)
	}
	priv, err := r.st.Identity()
	if err != nil {
		return 0, err
	}
	ch, err := r.challenge(ctx, base, cfg.NodeID)
	if err != nil {
		return 0, err
	}
	// Sign the audience this node was told to trust, never the one a
	// server names, so a challenge cannot be steered at another instance.
	assertion, err := nodeproto.Sign(priv, nodeproto.Challenge{NodeID: cfg.NodeID, Audience: base, Nonce: ch.Nonce, IssuedAt: ch.IssuedAt})
	if err != nil {
		return 0, err
	}
	authz, err := nodeproto.EncodeAuthorization(assertion)
	if err != nil {
		return 0, err
	}
	target, err := wsURL(base)
	if err != nil {
		return 0, err
	}
	d := &websocket.Dialer{Proxy: r.proxy, TLSClientConfig: r.tls, HandshakeTimeout: handshakeTimeout}
	conn, resp, err := d.DialContext(ctx, target, http.Header{"Authorization": {authz}})
	if err != nil {
		return 0, r.dialError(err, resp)
	}
	start := time.Now()
	ws := newWSConn(conn)
	mux, err := yamux.Server(ws, yamuxConfig())
	if err != nil {
		_ = conn.Close()
		return 0, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = mux.Close() })
	defer stop()

	s := &session{r: r, mux: mux, done: make(chan struct{})}
	defer close(s.done)
	err = s.run(ctx)
	cancel()
	_ = mux.Close()
	s.wg.Wait()
	s.waitServing()
	r.detach(s)
	if ws.code() == nodeproto.CloseUnauthorized {
		err = ErrRevoked
	}
	return time.Since(start), err
}

func (r *Runtime) dialError(err error, resp *http.Response) error {
	if resp == nil {
		return err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10)) //nolint:errcheck // a short read just shortens the message
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		r.log().Warn(refusedKeyMessage)
		return errKeyRefused
	case http.StatusUpgradeRequired:
		return &UpgradeError{Message: serverMessage(body)}
	}
	return fmt.Errorf("connecting to Loomplane: %s: %s", resp.Status, serverMessage(body))
}

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = defaultHeartbeat
	cfg.ConnectionWriteTimeout = controlWriteWait
	cfg.LogOutput = io.Discard
	return cfg
}

// serverMessage reads the human part of an error body: {"error": "..."} or
// the text itself, capped.
func serverMessage(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		msg = e.Error
	}
	if len(msg) > 300 {
		msg = strings.ToValidUTF8(msg[:300], "")
	}
	return sanitize(msg)
}

// session is one live connection: a yamux server whose stream 0 is control.
type session struct {
	r       *Runtime
	mux     *yamux.Session
	ctrl    *yamux.Stream
	wmu     sync.Mutex
	wg      sync.WaitGroup // the session's own helpers
	serving sync.WaitGroup // handlers serving streams

	done    chan struct{} // closed once the session is over
	lastInv []byte        // the inventory last sent, JSON
	invMu   sync.Mutex
}

// send writes one control message. A failed write ends the session.
// waitServing waits for the stream handlers, whose streams and context are
// closed by now, so that none still runs when Run deletes node.json or
// returns. A handler that ignores both is abandoned after servingGrace.
func (s *session) waitServing() {
	done := make(chan struct{})
	go func() { s.serving.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(servingGrace):
		s.r.log().Warn("loomplane-node: a stream handler outlived its connection")
	}
}

func (s *session) send(msg any) error {
	if s == nil {
		return nil
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if err := s.ctrl.SetWriteDeadline(time.Now().Add(controlWriteWait)); err != nil {
		return err
	}
	if err := nodeproto.EncodeControl(s.ctrl, msg); err != nil {
		_ = s.mux.Close()
		return err
	}
	return s.ctrl.SetWriteDeadline(time.Time{})
}

// run does the handshake, starts the helpers and reads control messages
// until the session ends.
func (s *session) run(ctx context.Context) error {
	r := s.r
	timeout := time.AfterFunc(handshakeTimeout, func() { _ = s.mux.Close() })
	defer timeout.Stop()
	ctrl, err := s.mux.AcceptStream()
	if err != nil {
		return fmt.Errorf("waiting for the control stream: %w", err)
	}
	s.ctrl = ctrl
	sc := bufio.NewScanner(ctrl)
	sc.Buffer(make([]byte, 0, 64<<10), maxControlLine)
	if !sc.Scan() {
		return fmt.Errorf("waiting for Loomplane's hello: %w", firstErr(sc.Err(), io.EOF))
	}
	first, err := nodeproto.DecodeControl(sc.Bytes())
	if err != nil {
		return fmt.Errorf("reading Loomplane's hello: %w", err)
	}
	hello, ok := first.(nodeproto.Hello)
	if !ok {
		return fmt.Errorf("Loomplane started with %T, not a hello", first) //nolint:staticcheck // names the product
	}
	timeout.Stop()

	// A failed write must not hide a Revoked the control plane sent just
	// before it hung up, so keep reading and report the write error after.
	greetErr := s.greet(ctx, hello)
	if greetErr == nil {
		r.log().Info("loomplane-node: connected to Loomplane", "epoch", hello.Epoch)
		s.wg.Add(3)
		go func() { defer s.wg.Done(); s.beat(ctx) }()
		go func() { defer s.wg.Done(); s.watchInventory(ctx) }()
		go func() { defer s.wg.Done(); s.accept(ctx) }()
	}
	for sc.Scan() {
		msg, derr := nodeproto.DecodeControl(sc.Bytes())
		if derr != nil {
			r.log().Debug("loomplane-node: skipping a control line that does not parse", "error", derr)
			continue
		}
		if herr := r.handle(s, msg); herr != nil {
			return herr
		}
	}
	return firstErr(greetErr, firstErr(sc.Err(), io.EOF))
}

// greet answers the hub's hello and reports what this node is.
func (s *session) greet(ctx context.Context, hello nodeproto.Hello) error {
	r := s.r
	if err := s.send(nodeproto.Hello{Version: nodeproto.Version, NodeVersion: r.nodeVersion, Epoch: hello.Epoch}); err != nil {
		return err
	}
	// Attach first, so an Allow from here on is sent live, and then read
	// what is pending: nothing falls between the two. What the owner allowed
	// goes before the first inventory, which the hub answers with a Narrow.
	r.leaving.Store(false)
	r.attach(s)
	if pending := r.live.Load().PendingAllowed; len(pending) > 0 {
		if err := s.send(nodeproto.Allowed{Endpoints: pending}); err != nil {
			return err
		}
	}
	if err := s.report(ctx); err != nil {
		return err
	}
	return s.send(heartbeat())
}

func firstErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

func heartbeat() nodeproto.Heartbeat { return nodeproto.Heartbeat{At: time.Now().UTC()} }

func (s *session) beat(ctx context.Context) {
	t := time.NewTicker(orDur(s.r.heartbeatEvery, defaultHeartbeat))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.send(heartbeat()) != nil {
				return
			}
		}
	}
}

// report sends the inventory when it differs from the last one sent.
func (s *session) report(ctx context.Context) error {
	inv := s.r.inventory(ctx)
	b, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	s.invMu.Lock()
	same := bytes.Equal(b, s.lastInv)
	if !same {
		s.lastInv = b
	}
	s.invMu.Unlock()
	if same {
		return nil
	}
	return s.send(nodeproto.InventoryReport{Inventory: inv})
}

func (s *session) watchInventory(ctx context.Context) {
	t := time.NewTicker(orDur(s.r.detectEvery, defaultDetectEvery))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.report(ctx) != nil {
				return
			}
		}
	}
}

// accept hands every stream after control to the registry.
func (s *session) accept(ctx context.Context) {
	for {
		st, err := s.mux.AcceptStream()
		if err != nil {
			return
		}
		s.serving.Add(1)
		go func() {
			defer s.serving.Done()
			s.r.reg.Serve(ctx, s.r.live, st)
		}()
	}
}

// handle applies one control message. It returns ErrRevoked for Revoked.
func (r *Runtime) handle(s *session, msg any) error {
	switch m := msg.(type) {
	case nodeproto.Narrow:
		cfg, err := r.apply(func(c *Config) {
			c.Acknowledge(m.Ack)
			c.Narrow(m.Endpoints)
		})
		r.narrows.Add(1)
		if err != nil {
			// The change holds in memory and goes unacknowledged; the
			// Narrow the hub sends on the next connect repeats it.
			r.log().Error("loomplane-node: saving a narrowed allowlist", "error", err)
			return nil
		}
		return s.send(nodeproto.NarrowApplied{Endpoints: nonNil(cfg.Endpoints)})
	case nodeproto.SetIngress:
		switch m.Mode {
		case nodeproto.IngressNone, nodeproto.IngressDirect, nodeproto.IngressRelay:
		default:
			r.log().Warn("loomplane-node: ignoring an unknown ingress mode", "mode", sanitize(m.Mode))
			return nil
		}
		cfg, err := r.apply(func(c *Config) { c.IngressMode = m.Mode })
		if err != nil {
			r.log().Error("loomplane-node: saving the ingress mode", "error", err)
			return nil
		}
		return s.send(nodeproto.IngressApplied{Mode: cfg.IngressMode})
	case nodeproto.Revoked:
		return ErrRevoked
	case nodeproto.Unknown:
		r.log().Debug("loomplane-node: ignoring a message from a newer Loomplane", "type", sanitize(m.Type))
	}
	return nil
}

func nonNil(e []nodeproto.Endpoint) []nodeproto.Endpoint {
	if e == nil {
		return []nodeproto.Endpoint{}
	}
	return e
}

// apply changes the live config and saves what that gave. Both happen under
// one lock, so changes save in the order they were made.
func (r *Runtime) apply(f func(*Config)) (Config, error) {
	r.cfgMu.Lock()
	defer r.cfgMu.Unlock()
	cfg := r.live.Update(f)
	return cfg, r.st.SaveConfig(cfg)
}

// Allow widens what this machine serves, on its owner's say-so. It adds the
// endpoints to the live config and to PendingAllowed, saves node.json, and
// tells Loomplane with an Allowed message when connected. Loomplane answers
// with a Narrow that includes them, which ends the pending state. Until then
// no Narrow can remove them, and every connect sends them again, so an allow
// made while offline, or before a restart, still arrives. Only endpoints an
// owner can allow are accepted: not control, public or unknown ones. A failed
// save changes nothing.
func (r *Runtime) Allow(eps []nodeproto.Endpoint) error {
	add, err := AllowableOnly(eps)
	if err != nil {
		return err
	}
	if len(add) == 0 {
		return nil
	}
	r.cfgMu.Lock()
	prev := r.live.Load()
	cfg := r.live.Update(func(c *Config) { c.Widen(add) })
	if err := r.st.SaveConfig(cfg); err != nil {
		r.live.Update(func(c *Config) { c.Endpoints, c.PendingAllowed = prev.Endpoints, prev.PendingAllowed })
		r.cfgMu.Unlock()
		return err
	}
	r.cfgMu.Unlock()

	r.mu.Lock()
	s := r.cur
	r.mu.Unlock()
	if s != nil {
		// A failed send ends the session, and the next connect sends it again.
		_ = s.send(nodeproto.Allowed{Endpoints: add}) //nolint:errcheck // see above
	}
	return nil
}

// attach makes s the live session.
func (r *Runtime) attach(s *session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cur = s
}

func (r *Runtime) detach(s *session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == s {
		r.cur = nil
	}
}
