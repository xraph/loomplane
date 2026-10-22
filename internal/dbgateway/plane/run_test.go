package plane

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// goodEnv is a complete, valid environment.
func goodEnv(t *testing.T) map[string]string {
	t.Helper()
	trust, _ := newCurrentTrustKey(t)
	keys, err := json.Marshal([]TrustKey{trust})
	if err != nil {
		t.Fatal(err)
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"LOOMPLANE_DBGW_ID":          "gw-1",
		"LOOMPLANE_DBGW_CONTROL_URL": "https://loomplane.example.com",
		"LOOMPLANE_DBGW_SIGNING_KEY": b64(seed),
		"LOOMPLANE_DBGW_SEALING_KEY": b64(newSealKey(t).Bytes()),
		"LOOMPLANE_DBGW_TRUST_KEYS":  string(keys),
		"LOOMPLANE_DBGW_SUFFIX":      "us-east-1.db.example.com",
	}
}

func getenv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestConfigFromEnv(t *testing.T) {
	env := goodEnv(t)
	cfg, err := ConfigFromEnv(getenv(env))
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.GatewayID != "gw-1" || cfg.ControlURL != "https://loomplane.example.com" ||
		cfg.Suffix != "us-east-1.db.example.com" || cfg.StateDir != "/tmp/dbgw" || cfg.Port != 8080 {
		t.Fatalf("config = %+v", cfg)
	}
	seed, _ := base64.StdEncoding.DecodeString(env["LOOMPLANE_DBGW_SIGNING_KEY"])
	if !cfg.SigningKey.Equal(ed25519.NewKeyFromSeed(seed)) {
		t.Error("SigningKey is not the key for the seed")
	}
	raw, _ := base64.StdEncoding.DecodeString(env["LOOMPLANE_DBGW_SEALING_KEY"])
	if !reflect.DeepEqual(cfg.SealingKey.Bytes(), raw) {
		t.Error("SealingKey is not the given X25519 key")
	}
	var want []TrustKey
	if err = json.Unmarshal([]byte(env["LOOMPLANE_DBGW_TRUST_KEYS"]), &want); err != nil {
		t.Fatal(err)
	}
	if len(cfg.TrustKeys) != 1 || cfg.TrustKeys[0].ID() != want[0].ID() || !cfg.TrustKeys[0].NotAfter.Equal(want[0].NotAfter) {
		t.Errorf("TrustKeys = %+v", cfg.TrustKeys)
	}

	env["LOOMPLANE_DBGW_STATE_DIR"] = "/var/lib/dbgw"
	env["PORT"] = "9000"
	cfg, err = ConfigFromEnv(getenv(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StateDir != "/var/lib/dbgw" || cfg.Port != 9000 {
		t.Errorf("StateDir, Port = %q, %d", cfg.StateDir, cfg.Port)
	}
}

func TestConfigFromEnvSuffixAndURL(t *testing.T) {
	for in, want := range map[string]string{
		"US-East-1.DB.Example.com":    "us-east-1.db.example.com",
		".us-east-1.db.example.com.":  "us-east-1.db.example.com",
		" us-east-1.db.example.com\n": "us-east-1.db.example.com",
		"..db.example.com..":          "db.example.com",
	} {
		env := goodEnv(t)
		env["LOOMPLANE_DBGW_SUFFIX"] = in
		cfg, err := ConfigFromEnv(getenv(env))
		if err != nil {
			t.Errorf("suffix %q: %v", in, err)
			continue
		}
		if string(cfg.Suffix) != want {
			t.Errorf("suffix %q = %q, want %q", in, cfg.Suffix, want)
		}
	}

	// The gateway looks hosts up as <label>.<suffix>, so a suffix that
	// can't be a DNS name would match nothing, or the wrong thing.
	for _, in := range []string{"", ".", "a b.example.com", "a/b.example.com", "a..b.example.com", "-a.example.com", "a_b.example.com"} {
		env := goodEnv(t)
		env["LOOMPLANE_DBGW_SUFFIX"] = in
		if _, err := ConfigFromEnv(getenv(env)); err == nil {
			t.Errorf("suffix %q accepted", in)
		}
	}

	for in, want := range map[string]string{
		"https://loomplane.example.com/":     "https://loomplane.example.com",
		"https://loomplane.example.com/base": "https://loomplane.example.com/base",
		"http://localhost:3000":              "http://localhost:3000",
		"http://127.0.0.1:3000/":             "http://127.0.0.1:3000",
		"http://[::1]:3000":                  "http://[::1]:3000",
	} {
		env := goodEnv(t)
		env["LOOMPLANE_DBGW_CONTROL_URL"] = in
		cfg, err := ConfigFromEnv(getenv(env))
		if err != nil {
			t.Errorf("control URL %q: %v", in, err)
			continue
		}
		if cfg.ControlURL != want {
			t.Errorf("control URL %q = %q, want %q", in, cfg.ControlURL, want)
		}
	}
}

func TestConfigFromEnvRejects(t *testing.T) {
	seed := b64(make([]byte, 32))
	cases := []struct {
		name, key, value string
	}{
		{"missing id", "LOOMPLANE_DBGW_ID", ""},
		{"id with a slash", "LOOMPLANE_DBGW_ID", "gw/1"},
		{"id with a space", "LOOMPLANE_DBGW_ID", "gw 1"},
		{"missing control url", "LOOMPLANE_DBGW_CONTROL_URL", ""},
		{"http control url", "LOOMPLANE_DBGW_CONTROL_URL", "http://loomplane.example.com"},
		{"http lookalike of localhost", "LOOMPLANE_DBGW_CONTROL_URL", "http://localhost.example.com"},
		{"control url without a scheme", "LOOMPLANE_DBGW_CONTROL_URL", "loomplane.example.com"},
		{"ftp control url", "LOOMPLANE_DBGW_CONTROL_URL", "ftp://loomplane.example.com"},
		{"control url with a password", "LOOMPLANE_DBGW_CONTROL_URL", "https://user:pw@loomplane.example.com"},
		{"control url with a query", "LOOMPLANE_DBGW_CONTROL_URL", "https://loomplane.example.com/?a=1"},
		{"missing signing key", "LOOMPLANE_DBGW_SIGNING_KEY", ""},
		{"signing key not base64", "LOOMPLANE_DBGW_SIGNING_KEY", "not-base64!!"},
		{"signing key too short", "LOOMPLANE_DBGW_SIGNING_KEY", b64(make([]byte, 31))},
		{"signing key is a full private key", "LOOMPLANE_DBGW_SIGNING_KEY", b64(make([]byte, 64))},
		{"missing sealing key", "LOOMPLANE_DBGW_SEALING_KEY", ""},
		{"sealing key not base64", "LOOMPLANE_DBGW_SEALING_KEY", "%%%"},
		{"sealing key too short", "LOOMPLANE_DBGW_SEALING_KEY", b64(make([]byte, 16))},
		{"missing trust keys", "LOOMPLANE_DBGW_TRUST_KEYS", ""},
		{"trust keys not json", "LOOMPLANE_DBGW_TRUST_KEYS", "not json"},
		{"no trust keys", "LOOMPLANE_DBGW_TRUST_KEYS", "[]"},
		{"trust key of the wrong size", "LOOMPLANE_DBGW_TRUST_KEYS", `[{"public_key":"` + seed[:8] + `","not_before":"2026-01-01T00:00:00Z","not_after":"2027-01-01T00:00:00Z"}]`},
		{"trust key with a bad time", "LOOMPLANE_DBGW_TRUST_KEYS", `[{"public_key":"` + seed + `","not_before":"yesterday","not_after":"2027-01-01T00:00:00Z"}]`},
		{"missing suffix", "LOOMPLANE_DBGW_SUFFIX", ""},
		{"port not a number", "PORT", "http"},
		{"port zero", "PORT", "0"},
		{"port too big", "PORT", "70000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := goodEnv(t)
			env[c.key] = c.value
			_, err := ConfigFromEnv(getenv(env))
			if err == nil {
				t.Fatal("ConfigFromEnv accepted it")
			}
			if !strings.Contains(err.Error(), c.key) {
				t.Errorf("error %q doesn't name %s", err, c.key)
			}
			// A key that fails to parse must not be echoed back into logs.
			if c.value != "" && strings.Contains(c.key, "KEY") && !strings.Contains(c.key, "TRUST") && strings.Contains(err.Error(), c.value) {
				t.Errorf("error %q repeats the secret", err)
			}
		})
	}
}

// startedRun is a Run in a test, serving on a loopback listener.
type startedRun struct {
	t      *testing.T
	base   string
	dir    string
	cancel context.CancelFunc
	done   chan struct{} // closed when Run has returned
	err    error         // what Run returned, once done is closed
	store  *Store
	router atomic.Pointer[Router]
}

// runSetup is a Config with keys the test knows.
type runSetup struct {
	cfg   Config
	trust TrustKey
	priv  ed25519.PrivateKey
	pub   ed25519.PublicKey // the gateway's request key

	// minBackoff and maxBackoff override the short test timings when set.
	minBackoff, maxBackoff time.Duration
}

func newRunSetup(t *testing.T, controlURL string) runSetup {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trust, trustPriv := newCurrentTrustKey(t)
	return runSetup{
		cfg: Config{
			GatewayID: "gw-1", ControlURL: controlURL, StateDir: t.TempDir(), Suffix: testSuffix,
			SigningKey: priv, SealingKey: newSealKey(t), TrustKeys: []TrustKey{trust},
		},
		trust: trust, priv: trustPriv, pub: pub,
	}
}

// snapshot is a signed snapshot with one route whose credential opens.
func (s runSetup) snapshot(t *testing.T, version uint64) SignedSnapshot {
	t.Helper()
	out, err := SignSnapshot(s.unsigned(t, version), s.priv, s.trust.ID())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// unsigned is the snapshot that snapshot signs.
func (s runSetup) unsigned(t *testing.T, version uint64) Snapshot {
	t.Helper()
	cred, err := Seal(s.cfg.SealingKey.PublicKey(), []byte(`{"user":"gateway","password":"backend-pw"}`), []byte("backend-credential:route-a"))
	if err != nil {
		t.Fatal(err)
	}
	snap := Snapshot{
		GatewayID: "gw-1", Version: version, IssuedAt: time.Now().UTC().Truncate(time.Second),
		Routes: []Route{{
			ID: "route-a", Label: Label{DB: "orders"}, State: RouteStateActive,
			Backend: Backend{Kind: "postgres", Host: "127.0.0.1", Port: 1, Database: "orders", SSLMode: "disable", Credential: cred},
		}},
	}
	return snap
}

// httpGet is a GET that carries a context.
func httpGet(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}

// start runs the gateway with short timings and returns once the server is
// accepting connections.
func (s runSetup) start(t *testing.T) *startedRun {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &startedRun{
		t: t, base: "http://" + ln.Addr().String(), dir: s.cfg.StateDir, cancel: cancel,
		done: make(chan struct{}), store: &Store{},
	}
	opts := runOptions{
		listener: ln, store: r.store, routerHook: func(rt *Router) { r.router.Store(rt) },
		minBackoff: 2 * time.Millisecond, maxBackoff: 10 * time.Millisecond,
		reportEvery: 20 * time.Millisecond, pollGap: 2 * time.Millisecond,
	}
	if s.minBackoff != 0 {
		opts.minBackoff, opts.maxBackoff = s.minBackoff, s.maxBackoff
	}
	go func() {
		r.err = run(ctx, s.cfg, opts)
		close(r.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(20 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	waitUntil(t, "the server to accept connections", func() bool {
		resp, err := httpGet(t.Context(), r.base+"/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	})
	return r
}

func (r *startedRun) healthz() int {
	resp, err := httpGet(r.t.Context(), r.base+"/healthz")
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunServesLastGoodWhenControlDown(t *testing.T) {
	// Nothing listens on port 1, so every poll and report fails.
	s := newRunSetup(t, "http://127.0.0.1:1")
	snap := s.snapshot(t, 5)
	if err := SaveLastGood(filepath.Join(s.cfg.StateDir, "last-good"), s.cfg.SealingKey, snap); err != nil {
		t.Fatal(err)
	}
	r := s.start(t)

	waitUntil(t, "healthz to return 200", func() bool { return r.healthz() == http.StatusOK })
	cur := r.store.Current()
	if cur == nil || cur.Version != 5 || len(cur.Routes) != 1 || cur.Routes[0].ID != "route-a" {
		t.Fatalf("Current() = %+v, want the last-good snapshot", cur)
	}

	// The transports are mounted: /v2 wants an address, /sql answers a
	// preflight, and anything else is not found.
	resp, err := httpGet(t.Context(), r.base+"/v2")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("GET /v2 = %d, want 400", resp.StatusCode)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodOptions, r.base+"/sql", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Errorf("OPTIONS /sql = %d, want 2xx", resp.StatusCode)
	}
	resp, err = httpGet(t.Context(), r.base+"/nope")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /nope = %d, want 404", resp.StatusCode)
	}
}

func TestRunIgnoresUntrustedLastGood(t *testing.T) {
	s := newRunSetup(t, "http://127.0.0.1:1")
	// Sealed to the right key, but signed by a key nobody trusts.
	otherTrust, otherPriv := newCurrentTrustKey(t)
	bad, err := SignSnapshot(s.unsigned(t, 5), otherPriv, otherTrust.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveLastGood(filepath.Join(s.cfg.StateDir, "last-good"), s.cfg.SealingKey, bad); err != nil {
		t.Fatal(err)
	}
	r := s.start(t)

	time.Sleep(100 * time.Millisecond)
	if got := r.healthz(); got != http.StatusServiceUnavailable {
		t.Errorf("healthz = %d, want 503", got)
	}
	if r.store.Current() != nil {
		t.Error("an untrusted last-good snapshot was applied")
	}
}

func TestRunRejectsBadSnapshot(t *testing.T) {
	otherTrust, otherPriv := newCurrentTrustKey(t)
	// Each case is a refusable version 2, served after version 1 was applied.
	for name, mk := range map[string]func(s runSetup, t *testing.T) SignedSnapshot{
		"unsigned": func(s runSetup, t *testing.T) SignedSnapshot {
			snap := s.snapshot(t, 2)
			snap.Signature, snap.KeyID = nil, ""
			return snap
		},
		"signed by an untrusted key": func(s runSetup, t *testing.T) SignedSnapshot {
			snap, err := SignSnapshot(s.unsigned(t, 2), otherPriv, otherTrust.ID())
			if err != nil {
				t.Fatal(err)
			}
			return snap
		},
		"tampered": func(s runSetup, t *testing.T) SignedSnapshot {
			snap := s.snapshot(t, 2)
			snap.Payload = bytes.Replace(snap.Payload, []byte(`"127.0.0.1"`), []byte(`"10.9.9.9"`), 1)
			return snap
		},
		"for another gateway": func(s runSetup, t *testing.T) SignedSnapshot {
			other := s.unsigned(t, 2)
			other.GatewayID = "gw-2"
			snap, err := SignSnapshot(other, s.priv, s.trust.ID())
			if err != nil {
				t.Fatal(err)
			}
			return snap
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newRunSetup(t, "")
			// Long enough that a tight loop is easy to tell from a backoff.
			s.minBackoff, s.maxBackoff = 20*time.Millisecond, 40*time.Millisecond
			ctrl := newFakeControl(t, s.pub)
			s.cfg.ControlURL = ctrl.srv.URL
			good, bad := s.snapshot(t, 1), mk(s, t)
			ctrl.snapshot = func(after uint64) *SignedSnapshot {
				if after < 1 {
					return &good
				}
				return &bad
			}
			r := s.start(t)

			// Version 1 is applied and saved; version 2 is then refused.
			waitUntil(t, "version 1 to be saved", func() bool {
				saved, err := LoadLastGood(filepath.Join(r.dir, "last-good"), s.cfg.SealingKey)
				return err == nil && versionOf(t, saved) == 1
			})
			waitUntil(t, "version 2 to be polled for", func() bool { return ctrl.pollCount() >= 3 })

			// A refused snapshot is not asked for again faster than the
			// backoff allows. Gaps are at least minBackoff, so a window of
			// w holds at most w/minBackoff polls (plus one at the edge).
			const window = 400 * time.Millisecond
			before := ctrl.pollCount()
			time.Sleep(window)
			polls := ctrl.pollCount() - before
			if limit := int(window/s.minBackoff) + 1; polls > limit {
				t.Errorf("%d polls in %v after a refused snapshot, want at most %d", polls, window, limit)
			}
			if polls == 0 {
				t.Error("the gateway stopped polling after a refused snapshot")
			}

			if cur := r.store.Current(); cur == nil || cur.Version != 1 {
				t.Fatalf("Current() = %+v, want version 1 unchanged", cur)
			}
			if got := r.healthz(); got != http.StatusOK {
				t.Errorf("healthz = %d, want 200 (version 1 is still served)", got)
			}
			saved, err := LoadLastGood(filepath.Join(r.dir, "last-good"), s.cfg.SealingKey)
			if err != nil {
				t.Fatalf("last-good: %v", err)
			}
			if v := versionOf(t, saved); v != 1 {
				t.Errorf("last-good = version %d, want version 1", v)
			}
		})
	}
}

func TestRunRefusesBadFirstSnapshot(t *testing.T) {
	s := newRunSetup(t, "")
	ctrl := newFakeControl(t, s.pub)
	s.cfg.ControlURL = ctrl.srv.URL
	bad := s.snapshot(t, 3)
	bad.Signature, bad.KeyID = nil, ""
	ctrl.snapshot = func(uint64) *SignedSnapshot { return &bad }
	r := s.start(t)

	waitUntil(t, "three polls", func() bool { return ctrl.pollCount() >= 3 })
	if cur := r.store.Current(); cur != nil {
		t.Fatalf("Current() = version %d, want none", cur.Version)
	}
	if got := r.healthz(); got != http.StatusServiceUnavailable {
		t.Errorf("healthz = %d, want 503", got)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "last-good")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused snapshot was saved as last-good: %v", err)
	}
}

func TestRunAppliesSnapshotAndReports(t *testing.T) {
	s := newRunSetup(t, "")
	ctrl := newFakeControl(t, s.pub)
	s.cfg.ControlURL = ctrl.srv.URL
	ctrl.live = 3
	snap := s.snapshot(t, 1)
	ctrl.snapshot = func(after uint64) *SignedSnapshot {
		if after >= 1 {
			return nil
		}
		return &snap
	}
	r := s.start(t)

	waitUntil(t, "healthz to return 200", func() bool { return r.healthz() == http.StatusOK })
	if cur := r.store.Current(); cur == nil || cur.Version != 1 {
		t.Fatalf("Current() = %+v, want version 1", cur)
	}
	// The next poll asks for what is newer than what it has.
	waitUntil(t, "a poll after version 1", func() bool {
		ctrl.mu.Lock()
		defer ctrl.mu.Unlock()
		return len(ctrl.polls) >= 2 && ctrl.polls[0] == 0 && ctrl.polls[1] == 1
	})

	// The snapshot was saved sealed, and opens to what was applied.
	saved, err := LoadLastGood(filepath.Join(r.dir, "last-good"), s.cfg.SealingKey)
	if err != nil {
		t.Fatalf("LoadLastGood: %v", err)
	}
	if !reflect.DeepEqual(saved, snap) {
		t.Errorf("last-good = %+v, want %+v", saved, snap)
	}

	// Reports carry the instance, its version and its routes, and the
	// answer sizes the pools.
	waitUntil(t, "a report with version 1", func() bool {
		rep, ok := ctrl.lastReport()
		return ok && rep.SnapshotVersion == 1
	})
	rep, _ := ctrl.lastReport()
	if rep.InstanceID == "" || rep.Draining || len(rep.Routes) != 1 || rep.Routes[0].RouteID != "route-a" {
		t.Errorf("report = %+v", rep)
	}
	ctrl.mu.Lock()
	for _, other := range ctrl.reports {
		if other.InstanceID != rep.InstanceID {
			t.Errorf("instance ID changed between reports: %q and %q", other.InstanceID, rep.InstanceID)
		}
	}
	ctrl.mu.Unlock()
	waitUntil(t, "SetInstances(3)", func() bool {
		rt := r.router.Load()
		return rt != nil && rt.instances.Load() == 3
	})

	// A newer snapshot replaces it.
	next := s.snapshot(t, 2)
	ctrl.mu.Lock()
	ctrl.snapshot = func(after uint64) *SignedSnapshot {
		if after >= 2 {
			return nil
		}
		return &next
	}
	ctrl.mu.Unlock()
	waitUntil(t, "version 2", func() bool { cur := r.store.Current(); return cur != nil && cur.Version == 2 })
	waitUntil(t, "version 2 to be saved", func() bool {
		saved, err := LoadLastGood(filepath.Join(r.dir, "last-good"), s.cfg.SealingKey)
		return err == nil && versionOf(t, saved) == 2
	})
}

// A newer control plane can add snapshot fields this gateway doesn't know:
// the snapshot still verifies, applies and is kept as last-good byte for
// byte.
func TestRunAppliesSnapshotWithUnknownFields(t *testing.T) {
	s := newRunSetup(t, "")
	ctrl := newFakeControl(t, s.pub)
	s.cfg.ControlURL = ctrl.srv.URL

	snap := s.unsigned(t, 4)
	snap.KeyID = s.trust.ID()
	base, err := SnapshotPayload(snap)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err = json.Unmarshal(base, &m); err != nil {
		t.Fatal(err)
	}
	m["replicas"] = []map[string]any{{"region": "eu-west-1", "lag_ms": 12}}
	payload, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	env := SignedSnapshot{
		Payload: payload, KeyID: s.trust.ID(),
		Signature: ed25519.Sign(s.priv, append([]byte(snapshotContext), payload...)),
	}
	ctrl.snapshot = func(after uint64) *SignedSnapshot {
		if after >= 4 {
			return nil
		}
		return &env
	}
	r := s.start(t)

	waitUntil(t, "healthz to return 200", func() bool { return r.healthz() == http.StatusOK })
	if cur := r.store.Current(); cur == nil || cur.Version != 4 || len(cur.Routes) != 1 || cur.Routes[0].ID != "route-a" {
		t.Fatalf("Current() = %+v, want version 4 with route-a", cur)
	}
	waitUntil(t, "the snapshot to be saved", func() bool {
		saved, err := LoadLastGood(filepath.Join(r.dir, "last-good"), s.cfg.SealingKey)
		return err == nil && bytes.Equal(saved.Payload, payload)
	})
}

func TestRunRemovesStaleTempFiles(t *testing.T) {
	s := newRunSetup(t, "http://127.0.0.1:1")
	stale := filepath.Join(s.cfg.StateDir, "last-good.tmp-123456")
	keep := filepath.Join(s.cfg.StateDir, "unrelated.tmp-1")
	for _, p := range []string{stale, keep} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s.start(t)

	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stale temp file is still there: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("an unrelated file was removed: %v", err)
	}
}

func TestRunDrainsOnCancel(t *testing.T) {
	s := newRunSetup(t, "")
	ctrl := newFakeControl(t, s.pub)
	s.cfg.ControlURL = ctrl.srv.URL
	snap := s.snapshot(t, 1)
	ctrl.snapshot = func(after uint64) *SignedSnapshot {
		if after >= 1 {
			return nil
		}
		return &snap
	}
	r := s.start(t)
	waitUntil(t, "healthz to return 200", func() bool { return r.healthz() == http.StatusOK })
	rt := r.router.Load()
	if rt == nil || rt.Draining() {
		t.Fatal("router missing or draining before cancel")
	}

	r.cancel()
	select {
	case <-r.done:
		if r.err != nil {
			t.Fatalf("Run = %v, want nil after a clean drain", r.err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if !rt.Draining() {
		t.Error("the router was not drained")
	}
	// The instance said it was draining before it went.
	rep, ok := ctrl.lastReport()
	if !ok || !rep.Draining {
		t.Errorf("last report = %+v, want one with draining set", rep)
	}
	// The listener is closed.
	if conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", strings.TrimPrefix(r.base, "http://")); err == nil {
		conn.Close()
		t.Error("the server still accepts connections")
	}
}

func TestRunFailsWhenItCannotListen(t *testing.T) {
	// Hold the wildcard address, which is what Run listens on.
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	s := newRunSetup(t, "http://127.0.0.1:1")
	s.cfg.Port = ln.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := Run(ctx, s.cfg); err == nil {
		t.Fatal("Run succeeded on a port that is taken")
	}
}

func TestHealthzFollowsReadinessAndDrain(t *testing.T) {
	h := newRouterHarness(t, nil, nil)
	g := &gateway{router: h.r}
	status := func() int {
		rec := httptest.NewRecorder()
		g.healthz(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil))
		return rec.Code
	}
	if got := status(); got != http.StatusServiceUnavailable {
		t.Errorf("before a snapshot: %d, want 503", got)
	}
	g.ready.Store(true)
	if got := status(); got != http.StatusOK {
		t.Errorf("with a snapshot: %d, want 200", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h.r.Drain(ctx)
	if got := status(); got != http.StatusServiceUnavailable {
		t.Errorf("while draining: %d, want 503", got)
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	s := newRunSetup(t, "http://127.0.0.1:1")
	for name, mut := range map[string]func(*Config){
		"no id":            func(c *Config) { c.GatewayID = "" },
		"no control url":   func(c *Config) { c.ControlURL = "" },
		"no signing key":   func(c *Config) { c.SigningKey = nil },
		"no sealing key":   func(c *Config) { c.SealingKey = nil },
		"no trust keys":    func(c *Config) { c.TrustKeys = nil },
		"no suffix":        func(c *Config) { c.Suffix = "" },
		"no state dir":     func(c *Config) { c.StateDir = "" },
		"http control url": func(c *Config) { c.ControlURL = "http://loomplane.example.com" },
		"control url with a password": func(c *Config) {
			c.ControlURL = "https://user:pw@loomplane.example.com"
		},
		"control url with a query":     func(c *Config) { c.ControlURL = "https://loomplane.example.com/?a=1" },
		"control url without a scheme": func(c *Config) { c.ControlURL = "loomplane.example.com" },
		"short trust key": func(c *Config) {
			c.TrustKeys = []TrustKey{{PublicKey: make([]byte, 8), NotAfter: time.Now().Add(time.Hour)}}
		},
	} {
		cfg := s.cfg
		mut(&cfg)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := Run(ctx, cfg); err == nil {
			t.Errorf("%s: Run succeeded", name)
		}
		cancel()
	}
}
