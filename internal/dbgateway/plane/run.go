package plane

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// lastGoodName is the sealed copy of the last snapshot the gateway
	// applied, in the state directory.
	lastGoodName = "last-good"

	defaultStateDir = "/tmp/dbgw"
	defaultPort     = 8080

	// reportEvery is how often an instance reports to the control plane. The
	// control plane counts an instance as live for 30s after its last report.
	reportEvery = 10 * time.Second
	// reportTimeout bounds one report.
	reportTimeout = 10 * time.Second
	// minBackoff and maxBackoff bound the wait after the control plane
	// fails or sends a snapshot the gateway refuses.
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
	// pollGap is the least time between two polls that found nothing new, so
	// a control plane that answers at once can't be hit in a tight loop.
	pollGap = time.Second

	// readHeaderTimeout bounds how long a client has to send request headers.
	readHeaderTimeout = 10 * time.Second
	// shutdownDrainLimit bounds Router.Drain on shutdown, and
	// shutdownHTTPLimit the HTTP server's own shutdown after it.
	shutdownDrainLimit = 30 * time.Second
	shutdownHTTPLimit  = 10 * time.Second
	// finalReportLimit bounds the report sent as the gateway starts to drain.
	finalReportLimit = 5 * time.Second
)

// Config is everything one gateway instance needs to run.
type Config struct {
	GatewayID  string
	ControlURL string
	StateDir   string
	Suffix     Suffix
	Port       int
	// SigningKey signs requests to the control plane.
	SigningKey ed25519.PrivateKey
	// SealingKey opens the credentials in snapshots and the last-good file.
	SealingKey *ecdh.PrivateKey
	// TrustKeys are the control-plane keys whose snapshots the gateway accepts.
	TrustKeys []TrustKey
}

var (
	gatewayIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	suffixPattern    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
)

// ConfigFromEnv reads the gateway's configuration through getenv:
//
//	LOOMPLANE_DBGW_ID           the gateway's ID, as the control plane knows it
//	LOOMPLANE_DBGW_CONTROL_URL  the control plane's origin, https (http only for localhost)
//	LOOMPLANE_DBGW_SIGNING_KEY  standard base64 of the 32-byte Ed25519 seed
//	LOOMPLANE_DBGW_SEALING_KEY  standard base64 of the 32-byte X25519 private key
//	LOOMPLANE_DBGW_TRUST_KEYS   JSON array of {"public_key", "not_before", "not_after"}
//	LOOMPLANE_DBGW_SUFFIX       the zone logical hosts live in; lowercased, dots trimmed
//	LOOMPLANE_DBGW_STATE_DIR    where the last-good snapshot lives (default /tmp/dbgw)
//	PORT                        the listening port (default 8080)
//
// Errors name the variable and never repeat a key.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	var cfg Config

	cfg.GatewayID = get("LOOMPLANE_DBGW_ID")
	if !gatewayIDPattern.MatchString(cfg.GatewayID) {
		return Config{}, errors.New("LOOMPLANE_DBGW_ID: required, letters, digits, '.', '_' and '-', at most 128 characters")
	}

	control, err := parseControlURL(get("LOOMPLANE_DBGW_CONTROL_URL"))
	if err != nil {
		return Config{}, fmt.Errorf("LOOMPLANE_DBGW_CONTROL_URL: %w", err)
	}
	cfg.ControlURL = control

	seed, err := base64.StdEncoding.DecodeString(get("LOOMPLANE_DBGW_SIGNING_KEY"))
	if err != nil || len(seed) != ed25519.SeedSize {
		return Config{}, errors.New("LOOMPLANE_DBGW_SIGNING_KEY: required, standard base64 of a 32-byte Ed25519 seed")
	}
	cfg.SigningKey = ed25519.NewKeyFromSeed(seed)

	raw, err := base64.StdEncoding.DecodeString(get("LOOMPLANE_DBGW_SEALING_KEY"))
	if err == nil {
		cfg.SealingKey, err = ecdh.X25519().NewPrivateKey(raw)
	}
	if err != nil {
		return Config{}, errors.New("LOOMPLANE_DBGW_SEALING_KEY: required, standard base64 of a 32-byte X25519 private key")
	}

	if err := json.Unmarshal([]byte(get("LOOMPLANE_DBGW_TRUST_KEYS")), &cfg.TrustKeys); err != nil {
		return Config{}, errors.New("LOOMPLANE_DBGW_TRUST_KEYS: required, a JSON array of {public_key, not_before, not_after}")
	}
	if len(cfg.TrustKeys) == 0 {
		return Config{}, errors.New("LOOMPLANE_DBGW_TRUST_KEYS: needs at least one key")
	}
	for _, k := range cfg.TrustKeys {
		if len(k.PublicKey) != ed25519.PublicKeySize {
			return Config{}, errors.New("LOOMPLANE_DBGW_TRUST_KEYS: every public_key is a 32-byte Ed25519 key")
		}
	}

	suffix := strings.Trim(strings.ToLower(get("LOOMPLANE_DBGW_SUFFIX")), ".")
	if !suffixPattern.MatchString(suffix) {
		return Config{}, errors.New("LOOMPLANE_DBGW_SUFFIX: required, a DNS zone such as us-east-1.db.example.com")
	}
	cfg.Suffix = Suffix(suffix)

	cfg.StateDir = get("LOOMPLANE_DBGW_STATE_DIR")
	if cfg.StateDir == "" {
		cfg.StateDir = defaultStateDir
	}

	cfg.Port = defaultPort
	if p := get("PORT"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return Config{}, errors.New("PORT: a port number from 1 to 65535")
		}
		cfg.Port = n
	}
	return cfg, nil
}

// parseControlURL returns the control plane's origin without a trailing
// slash. The gateway sends signed requests there, so it must be https; plain
// http is for a control plane on the same machine.
func parseControlURL(s string) (string, error) {
	if s == "" {
		return "", errors.New("required")
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", errors.New("not a URL with a host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("must not carry credentials, a query or a fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
		default:
			return "", errors.New("must be https unless the host is localhost")
		}
	default:
		return "", errors.New("must be an https URL")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

// validate checks a Config that did not come from ConfigFromEnv.
func (c Config) validate() error {
	switch {
	case !gatewayIDPattern.MatchString(c.GatewayID):
		return errors.New("run: bad gateway ID")
	case len(c.SigningKey) != ed25519.PrivateKeySize:
		return errors.New("run: bad signing key")
	case c.SealingKey == nil || c.SealingKey.Curve() != ecdh.X25519():
		return errors.New("run: bad sealing key")
	case len(c.TrustKeys) == 0:
		return errors.New("run: no trust keys")
	case c.Suffix == "":
		return errors.New("run: no suffix")
	case c.StateDir == "":
		return errors.New("run: no state directory")
	case c.Port < 0 || c.Port > 65535:
		return errors.New("run: bad port")
	}
	// The same rules as ConfigFromEnv, so a Config built in code can't send
	// signed requests over plain http to another host.
	if _, err := parseControlURL(c.ControlURL); err != nil {
		return fmt.Errorf("run: control URL: %w", err)
	}
	for _, k := range c.TrustKeys {
		if len(k.PublicKey) != ed25519.PublicKeySize {
			return errors.New("run: every trust key is a 32-byte Ed25519 key")
		}
	}
	return nil
}

// runOptions are the parts of Run that tests change: where it listens, the
// store it fills, and its timings.
type runOptions struct {
	listener   net.Listener
	store      *Store
	routerHook func(*Router)

	minBackoff, maxBackoff, reportEvery, pollGap time.Duration
}

func (o runOptions) withDefaults() runOptions {
	if o.store == nil {
		o.store = &Store{}
	}
	if o.minBackoff == 0 {
		o.minBackoff = minBackoff
	}
	if o.maxBackoff == 0 {
		o.maxBackoff = maxBackoff
	}
	if o.reportEvery == 0 {
		o.reportEvery = reportEvery
	}
	if o.pollGap == 0 {
		o.pollGap = pollGap
	}
	return o
}

// Run serves the gateway until ctx ends. It opens the last-good snapshot if
// there is a trusted one, serves /v2 (WebSocket), /sql (HTTP) and /healthz,
// follows the control plane's snapshots, and reports to it every 10s. When
// ctx ends it tells the control plane it is draining, drains the router,
// closes the server and returns nil. It returns an error at once if the
// configuration is unusable or the port can't be opened.
func Run(ctx context.Context, cfg Config) error {
	return run(ctx, cfg, runOptions{})
}

// gateway is one running instance.
type gateway struct {
	cfg    Config
	opts   runOptions
	id     string // this instance, random per process
	store  *Store
	router *Router
	client *ControlClient
	path   string // the last-good file
	ready  atomic.Bool
}

func run(ctx context.Context, cfg Config, opts runOptions) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	cfg.ControlURL, _ = parseControlURL(cfg.ControlURL) //nolint:errcheck // validated above
	opts = opts.withDefaults()
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return fmt.Errorf("run: state directory: %w", err)
	}
	removeStaleTemps(cfg.StateDir)

	ln := opts.listener
	if ln == nil {
		var err error
		var lc net.ListenConfig
		if ln, err = lc.Listen(ctx, "tcp", ":"+strconv.Itoa(cfg.Port)); err != nil {
			return fmt.Errorf("run: listen: %w", err)
		}
	}

	var idBytes [8]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		_ = ln.Close()
		return fmt.Errorf("run: instance ID: %w", err)
	}
	g := &gateway{
		cfg:    cfg,
		opts:   opts,
		id:     hex.EncodeToString(idBytes[:]),
		store:  opts.store,
		client: NewControlClient(cfg.ControlURL, cfg.GatewayID, cfg.SigningKey),
		path:   filepath.Join(cfg.StateDir, lastGoodName),
	}
	g.router = NewRouter(g.store, cfg.SealingKey, cfg.GatewayID, PgDialer{}, g.client, cfg.Suffix)
	if opts.routerHook != nil {
		opts.routerHook(g.router)
	}
	g.loadLastGood()

	// Sessions end when the router has drained, not when ctx ends, so a
	// transaction in flight at SIGTERM gets to finish.
	runCtx, endSessions := context.WithCancel(context.WithoutCancel(ctx))
	defer endSessions()
	mux := http.NewServeMux()
	mux.Handle("/v2", WebSocketHandlerContext(runCtx, g.router))
	mux.Handle("/sql", HTTPQueryHandler(g.router))
	mux.HandleFunc("/healthz", g.healthz)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: readHeaderTimeout}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	slog.Info("database gateway listening", "addr", ln.Addr().String(), "gateway", cfg.GatewayID)

	loopCtx, stopLoops := context.WithCancel(ctx)
	var loops sync.WaitGroup
	loops.Add(2)
	go func() { defer loops.Done(); g.pollLoop(loopCtx) }()
	go func() { defer loops.Done(); g.reportLoop(loopCtx) }()

	var failure error
	select {
	case <-ctx.Done():
	case err := <-serveErr:
		failure = err
	}
	stopLoops()
	loops.Wait()

	slog.Info("database gateway draining")
	final, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalReportLimit)
	g.report(final, true)
	cancel()
	drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownDrainLimit)
	g.router.Drain(drain)
	cancel()
	endSessions()
	down, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownHTTPLimit)
	defer cancel()
	if err := srv.Shutdown(down); err != nil {
		_ = srv.Close()
	}
	if failure != nil && !errors.Is(failure, http.ErrServerClosed) {
		return fmt.Errorf("run: serve: %w", failure)
	}
	return nil
}

// removeStaleTemps deletes the temporary files a SaveLastGood that was cut
// short left behind. They hold sealed snapshots, so they are not kept.
func removeStaleTemps(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), lastGoodName+".tmp-") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// loadLastGood serves the snapshot saved by an earlier run, so the gateway
// can answer while the control plane is away. It is verified like any other.
func (g *gateway) loadLastGood() {
	env, err := LoadLastGood(g.path, g.cfg.SealingKey)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return
	case err != nil:
		slog.Warn("database gateway ignored an unreadable last-good snapshot")
		return
	}
	s, err := VerifySnapshot(env, g.cfg.TrustKeys, g.cfg.GatewayID, time.Now())
	if err != nil {
		slog.Warn("database gateway ignored an untrusted last-good snapshot")
		return
	}
	if err := g.router.Apply(s); err != nil {
		slog.Warn("database gateway could not apply the last-good snapshot")
		return
	}
	g.ready.Store(true)
	slog.Info("database gateway applied the last-good snapshot", "version", s.Version)
}

// apply verifies env, hands the snapshot to the router, which swaps the
// store, and then saves env as the new last-good. A failure to save is
// logged and does not stop the gateway serving what it applied.
func (g *gateway) apply(env SignedSnapshot) error {
	s, err := VerifySnapshot(env, g.cfg.TrustKeys, g.cfg.GatewayID, time.Now())
	if err != nil {
		return err
	}
	if err := g.router.Apply(s); err != nil {
		return err
	}
	g.ready.Store(true)
	slog.Info("database gateway applied a snapshot", "version", s.Version)
	if err := SaveLastGood(g.path, g.cfg.SealingKey, env); err != nil {
		slog.Warn("database gateway could not save the last-good snapshot", "err", err)
	}
	return nil
}

// pollLoop follows the control plane's snapshots until ctx ends. After a
// failure, or a snapshot it refuses, it waits 1s, doubling to 30s.
func (g *gateway) pollLoop(ctx context.Context) {
	backoff := g.opts.minBackoff
	for ctx.Err() == nil {
		var after uint64
		if cur := g.store.Current(); cur != nil {
			after = cur.Version
		}
		started := time.Now()
		snap, err := g.client.Poll(ctx, after)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("database gateway could not poll the control plane", "err", err)
		} else if snap != nil {
			if err = g.apply(*snap); err != nil {
				slog.Warn("database gateway refused a snapshot", "err", err)
			}
		}
		switch {
		case err != nil:
			sleep(ctx, backoff)
			backoff = min(backoff*2, g.opts.maxBackoff)
		case snap == nil:
			backoff = g.opts.minBackoff
			sleep(ctx, g.opts.pollGap-time.Since(started))
		default:
			backoff = g.opts.minBackoff
		}
	}
}

// reportLoop reports now and then every report interval until ctx ends.
func (g *gateway) reportLoop(ctx context.Context) {
	t := time.NewTicker(g.opts.reportEvery)
	defer t.Stop()
	for {
		g.report(ctx, false)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// report sends one Report and sizes the pools from the answer.
func (g *gateway) report(ctx context.Context, draining bool) {
	ctx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()
	var version uint64
	if cur := g.store.Current(); cur != nil {
		version = cur.Version
	}
	resp, err := g.client.Report(ctx, Report{
		InstanceID:      g.id,
		SnapshotVersion: version,
		Draining:        draining || g.router.Draining(),
		Routes:          g.router.Stats(),
	})
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("database gateway could not report to the control plane", "err", err)
		}
		return
	}
	// This instance is one of the live ones, so a count below 1 is wrong.
	if resp.LiveInstances >= 1 {
		g.router.SetInstances(resp.LiveInstances)
	}
}

// healthz is 200 once a snapshot has been applied, and 503 before that and
// while the gateway drains, so a load balancer sends traffic only to an
// instance that can route it.
func (g *gateway) healthz(w http.ResponseWriter, _ *http.Request) {
	switch {
	case g.router.Draining():
		http.Error(w, "draining", http.StatusServiceUnavailable)
	case !g.ready.Load():
		http.Error(w, "no snapshot yet", http.StatusServiceUnavailable)
	default:
		_, _ = w.Write([]byte("ok\n")) //nolint:errcheck // a client that hung up has nobody to tell
	}
}

// sleep waits for d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
