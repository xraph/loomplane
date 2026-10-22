package catalogproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
	"github.com/xraph/loomplane/internal/security"
)

// adminToken is the generated secret the sample target sends upstream.
const adminToken = "s3cret-admin-token"

// fakeClock is a clock the test moves.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// memStore is a Store in memory.
type memStore struct {
	mu   sync.Mutex
	rows map[string]Record
}

func newMemStore() *memStore { return &memStore{rows: map[string]Record{}} }

func (s *memStore) Create(_ context.Context, r *Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[r.TokenHash] = *r
	return nil
}

func (s *memStore) Get(_ context.Context, tokenHash string) (*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[tokenHash]
	if !ok {
		return nil, ErrNotFound
	}
	return &r, nil
}

func (s *memStore) Touch(_ context.Context, tokenHash string, usedAt, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[tokenHash]
	if !ok {
		return ErrNotFound
	}
	r.LastUsedAt, r.ExpiresAt = usedAt, expiresAt
	s.rows[tokenHash] = r
	return nil
}

func (s *memStore) Delete(_ context.Context, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, tokenHash)
	return nil
}

func (s *memStore) DeleteResource(_ context.Context, orgID, environmentID, resource string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, r := range s.rows {
		if r.OrgID == orgID && r.EnvironmentID == environmentID && r.Resource == resource {
			delete(s.rows, k)
		}
	}
	return nil
}

func (s *memStore) Prune(_ context.Context, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, r := range s.rows {
		if r.ExpiresAt.Before(before) {
			delete(s.rows, k)
		}
	}
	return nil
}

func (s *memStore) all() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.rows))
	for _, r := range s.rows {
		out = append(out, r)
	}
	return out
}

// fakeAuthz lets everyone but "developer" and the users in denied modify
// infrastructure, and counts its checks.
type fakeAuthz struct {
	mu     sync.Mutex
	denied map[string]bool
	checks []string
}

func (a *fakeAuthz) Require(_ context.Context, p security.Principal, perm security.Permission, resourceID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.checks = append(a.checks, p.OrgID+" "+p.UserID+" "+perm.Name()+" "+resourceID)
	if p.UserID == "developer" || a.denied[p.UserID] {
		return security.ErrForbidden
	}
	return nil
}

func (a *fakeAuthz) deny(user string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.denied == nil {
		a.denied = map[string]bool{}
	}
	a.denied[user] = true
}

func (a *fakeAuthz) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.checks)
}

// fakeSecrets is the generated secrets Vault keeps.
type fakeSecrets map[string][]byte

func (s fakeSecrets) Put(context.Context, string, []byte) error { return nil }
func (s fakeSecrets) Delete(context.Context, string) error      { return nil }
func (s fakeSecrets) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := s[key]
	if !ok {
		return nil, deployments.ErrNoCredential
	}
	return v, nil
}

// upstream stands in for the component's admin port. It records each
// request it gets.
type upstream struct {
	mu       sync.Mutex
	requests []*http.Request
	handler  http.HandlerFunc
	// transport, when set, answers instead of handler.
	transport http.RoundTripper
}

func (u *upstream) RoundTrip(r *http.Request) (*http.Response, error) {
	u.mu.Lock()
	u.requests = append(u.requests, r.Clone(context.Background()))
	h, rt := u.handler, u.transport
	u.mu.Unlock()
	if rt != nil {
		return rt.RoundTrip(r)
	}
	w := httptest.NewRecorder()
	if h == nil {
		h = func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok "+r.URL.RequestURI()) }
	}
	h(w, r)
	res := w.Result()
	res.Request = r
	return res, nil
}

func (u *upstream) seen() []*http.Request {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.requests)
}

// componentDialer is a target that reaches every component through up.
type componentDialer struct {
	deployments.Executor
	up *upstream
}

func (d componentDialer) ComponentTransport(context.Context, *planning.Binding, int) (http.RoundTripper, io.Closer, error) {
	return d.up, io.NopCloser(nil), nil
}

// sampleBindings are a deployed "app" of the sample kind and a "db"
// PostgreSQL in environment env of project proj.
func sampleBindings(t *testing.T) []*planning.Binding {
	t.Helper()
	composition, err := recipestest.Registry(t).Resolve("sample", "1", "amd64", "server")
	if err != nil {
		t.Fatal(err)
	}
	out := make([]*planning.Binding, 0, len(composition.Components)+2)
	for _, c := range composition.Components {
		raw, _ := json.Marshal(planning.ResourceConfig{Provider: "server", Target: "conn", Kind: "sample", Version: "1", Component: &planning.ComponentConfig{Kind: "sample", Resource: "app", Revision: composition.Revision, Component: c}})
		out = append(out, &planning.Binding{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ResourceSubject, Name: planning.ComponentActionName("app", c.Name), Provider: "server", ConnectionID: "conn", ExternalID: "inst_" + c.Name, Config: raw})
	}
	sample := recipestest.Sample()
	parent, _ := json.Marshal(planning.ResourceConfig{Provider: "server", Target: "conn", Kind: "sample", Version: "1", Composition: &planning.CompositionConfig{Kind: "sample", Version: "1", Revision: composition.Revision, Outputs: sample.Outputs, Secrets: sample.Secrets}})
	out = append(out, &planning.Binding{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ResourceSubject, Name: "app", Provider: "server", ConnectionID: "conn", ExternalID: "catalog/app", Config: parent})
	db, _ := json.Marshal(planning.ResourceConfig{Provider: "server", Target: "conn", Kind: "postgres", Version: "17"})
	out = append(out, &planning.Binding{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ResourceSubject, Name: "db", Provider: "server", ConnectionID: "conn", ExternalID: "inst_db", Config: db})
	return out
}

// sampleTarget is an Octopus-like admin under /admin that wants its
// token in X-Octopus-Admin-Token.
func sampleTarget() Target {
	return Target{
		Component: "api", Port: "admin", Root: "/admin", AllowedPrefixes: []string{"/admin"},
		Header: func(_ context.Context, secret func(string) ([]byte, error)) (http.Header, error) {
			token, err := secret("admin-password")
			if err != nil {
				return nil, err
			}
			return http.Header{"X-Octopus-Admin-Token": {string(token)}}, nil
		},
	}
}

// useTarget registers target for kind "sample" for one test.
func useTarget(t *testing.T, target Target) {
	t.Helper()
	targetsMu.Lock()
	old, had := targets["sample"]
	targetsMu.Unlock()
	t.Cleanup(func() {
		targetsMu.Lock()
		defer targetsMu.Unlock()
		if had {
			targets["sample"] = old
		} else {
			delete(targets, "sample")
		}
	})
	Register("sample", target)
}

type harness struct {
	// tick paces the checks of streaming responses.
	tick     chan time.Time
	checked  chan struct{}
	warnMu   sync.Mutex
	warnings []string
	svc      *Service
	store    *memStore
	authz    *fakeAuthz
	clock    *fakeClock
	up       *upstream
	handler  http.Handler
}

func newHarness(t *testing.T, target Target) *harness {
	t.Helper()
	useTarget(t, target)
	bindings := sampleBindings(t)
	list := func(_ context.Context, orgID, environmentID string) ([]*planning.Binding, error) {
		var out []*planning.Binding
		for _, b := range bindings {
			if b.OrgID == orgID && b.EnvironmentID == environmentID {
				out = append(out, b)
			}
		}
		return out, nil
	}
	h := &harness{store: newMemStore(), authz: &fakeAuthz{}, clock: &fakeClock{t: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)}, up: &upstream{}}
	access := deployments.NewCatalogAccess(deployments.CatalogAccessDeps{
		Authz: h.authz, Bindings: list,
		Executors: map[string]deployments.Executor{"server": componentDialer{up: h.up}},
		Secrets:   fakeSecrets{deployments.ResourceKey("org", "env", "app", "secret:admin-password"): []byte(adminToken)},
	})
	h.svc = NewService(Deps{Store: h.store, Access: access, Authz: h.authz, Bindings: list, Now: h.clock.Now, Warn: func(msg string, err error) {
		h.warnMu.Lock()
		defer h.warnMu.Unlock()
		h.warnings = append(h.warnings, msg+": "+err.Error())
	}})
	h.tick = make(chan time.Time)
	h.svc.ticker = func() (<-chan time.Time, func()) { return h.tick, func() {} }
	h.checked = make(chan struct{}, 1)
	h.svc.checkedStream = func() { h.checked <- struct{}{} }
	h.handler = h.svc.Handler()
	return h
}

var admin = security.Principal{OrgID: "org", UserID: "admin"}

var appRef = deployments.ResourceRef{ProjectID: "proj", EnvironmentID: "env", Resource: "app"}

// open opens a session as admin and returns its session prefix,
// /catalog-admin/<token>.
func (h *harness) open(t *testing.T) string {
	t.Helper()
	s, err := h.svc.Open(context.Background(), admin, appRef)
	if err != nil {
		t.Fatal(err)
	}
	prefix, _, ok := strings.Cut(strings.TrimPrefix(s.URL, "/catalog-admin/"), "/")
	if !ok {
		t.Fatalf("session URL %q", s.URL)
	}
	return "/catalog-admin/" + prefix
}

func (h *harness) do(method, target string, header http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), method, target, nil)
	for k, v := range header {
		r.Header[k] = v
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w
}

func TestProxyInjectsTargetHeader(t *testing.T) {
	h := newHarness(t, sampleTarget())
	base := h.open(t)
	w := h.do(http.MethodGet, base+"/admin/routes?page=2", nil)
	if w.Code != http.StatusOK || w.Body.String() != "ok /admin/routes?page=2" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
	seen := h.up.seen()
	if len(seen) != 1 {
		t.Fatalf("upstream saw %d requests", len(seen))
	}
	r := seen[0]
	if got := r.Header.Values("X-Octopus-Admin-Token"); len(got) != 1 || got[0] != adminToken {
		t.Fatalf("admin token header: %q", got)
	}
	if r.URL.Host != "component" || r.Header.Get("Accept-Encoding") != "identity" {
		t.Fatalf("upstream request: host %q, accept-encoding %q", r.URL.Host, r.Header.Get("Accept-Encoding"))
	}
}

func TestProxyStripsBrowserCredentials(t *testing.T) {
	h := newHarness(t, sampleTarget())
	base := h.open(t)
	w := h.do(http.MethodGet, base+"/admin", http.Header{
		"Cookie":                {"lp_session=browser-session"},
		"Authorization":         {"Bearer lp_key"},
		"Proxy-Authorization":   {"Basic eA=="},
		"X-Octopus-Admin-Token": {"forged"},
		"X-Requested-With":      {"XMLHttpRequest"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
	r := h.up.seen()[0]
	for _, name := range []string{"Cookie", "Authorization", "Proxy-Authorization"} {
		if v := r.Header.Values(name); len(v) != 0 {
			t.Fatalf("%s reached the component: %q", name, v)
		}
	}
	if got := r.Header.Values("X-Octopus-Admin-Token"); len(got) != 1 || got[0] != adminToken {
		t.Fatalf("a forged admin token reached the component: %q", got)
	}
	if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
		t.Fatal("an ordinary header was dropped")
	}
}

func TestProxyResponsesAreSandboxed(t *testing.T) {
	h := newHarness(t, sampleTarget())
	h.up.handler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Set-Cookie", "octopus_session=abc; Path=/")
		w.Header().Set("Content-Security-Policy", "default-src *")
		w.Header().Set("Content-Security-Policy-Report-Only", "default-src *")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Strict-Transport-Security", "max-age=63072000")
		w.Header().Set("Clear-Site-Data", `"cookies"`)
		w.Header().Set("Referrer-Policy", "unsafe-url")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Octopus-Version", "1.0")
		w.Header().Set("X-Octopus-Admin-Token", adminToken)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}
	base := h.open(t)
	w := h.do(http.MethodGet, base+"/admin/api/stats", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
	want := map[string]string{
		"Content-Security-Policy":     "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads",
		"Referrer-Policy":             "no-referrer",
		"X-Content-Type-Options":      "nosniff",
		"Cache-Control":               "no-store",
		"Access-Control-Allow-Origin": "null",
		"Content-Type":                "application/json",
		"X-Octopus-Version":           "1.0",
	}
	for k, v := range want {
		if got := w.Header().Values(k); len(got) != 1 || got[0] != v {
			t.Errorf("%s: %q, want %q", k, got, v)
		}
	}
	for _, k := range []string{"X-Octopus-Admin-Token", "Set-Cookie", "Content-Security-Policy-Report-Only", "Access-Control-Allow-Credentials", "Strict-Transport-Security", "Clear-Site-Data"} {
		if got := w.Header().Values(k); len(got) != 0 {
			t.Errorf("%s passed through: %q", k, got)
		}
	}
	// Loomplane's own answers are sandboxed too.
	w = h.do(http.MethodGet, base+"/metrics", nil)
	if w.Code != http.StatusNotFound || w.Header().Get("Content-Security-Policy") != want["Content-Security-Policy"] || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("refusal: %d %v", w.Code, w.Header())
	}
}

func TestProxyRefusesPathsOutsideAllowedPrefixes(t *testing.T) {
	h := newHarness(t, sampleTarget())
	base := h.open(t)
	for _, p := range []string{
		"/_farp/v1/register", "/metrics", "/__farp/x", "/administrator", "/admin/../metrics",
		"/admin/%2e%2e/metrics", "/admin/%2E%2E/%2E%2E/metrics", "/admin%2f..%2fmetrics", "/admin/x%2Fy",
		"/admin/./x", "/admin/%2e/x", "/admin/%00", "/admin/a%5cb",
		"/admin/..;/metrics", "/admin/..;jsessionid=x/metrics", "/admin/%2e%2e;x/metrics", "/admin/.;/x",
	} {
		if w := h.do(http.MethodGet, base+p, nil); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d %q", p, w.Code, w.Body.String())
		}
	}
	if n := len(h.up.seen()); n != 0 {
		t.Fatalf("the component got %d requests", n)
	}
	// An unknown token is a 404 too, without reaching anything.
	if w := h.do(http.MethodGet, "/catalog-admin/"+strings.Repeat("A", 43)+"/admin", nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown token: %d", w.Code)
	}
	if w := h.do(http.MethodGet, "/catalog-admin/short/admin", nil); w.Code != http.StatusNotFound {
		t.Fatalf("malformed token: %d", w.Code)
	}
	// Double slashes fold away; a trailing slash is kept.
	for p, want := range map[string]string{"/admin//api///stats": "/admin/api/stats", "/admin/": "/admin/", "/admin/a%20b": "/admin/a%20b"} {
		if w := h.do(http.MethodGet, base+p, nil); w.Code != http.StatusOK || w.Body.String() != "ok "+want {
			t.Errorf("%s: %d %q, want ok %s", p, w.Code, w.Body.String(), want)
		}
	}
}

func TestProxyRefusesOtherMethods(t *testing.T) {
	h := newHarness(t, sampleTarget())
	base := h.open(t)
	for _, method := range []string{http.MethodTrace, "PROPFIND", "TRACK"} {
		if w := h.do(method, base+"/admin", nil); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d", method, w.Code)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if w := h.do(method, base+"/admin", nil); w.Code != http.StatusOK {
			t.Errorf("%s: %d", method, w.Code)
		}
	}
	if n := len(h.up.seen()); n != 6 {
		t.Fatalf("the component got %d requests, want 6", n)
	}
}

func TestProxyRedirectsTheSessionRootToTheTargetRoot(t *testing.T) {
	h := newHarness(t, sampleTarget())
	base := h.open(t)
	for _, p := range []string{base, base + "/"} {
		w := h.do(http.MethodGet, p, nil)
		if w.Code != http.StatusFound || w.Header().Get("Location") != base+"/admin" {
			t.Fatalf("%s: %d %q", p, w.Code, w.Header().Get("Location"))
		}
	}
	if n := len(h.up.seen()); n != 0 {
		t.Fatalf("the component got %d requests", n)
	}
}

func TestProxyAnswersPreflightForOpaqueOrigin(t *testing.T) {
	h := newHarness(t, sampleTarget())
	base := h.open(t)
	w := h.do(http.MethodOptions, base+"/admin/api/routes", http.Header{
		"Origin":                         {"null"},
		"Access-Control-Request-Method":  {"PUT"},
		"Access-Control-Request-Headers": {"content-type, hx-request"},
	})
	if w.Code != http.StatusNoContent {
		t.Fatalf("got %d", w.Code)
	}
	want := map[string]string{
		"Access-Control-Allow-Origin":  "null",
		"Access-Control-Allow-Methods": "GET, POST, PUT, PATCH, DELETE",
		"Access-Control-Allow-Headers": "content-type, hx-request",
		"Access-Control-Max-Age":       "600",
	}
	for k, v := range want {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s: %q, want %q", k, got, v)
		}
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("credentials allowed: %q", got)
	}
	if n := len(h.up.seen()); n != 0 {
		t.Fatalf("the component got %d requests", n)
	}
}

func TestProxyStripsTargetPrefix(t *testing.T) {
	h := newHarness(t, Target{Component: "api", Port: "admin", Root: "/fabriq/", AllowedPrefixes: []string{"/fabriq/"}, StripPrefix: "/fabriq"})
	s, err := h.svc.Open(context.Background(), admin, appRef)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(s.URL, "/fabriq/") {
		t.Fatalf("session URL %q", s.URL)
	}
	base := strings.TrimSuffix(s.URL, "/fabriq/")
	w := h.do(http.MethodGet, base+"/fabriq/admin/meta", nil)
	if w.Code != http.StatusOK || w.Body.String() != "ok /admin/meta" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
	w = h.do(http.MethodGet, base+"/fabriq/", nil)
	if w.Code != http.StatusOK || w.Body.String() != "ok /" {
		t.Fatalf("root: %d %q", w.Code, w.Body.String())
	}
	for _, p := range []string{"/fabriq/../metrics", "/fabriqx/admin", "/metrics", "/fabriq/%2e%2e/metrics"} {
		if w := h.do(http.MethodGet, base+p, nil); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	if n := len(h.up.seen()); n != 2 {
		t.Fatalf("the component got %d requests, want 2", n)
	}
}

// fixedTransport answers every request with res.
type fixedTransport struct {
	res func(*http.Request) *http.Response
}

func (f fixedTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f.res(r), nil }

func TestProxyWebSocketWithoutUpgradeSupportIs502(t *testing.T) {
	upgrade := http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}, "Sec-Websocket-Key": {"dGhlIHNhbXBsZSBub25jZQ=="}, "Sec-Websocket-Version": {"13"}}
	for name, res := range map[string]func(*http.Request) *http.Response{
		// A target whose channel can't switch protocols answers 101 with a
		// body that can't be written to.
		"read-only body": func(r *http.Request) *http.Response {
			return &http.Response{StatusCode: http.StatusSwitchingProtocols, Header: http.Header{"Upgrade": {"websocket"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}
		},
		"no switch": func(r *http.Request) *http.Response {
			return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("no")), Request: r}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, sampleTarget())
			h.up.transport = fixedTransport{res}
			base := h.open(t)
			w := h.do(http.MethodGet, base+"/admin/ws", upgrade)
			if w.Code != http.StatusBadGateway || strings.TrimSpace(w.Body.String()) != "This page's live updates aren't available through Loomplane on this target." {
				t.Fatalf("got %d %q", w.Code, w.Body.String())
			}
			r := h.up.seen()[0]
			if r.Header.Get("Upgrade") != "websocket" || r.Header.Get("X-Octopus-Admin-Token") != adminToken {
				t.Fatalf("upstream upgrade request: %v", r.Header)
			}
		})
	}
}

// pipeBody is the component's end of a switched connection.
type pipeBody struct{ net.Conn }

func TestProxyWebSocketPassesThroughWhenTheChannelSwitches(t *testing.T) {
	h := newHarness(t, sampleTarget())
	component, proxySide := net.Pipe()
	go func() {
		// The component echoes one line.
		line, err := bufio.NewReader(component).ReadString('\n')
		if err == nil {
			_, _ = io.WriteString(component, "echo "+line)
		}
		_ = component.Close()
	}()
	h.up.transport = fixedTransport{func(r *http.Request) *http.Response {
		return &http.Response{StatusCode: http.StatusSwitchingProtocols, Header: http.Header{
			"Upgrade": {"websocket"}, "Connection": {"Upgrade"}, "Sec-Websocket-Accept": {"s3pPLMBiTxaQ9kYGzzhZRbK+xOo="}, "Set-Cookie": {"x=y"},
		}, Body: pipeBody{proxySide}, Request: r}
	}}
	base := h.open(t)
	srv := httptest.NewServer(h.handler)
	defer srv.Close()
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(conn, "GET "+base+"/admin/ws HTTP/1.1\r\nHost: loomplane.test\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusSwitchingProtocols || res.Header.Get("Sec-Websocket-Accept") == "" || res.Header.Get("Set-Cookie") != "" ||
		res.Header.Get("Content-Security-Policy") != sandboxPolicy || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("switch: %d %v", res.StatusCode, res.Header)
	}
	_, _ = io.WriteString(conn, "hello\n")
	line, err := br.ReadString('\n')
	if err != nil || line != "echo hello\n" {
		t.Fatalf("echo: %q %v", line, err)
	}
}

func TestProxyChannelFailureIs502(t *testing.T) {
	h := newHarness(t, sampleTarget())
	h.up.transport = errTransport{}
	base := h.open(t)
	w := h.do(http.MethodGet, base+"/admin", nil)
	if w.Code != http.StatusBadGateway || strings.TrimSpace(w.Body.String()) != msgReach {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
	// The detail goes to the operator, without the token.
	h.warnMu.Lock()
	defer h.warnMu.Unlock()
	token := strings.TrimPrefix(base, "/catalog-admin/")
	if len(h.warnings) != 1 || !strings.Contains(h.warnings[0], "connection refused") || !strings.Contains(h.warnings[0], "resource app") || strings.Contains(h.warnings[0], token) {
		t.Fatalf("warnings: %q", h.warnings)
	}
}

func TestProxySecretFailureIsAFixedMessage(t *testing.T) {
	target := sampleTarget()
	target.Header = func(_ context.Context, secret func(string) ([]byte, error)) (http.Header, error) {
		_, err := secret("no-such-secret")
		return nil, err
	}
	h := newHarness(t, target)
	base := h.open(t)
	w := h.do(http.MethodGet, base+"/admin", nil)
	if w.Code != http.StatusBadGateway || strings.TrimSpace(w.Body.String()) != msgSignIn {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
	if n := len(h.up.seen()); n != 0 {
		t.Fatalf("the component got %d requests", n)
	}
}

type errTransport struct{}

func (errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp 10.0.0.9:9090: connection refused")
}
