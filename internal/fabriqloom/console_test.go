package fabriqloom_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/catalogproxy"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/fabriqloom"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
)

// What fabriq-admin writes: the base path in index.html's <base href> and in
// config.js, and relative asset URLs that the base resolves.
const (
	indexHTML = `<!doctype html><html><head><base href="/fabriq/" /><script type="module" crossorigin src="./assets/i.js"></script></head><body><div id="root"></div></body></html>`
	configJS  = `window.__FABRIQ_ADMIN_CONFIG__ = {"basePath":"/fabriq/","apiUrl":"/fabriq/admin","defaultTenant":""};`
)

type memStore struct {
	mu   sync.Mutex
	rows map[string]catalogproxy.Record
}

func (s *memStore) Create(_ context.Context, r *catalogproxy.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[r.TokenHash] = *r
	return nil
}

func (s *memStore) Get(_ context.Context, hash string) (*catalogproxy.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[hash]
	if !ok {
		return nil, catalogproxy.ErrNotFound
	}
	return &r, nil
}

func (s *memStore) Touch(_ context.Context, hash string, usedAt, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rows[hash]
	r.LastUsedAt, r.ExpiresAt = usedAt, expiresAt
	s.rows[hash] = r
	return nil
}

func (s *memStore) Delete(_ context.Context, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, hash)
	return nil
}

func (s *memStore) DeleteResource(context.Context, string, string, string) error { return nil }
func (s *memStore) Prune(context.Context, time.Time) error                       { return nil }

type allowAll struct{}

func (allowAll) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

// fakeAdmin stands in for fabriq-admin's nginx, as the component's channel
// reaches it. It records each request it gets.
type fakeAdmin struct {
	mu       sync.Mutex
	requests []*http.Request
}

func (f *fakeAdmin) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Clone(context.Background()))
	f.mu.Unlock()
	w := httptest.NewRecorder()
	switch r.URL.Path {
	case "/":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, indexHTML)
	case "/config.js":
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = io.WriteString(w, configJS)
	case "/assets/i.js":
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = io.WriteString(w, `console.log("fabriq")`)
	case "/admin/meta":
		// Simulate an upstream that echoes the credential header.
		echo := r.Header.Get(fixtureControlHeader)
		if echo == "" {
			echo = "upstream-echo"
		}
		w.Header().Set(fixtureControlHeader, echo)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
	res := w.Result()
	res.Request = r
	return res, nil
}

func (f *fakeAdmin) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.requests))
	for _, r := range f.requests {
		out = append(out, r.URL.Path)
	}
	return out
}

func (f *fakeAdmin) last() *http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

// dialer hands out rt and records which binding and container port each
// channel was opened for.
type dialer struct {
	deployments.Executor
	rt    http.RoundTripper
	mu    *sync.Mutex
	dials *[]dial
}

type dial struct {
	binding string
	port    int
}

func (d dialer) ComponentTransport(_ context.Context, b *planning.Binding, port int) (http.RoundTripper, io.Closer, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	*d.dials = append(*d.dials, dial{b.Name, port})
	return d.rt, io.NopCloser(nil), nil
}

// fabriqBindings are a deployed "fabriq" stack in environment env of project
// proj, with both projections off.
func fabriqBindings(t *testing.T) []*planning.Binding {
	t.Helper()
	kind, composition, err := recipes.Default().ResolveRevision("fabriq", "1.9", "1", "amd64", "server")
	if err != nil {
		t.Fatal(err)
	}
	composition = composition.Without(map[string]bool{"falkordb": true, "elasticsearch": true})
	var out []*planning.Binding
	for _, c := range composition.Components {
		raw, merr := json.Marshal(planning.ResourceConfig{Provider: "server", Target: "conn", Kind: "fabriq", Version: "1.9", Component: &planning.ComponentConfig{Kind: "fabriq", Resource: "fabriq", Revision: composition.Revision, Component: c}})
		if merr != nil {
			t.Fatal(merr)
		}
		out = append(out, &planning.Binding{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ResourceSubject, Name: planning.ComponentActionName("fabriq", c.Name), Provider: "server", ConnectionID: "conn", ExternalID: "inst_" + c.Name, Config: raw})
	}
	parent, err := json.Marshal(planning.ResourceConfig{Provider: "server", Target: "conn", Kind: "fabriq", Version: "1.9", Composition: &planning.CompositionConfig{Kind: "fabriq", Version: "1.9", Revision: composition.Revision, Secrets: kind.Secrets}})
	if err != nil {
		t.Fatal(err)
	}
	return append(out, &planning.Binding{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ResourceSubject, Name: "fabriq", Provider: "server", ConnectionID: "conn", ExternalID: "catalog/fabriq", Config: parent})
}

func TestAdminTargetIsRegistered(t *testing.T) {
	want := catalogproxy.Target{Component: "admin", Port: "http", Root: "/fabriq/", AllowedPrefixes: []string{"/fabriq/"}, StripPrefix: "/fabriq"}
	got := fabriqloom.AdminTarget
	if got.Component != want.Component || got.Port != want.Port || got.Root != want.Root || got.StripPrefix != want.StripPrefix ||
		len(got.AllowedPrefixes) != 1 || got.AllowedPrefixes[0] != want.AllowedPrefixes[0] {
		t.Fatalf("got %+v", got)
	}
	if got.Header == nil {
		t.Fatal("the proxy has no control credential resolver")
	}
	if fabriqloom.ConsoleBasePath != "/fabriq/" {
		t.Fatalf("base path %q", fabriqloom.ConsoleBasePath)
	}
}

func TestFabriqAdminWorksThroughTheProxy(t *testing.T) {
	bindings := fabriqBindings(t)
	list := func(context.Context, string, string) ([]*planning.Binding, error) { return bindings, nil }
	fake := &fakeAdmin{}
	var dials []dial
	var dialMu sync.Mutex
	access := deployments.NewCatalogAccess(deployments.CatalogAccessDeps{
		Authz: allowAll{}, Bindings: list, Secrets: newMemSecrets(),
		Executors: map[string]deployments.Executor{"server": dialer{rt: fake, mu: &dialMu, dials: &dials}},
	})
	svc := catalogproxy.NewService(catalogproxy.Deps{
		Store: &memStore{rows: map[string]catalogproxy.Record{}}, Access: access, Authz: allowAll{}, Bindings: list,
		Now: func() time.Time { return time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC) },
	})
	handler := svc.Handler()
	session, err := svc.Open(context.Background(), security.Principal{OrgID: "org", UserID: "admin"}, deployments.ResourceRef{ProjectID: "proj", EnvironmentID: "env", Resource: "fabriq"})
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSuffix(session.URL, "/fabriq/")
	if !strings.HasPrefix(base, "/catalog-admin/") || base+"/fabriq/" != session.URL {
		t.Fatalf("session URL %q", session.URL)
	}
	do := func(target string, header http.Header) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
		for k, v := range header {
			r.Header[k] = v
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	// The session's root lands on the console.
	w := do(base+"/", nil)
	if w.Code != http.StatusFound || w.Header().Get("Location") != base+"/fabriq/" {
		t.Fatalf("root: %d to %q", w.Code, w.Header().Get("Location"))
	}

	// The page's base path becomes the session's, and its relative assets
	// follow it.
	w = do(base+"/fabriq/", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<base href="`+base+`/fabriq/" />`) {
		t.Fatalf("index: %d %q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `src="./assets/i.js"`) {
		t.Fatalf("a relative asset URL changed: %q", w.Body.String())
	}
	if got := fake.paths(); len(got) != 1 || got[0] != "/" {
		t.Fatalf("index reached the console as %v", got)
	}

	w = do(base+"/fabriq/config.js", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"basePath":"`+base+`/fabriq/"`) || !strings.Contains(w.Body.String(), `"apiUrl":"`+base+`/fabriq/admin"`) {
		t.Fatalf("config.js: %d %q", w.Code, w.Body.String())
	}
	if got := fake.last().URL.Path; got != "/config.js" {
		t.Fatalf("config.js reached the console as %q", got)
	}

	w = do(base+"/fabriq/assets/i.js", nil)
	if w.Code != http.StatusOK || fake.last().URL.Path != "/assets/i.js" {
		t.Fatalf("asset: %d reached as %q", w.Code, fake.last().URL.Path)
	}

	// The historical recipe still works, and the proxy strips spoofed
	// control headers and upstream echoes.
	w = do(base+"/fabriq/admin/meta", http.Header{
		"Authorization": {"Bearer lp_browser_key"}, "Cookie": {"lp_session=abc"}, "X-Tenant-Id": {"acme"},
		fixtureControlHeader: {"spoofed", "also spoofed"},
	})
	if w.Code != http.StatusOK || w.Body.String() != `{"ok":true}` {
		t.Fatalf("meta: %d %q", w.Code, w.Body.String())
	}
	r := fake.last()
	if len(r.Header.Values(fixtureControlHeader)) != 0 || len(w.Header().Values(fixtureControlHeader)) != 0 {
		t.Fatal("historical console received a spoofed token or sent an echo to the browser")
	}
	if r.URL.Path != "/admin/meta" {
		t.Fatalf("meta reached the console as %q", r.URL.Path)
	}
	if v := r.Header.Values("Authorization"); len(v) != 0 {
		t.Fatalf("the browser's Authorization reached the console: %q", v)
	}
	if v := r.Header.Values("Cookie"); len(v) != 0 {
		t.Fatalf("the browser's cookie reached the console: %q", v)
	}
	if r.Header.Get("X-Tenant-Id") != "acme" {
		t.Fatal("the tenant header was dropped")
	}

	// Every channel went to the console's own HTTP port.
	dialMu.Lock()
	opened := slices.Clone(dials)
	dialMu.Unlock()
	if len(opened) == 0 {
		t.Fatal("no channel was opened")
	}
	for _, d := range opened {
		if d.binding != "fabriq--admin" || d.port != 8080 {
			t.Fatalf("a channel opened for %s port %d, want fabriq--admin port 8080", d.binding, d.port)
		}
	}

	// Nothing outside the console is reachable, and nothing climbs out of it.
	reached := len(fake.paths())
	for _, target := range []string{base + "/metrics", base + "/fabriq/../admin/keys", base + "/fabriq/%2e%2e/admin/keys", base + "/fabriqx/"} {
		if w := do(target, nil); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", target, w.Code)
		}
	}
	if got := fake.paths(); len(got) != reached {
		t.Fatalf("a refused path reached the console: %v", got[reached:])
	}
}

func TestFabriqAdminProxyControlCredential(t *testing.T) {
	for _, tc := range []struct {
		name               string
		value              []byte
		storeErr           error
		declared, nilStore bool
		status             int
	}{
		{name: "valid", value: []byte(fixtureControlToken), declared: true, status: http.StatusOK},
		{name: "missing", declared: true, status: http.StatusBadGateway},
		{name: "empty", value: []byte{}, declared: true, status: http.StatusBadGateway},
		{name: "malformed", value: []byte(strings.ToUpper(fixtureControlToken)), declared: true, status: http.StatusBadGateway},
		{name: "unknown revision", status: http.StatusBadGateway},
		{name: "legacy without store", nilStore: true, status: http.StatusBadGateway},
		{name: "vault error", storeErr: errors.New("read failed for " + fixtureControlToken), declared: true, status: http.StatusBadGateway},
		{name: "vault impersonates declaration", storeErr: &deployments.GeneratedSecretNotDeclaredError{Kind: "fabriq", Version: "1.9", Revision: "1", Name: fixtureControlSecret}, declared: true, status: http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bindings := fabriqBindings(t)
			parent := bindings[len(bindings)-1]
			var config planning.ResourceConfig
			if err := json.Unmarshal(parent.Config, &config); err != nil {
				t.Fatal(err)
			}
			if !tc.nilStore {
				config.Composition.Revision = "gated-fixture"
			}
			if tc.declared {
				config.Composition.Secrets = append(config.Composition.Secrets, recipes.GeneratedSecret{Name: fixtureControlSecret, Bytes: 32, Encoding: "hex"})
			}
			var err error
			parent.Config, err = json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			var store deployments.ResourceSecrets
			if !tc.nilStore {
				store = controlFixtureSecrets{value: tc.value, err: tc.storeErr}
			}
			list := func(context.Context, string, string) ([]*planning.Binding, error) { return bindings, nil }
			fake := &fakeAdmin{}
			var dials []dial
			var dialMu sync.Mutex
			authz := allowAll{}
			access := deployments.NewCatalogAccess(deployments.CatalogAccessDeps{
				Authz: authz, Bindings: list, Secrets: store,
				Executors: map[string]deployments.Executor{"server": dialer{rt: fake, mu: &dialMu, dials: &dials}},
			})
			var warnings []string
			svc := catalogproxy.NewService(catalogproxy.Deps{
				Store: &memStore{rows: map[string]catalogproxy.Record{}}, Access: access, Authz: authz, Bindings: list,
				Warn: func(msg string, err error) { warnings = append(warnings, msg+": "+err.Error()) },
			})
			session, err := svc.Open(t.Context(), as("admin"), fabriqRef)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, session.URL+"admin/meta", nil)
			r.Header.Add(fixtureControlHeader, "forged")
			r.Header.Add(fixtureControlHeader, "forged-again")
			r.Header.Set("Authorization", "Bearer browser")
			w := httptest.NewRecorder()
			svc.Handler().ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d", w.Code, tc.status)
			}
			if strings.Contains(session.URL, fixtureControlToken) || strings.Contains(w.Body.String(), fixtureControlToken) ||
				strings.Contains(strings.Join(warnings, "\n"), fixtureControlToken) || len(w.Header().Values(fixtureControlHeader)) != 0 {
				t.Fatal("proxy exposed the control credential")
			}
			if tc.status != http.StatusOK {
				if len(dials) != 0 || len(fake.paths()) != 0 {
					t.Fatal("bad credential opened a channel or reached the console")
				}
				return
			}
			got := fake.last().Header
			values := got.Values(fixtureControlHeader)
			if len(values) != 1 || values[0] != fixtureControlToken || len(got.Values("Authorization")) != 0 {
				t.Fatal("proxy did not replace spoofed credentials with the resource token")
			}
		})
	}
}

type controlFixtureSecrets struct {
	deployments.ResourceSecrets
	value []byte
	err   error
}

func (s controlFixtureSecrets) Get(_ context.Context, key string) ([]byte, error) {
	if key != deployments.ResourceKey("org", "env", "fabriq", "secret:"+fixtureControlSecret) {
		return nil, fmt.Errorf("unexpected credential scope: %s", key)
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.value == nil {
		return nil, deployments.ErrNoCredential
	}
	return slices.Clone(s.value), nil
}

func TestFabriqControlCredentialRejectsUnknownIdentities(t *testing.T) {
	historical := deployments.GeneratedSecretNotDeclaredError{Kind: "fabriq", Version: "1.9", Revision: "1", Name: fixtureControlSecret}
	wrongKind := historical
	wrongKind.Kind = "fabriq-library"
	wrongVersion := historical
	wrongVersion.Version = "1.10"
	wrongName := historical
	wrongName.Name = "admin-key"
	for name, err := range map[string]error{
		"wrong kind":          &wrongKind,
		"wrong version":       &wrongVersion,
		"wrong secret":        &wrongName,
		"missing parent":      deployments.ErrComponentNotFound,
		"wrapped declaration": fmt.Errorf("binding source failed: %w", &historical),
	} {
		t.Run(name, func(t *testing.T) {
			header, err := fabriqloom.AdminTarget.Header(t.Context(), func(string) ([]byte, error) {
				return nil, err
			})
			if err == nil || header != nil {
				t.Fatal("an unknown credential source selected legacy routing")
			}
		})
	}
	if header, err := fabriqloom.AdminTarget.Header(t.Context(), nil); err == nil || header != nil {
		t.Fatal("missing credential reader selected legacy routing")
	}
}
