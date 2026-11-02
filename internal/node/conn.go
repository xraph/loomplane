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
	// ExitRevoked is the process exit code of a node that Loomplane revoked.
	ExitRevoked = 3

	defaultMinDelay    = time.Second
	defaultMaxDelay    = time.Minute
	defaultStableAfter = time.Minute
	defaultHeartbeat   = 15 * time.Second
	defaultDetectEvery = time.Minute
	handshakeTimeout   = 30 * time.Second
	controlWriteWait   = 10 * time.Second
	maxControlLine     = 1 << 20

	refusedKeyMessage = "Loomplane refused this node's key; if it was revoked, run loomctl node leave"
)

// ErrRevoked is what Run returns once Loomplane no longer trusts this node.
// node.json is gone by then and the key stays.
var ErrRevoked = errors.New("this node was revoked in Loomplane")

// UpgradeError is what Run returns when Loomplane answers 426: this node is
// too old for it, or newer than it. Its text is the server's.
type UpgradeError struct{ Message string }

func (e *UpgradeError) Error() string { return e.Message }

// ExitCode is the exit code for an error from Run: 0 for none, 3 for a
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

	mu      sync.Mutex
	cur     *session
	pending []nodeproto.Endpoint // allowed while no session was up

	// Tunables, set by tests.
	minDelay, maxDelay, stableAfter time.Duration
	heartbeatEvery, detectEvery     time.Duration
	nodeVersion                     string
	narrows                         atomic.Int32 // Narrow messages applied, for tests
	detectBase                      DetectOptions
	detect                          func(context.Context) nodeproto.Inventory
	tls                             *tls.Config
}

// NewRuntime loads node.json and returns a Runtime that serves streams from
// reg. It registers ProbeHandler on reg for the probe endpoint, replacing
// any handler there; the caller registers the other handlers.
func NewRuntime(st State, reg *Registry) (*Runtime, error) {
	cfg, err := st.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("reading this node's config (has it joined? run loomctl node join): %w", err)
	}
	r := &Runtime{st: st, reg: reg, live: NewLive(cfg), nodeVersion: buildinfo.Version}
	reg.Register(nodeproto.EndpointProbe, ProbeHandler(r.detectOptions))
	return r, nil
}

// Run keeps the connection to Loomplane until ctx ends (it then returns nil),
// the node is revoked (ErrRevoked, exit code 3) or Loomplane answers 426
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
	minDelay, maxDelay := orDur(r.minDelay, defaultMinDelay), orDur(r.maxDelay, defaultMaxDelay)
	delay := minDelay
	for {
		connected, err := r.once(ctx)
		if ctx.Err() != nil {
			return nil
		}
		var up *UpgradeError
		switch {
		case errors.Is(err, ErrRevoked):
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
		if err != nil {
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

// httpClient is for the challenge request. It honours the proxy environment.
func (r *Runtime) proxy(req *http.Request) (*url.URL, error) {
	return httpproxy.FromEnvironment().ProxyFunc()(req.URL)
}

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

	s := &session{r: r, mux: mux}
	err = s.run(ctx)
	cancel()
	_ = mux.Close()
	s.wg.Wait()
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
		return errors.New("Loomplane answered 401") //nolint:staticcheck // names the product
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
	r    *Runtime
	mux  *yamux.Session
	ctrl *yamux.Stream
	wmu  sync.Mutex
	wg   sync.WaitGroup

	lastInv []byte // the inventory last sent, JSON
	invMu   sync.Mutex
}

// send writes one control message. A failed write ends the session.
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
	// What the owner allowed while we were away goes before the first
	// inventory: the hub narrows to what it stored right after that.
	if pending := r.attach(s); len(pending) > 0 {
		if err := s.send(nodeproto.Allowed{Endpoints: pending}); err != nil {
			r.queue(pending)
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
		go s.r.reg.Serve(ctx, s.r.live, st)
	}
}

// handle applies one control message. It returns ErrRevoked for Revoked.
func (r *Runtime) handle(s *session, msg any) error {
	switch m := msg.(type) {
	case nodeproto.Narrow:
		cfg, err := r.apply(func(c *Config) { c.Narrow(m.Endpoints) })
		if err != nil {
			// The narrowing holds in memory, and the next connect repeats it.
			r.log().Error("loomplane-node: saving a narrowed allowlist", "error", err)
		}
		r.narrows.Add(1)
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

// Allow widens what this machine serves, on its owner's say-so. It updates
// the live config, saves node.json and tells Loomplane with an Allowed
// message, now when connected and on the next connect otherwise. Only
// endpoints an owner can allow are accepted: not control, public or unknown
// ones. A failed save changes nothing.
func (r *Runtime) Allow(eps []nodeproto.Endpoint) error {
	var add []nodeproto.Endpoint
	for _, e := range eps {
		if !slices.Contains(nodeproto.AllowableEndpoints(), e) {
			return fmt.Errorf("%q cannot be allowed here; choose from %v", e, nodeproto.AllowableEndpoints())
		}
		if !slices.Contains(add, e) {
			add = append(add, e)
		}
	}
	if len(add) == 0 {
		return nil
	}
	r.cfgMu.Lock()
	prev := r.live.Load().Endpoints
	cfg := r.live.Update(func(c *Config) {
		for _, e := range add {
			if !slices.Contains(c.Endpoints, e) {
				c.Endpoints = append(c.Endpoints, e)
			}
		}
	})
	if err := r.st.SaveConfig(cfg); err != nil {
		r.live.Update(func(c *Config) { c.Endpoints = prev })
		r.cfgMu.Unlock()
		return err
	}
	r.cfgMu.Unlock()

	r.mu.Lock()
	s := r.cur
	r.mu.Unlock()
	if s != nil && s.send(nodeproto.Allowed{Endpoints: add}) == nil {
		return nil
	}
	r.queue(add)
	return nil
}

func (r *Runtime) queue(eps []nodeproto.Endpoint) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range eps {
		if !slices.Contains(r.pending, e) {
			r.pending = append(r.pending, e)
		}
	}
}

// attach makes s the live session and returns what was allowed meanwhile.
func (r *Runtime) attach(s *session) []nodeproto.Endpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cur = s
	p := r.pending
	r.pending = nil
	return p
}

func (r *Runtime) detach(s *session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == s {
		r.cur = nil
	}
}
