package repositories

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/scope"
	"github.com/xraph/vault/secret"
	vaultmem "github.com/xraph/vault/store/memory"

	"github.com/xraph/loomplane/internal/security"
)

// memPlatformStore counts its reads and can be made to fail.
type memPlatformStore struct {
	mu    sync.Mutex
	apps  map[string]PlatformApp
	reads int
	fail  error
}

func (m *memPlatformStore) List(context.Context) (map[string]PlatformApp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	if m.fail != nil {
		return nil, m.fail
	}
	return maps.Clone(m.apps), nil
}

func (m *memPlatformStore) Put(_ context.Context, key string, a PlatformApp) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.apps[key] = a
	return nil
}

func (m *memPlatformStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.apps, key)
	return nil
}

func newTestPlatformApps() (*PlatformApps, *memPlatformStore, *time.Time) {
	store := &memPlatformStore{apps: map[string]PlatformApp{}}
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	p := NewPlatformApps(store)
	p.now = func() time.Time { return now }
	return p, store, &now
}

func TestPlatformAppsCache(t *testing.T) {
	p, store, now := newTestPlatformApps()
	ctx := t.Context()
	store.apps[PlatformGitLab] = PlatformApp{ClientID: "gl", ClientSecret: "s"}
	if a, ok := p.Get(ctx, PlatformGitLab); !ok || a.ClientID != "gl" {
		t.Fatalf("first read %v %v", a, ok)
	}
	// Another process saves; this one sees it only after the TTL.
	store.apps[PlatformGitLab] = PlatformApp{ClientID: "gl2", ClientSecret: "s"}
	if a, _ := p.Get(ctx, PlatformGitLab); a.ClientID != "gl" || store.reads != 1 {
		t.Fatalf("within the TTL %v, %d reads", a, store.reads)
	}
	*now = now.Add(platformAppsTTL)
	if a, _ := p.Get(ctx, PlatformGitLab); a.ClientID != "gl2" {
		t.Fatalf("after the TTL %v", a)
	}
	// A failed read keeps what was cached, and waits the TTL again.
	store.fail = errors.New("db down")
	*now = now.Add(platformAppsTTL)
	if a, ok := p.Get(ctx, PlatformGitLab); !ok || a.ClientID != "gl2" {
		t.Fatalf("through an outage %v %v", a, ok)
	}
	reads := store.reads
	p.Get(ctx, PlatformGitLab)
	if store.reads != reads {
		t.Fatal("retried the store before the TTL")
	}
	store.fail = nil
	// A save in this process is live in it at once; so is a removal.
	if err := p.Save(ctx, PlatformBitbucket, PlatformApp{ClientID: "bb", ClientSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Get(ctx, PlatformBitbucket); !ok {
		t.Fatal("a save isn't live")
	}
	if err := p.Remove(ctx, PlatformBitbucket); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Get(ctx, PlatformBitbucket); ok {
		t.Fatal("a removal isn't live")
	}
}

func TestNilPlatformAppsHaveNone(t *testing.T) {
	var p *PlatformApps
	if _, ok := p.Get(t.Context(), PlatformGitHub); ok {
		t.Fatal("a nil PlatformApps has an app")
	}
	if err := p.Save(t.Context(), PlatformGitHub, PlatformApp{}); !errors.Is(err, ErrNoPlatformApps) {
		t.Fatalf("save: %v", err)
	}
	if p.GitHubAppSource("", "")(t.Context()) != nil || p.BitbucketSource("", "")(t.Context()) != nil || p.GitLabSource("")(t.Context()) != nil {
		t.Fatal("a nil PlatformApps resolves an app")
	}
}

func TestPlatformAppKeepsSecretsOutOfPrints(t *testing.T) {
	a := PlatformApp{ClientID: "id", ClientSecret: "cs-value", PrivateKey: "pk-value", WebhookSecret: "wh-value"}
	for _, s := range []string{a.String(), a.GoString()} {
		for _, secret := range []string{"cs-value", "pk-value", "wh-value"} {
			if strings.Contains(s, secret) {
				t.Fatalf("%q prints %s", s, secret)
			}
		}
	}
}

func rsaPEM(t *testing.T) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
}

func TestGitHubAppSource(t *testing.T) {
	p, store, now := newTestPlatformApps()
	var logged []string
	p.SetLogf(func(format string, _ ...any) { logged = append(logged, format) })
	src := p.GitHubAppSource("https://api.github.example", "https://github.example")
	ctx := t.Context()
	if src(ctx) != nil {
		t.Fatal("an App with nothing saved")
	}
	at := *now
	store.apps[PlatformGitHub] = PlatformApp{ClientID: "Iv1", ClientSecret: "s", AppID: 9, Slug: "lp", PrivateKey: "not a key", UpdatedAt: at}
	if _, err := p.Fresh(ctx); err != nil {
		t.Fatal(err)
	}
	if bad, again := src(ctx), src(ctx); bad != nil || again != nil || len(logged) != 1 {
		t.Fatalf("a key that doesn't parse: logged %d times", len(logged))
	}
	store.apps[PlatformGitHub] = PlatformApp{ClientID: "Iv1", ClientSecret: "s", AppID: 9, Slug: "lp", PrivateKey: rsaPEM(t), UpdatedAt: at.Add(time.Second)}
	if _, err := p.Fresh(ctx); err != nil {
		t.Fatal(err)
	}
	first := src(ctx)
	app, ok := first.(*App)
	if !ok || app.id != 9 || app.InstallURL("st") != "https://github.example/apps/lp/installations/new?state=st" {
		t.Fatalf("built %#v", first)
	}
	if src(ctx) != first {
		t.Fatal("the App was built again for the same save")
	}
}

func TestBitbucketSourceAndConsumerChange(t *testing.T) {
	p, _, _ := newTestPlatformApps()
	ctx := t.Context()
	src := p.BitbucketSource("", "")
	if src(ctx) != nil {
		t.Fatal("a consumer with nothing saved")
	}
	if err := p.Save(ctx, PlatformBitbucket, PlatformApp{ClientID: "bb", ClientSecret: "s", UpdatedAt: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	first := src(ctx)
	if first == nil || src(ctx) != first {
		t.Fatal("the consumer isn't kept between calls")
	}

	f := newBBFixture(t)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	conns := f.newConns()
	var current BitbucketAPI
	conns.UseConsumer(func(context.Context) BitbucketAPI { return current })
	if _, err := conns.Authorize(ctx, admin, "acme"); !errors.Is(err, ErrBitbucketNotConfigured) {
		t.Fatalf("no consumer: %v", err)
	}
	current = f.api
	if _, err := conns.Authorize(ctx, admin, "acme"); err != nil {
		t.Fatalf("a consumer saved since: %v", err)
	}
	conns.consumerRejectedNow()
	if !conns.consumerWasRejected() {
		t.Fatal("the rejection wasn't kept")
	}
	// A different consumer starts with a clean slate.
	current = newFakeBBAPI(f.clock)
	conns.consumer(ctx)
	if conns.consumerWasRejected() {
		t.Fatal("a new consumer inherited the old one's rejection")
	}
}

// ghStore and ghApp are the least of a Store and a GitHubApp that Token
// needs.
type ghStore struct {
	Store
	recs []*InstallationRecord
}

func (s ghStore) ListInstallations(context.Context, string) ([]*InstallationRecord, error) {
	return s.recs, nil
}

type ghApp struct{ GitHubApp }

func (ghApp) RepositoryToken(context.Context, int64, string) (string, error) { return "tok", nil }

func TestConnectionsUseTheAppOfTheMoment(t *testing.T) {
	store := ghStore{recs: []*InstallationRecord{{OrgID: "o", ID: 1, Account: "acme"}}}
	c := NewConnections(store, nil, nil, nil, nil)
	var current GitHubApp
	c.UseApp(func(context.Context) GitHubApp { return current })
	repo := Repository{Host: "github.com", Owner: "acme", Name: "api"}
	if tok, err := c.Token(t.Context(), "o", repo); err != nil || tok != "" {
		t.Fatalf("no App: %q %v", tok, err)
	}
	current = ghApp{}
	if tok, err := c.Token(t.Context(), "o", repo); err != nil || tok != "tok" {
		t.Fatalf("an App saved since: %q %v", tok, err)
	}
}

func TestGitLabRegistryManagedHosts(t *testing.T) {
	p, _, _ := newTestPlatformApps()
	ctx := t.Context()
	reg := GitLabRegistry{Managed: p.GitLabSource("https://gitlab.example")}
	if _, ok, _ := reg.Host(ctx, "o", HostGitLab); ok {
		t.Fatal("gitlab.com is an operator host with no application")
	}
	if err := p.Save(ctx, PlatformGitLab, PlatformApp{ClientID: "gl", ClientSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	h, ok, err := reg.Host(ctx, "o", HostGitLab)
	if err != nil || !ok || !h.Instance || h.ClientID != "gl" || h.WebURL != "https://gitlab.example" {
		t.Fatalf("managed gitlab.com %+v %v %v", h, ok, err)
	}
	if secret, ok := reg.managedSecret(ctx, HostGitLab); !ok || secret != "s" {
		t.Fatalf("managed secret %q %v", secret, ok)
	}
	if _, ok := reg.Reader(ctx, HostGitLab); !ok {
		t.Fatal("no reader for managed gitlab.com")
	}
	// Configuration's gitlab.com wins over the saved one.
	reg.Instance = []GitLabHost{{WebURL: "https://gitlab.com", Host: HostGitLab, ClientID: "cfg"}}
	if h, _, _ := reg.Host(ctx, "o", HostGitLab); h.ClientID != "cfg" {
		t.Fatalf("config lost to the console: %+v", h)
	}
	if _, ok := reg.managedSecret(ctx, HostGitLab); ok {
		t.Fatal("the saved secret was offered for configuration's host")
	}
	if n := len(reg.instances(ctx)); n != 1 {
		t.Fatalf("%d operator hosts, want 1", n)
	}
}

func TestCheckClient(t *testing.T) {
	var form, basic string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, secret, ok := r.BasicAuth()
		if ok {
			basic = id
		} else {
			id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
		}
		form = r.Form.Encode()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case id == "mismatch":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"redirect_uri_mismatch"}`))
		case id == "odd":
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte(`{"error":"whatever"}`))
		case id == "good" && secret == "s":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"bad ` + secret + `"}`))
		}
	}))
	defer srv.Close()
	probe := func(id, secret string, basicAuth bool) ClientCheck {
		return CheckClient(t.Context(), ClientProbe{
			Host: "Host", TokenURL: srv.URL + "/token", RedirectURL: "https://lp.example/cb",
			ClientID: id, ClientSecret: secret, BasicAuth: basicAuth,
		})
	}
	if r := probe("good", "s", false); r.Result != ClientAccepted || !strings.Contains(form, "redirect_uri=https") || !strings.Contains(form, "client_id=good") {
		t.Fatalf("good: %+v, form %s", r, form)
	}
	if r := probe("good", "s", true); r.Result != ClientAccepted || basic != "good" || strings.Contains(form, "client_secret") {
		t.Fatalf("basic: %+v, form %s", r, form)
	}
	if r := probe("good", "hunter2", false); r.Result != ClientRejected || strings.Contains(r.Detail, "hunter2") || !strings.Contains(r.Detail, "invalid_client") {
		t.Fatalf("rejected: %+v", r)
	}
	if r := probe("mismatch", "s", false); r.Result != ClientRedirectMismatch {
		t.Fatalf("mismatch: %+v", r)
	}
	if r := probe("odd", "s", false); r.Result != ClientInconclusive {
		t.Fatalf("odd: %+v", r)
	}
	srv.Close()
	if r := probe("good", "s", false); r.Result != ClientUnreachable {
		t.Fatalf("unreachable: %+v", r)
	}
}

func TestAppIdentify(t *testing.T) {
	key := rsaPEM(t)
	accept := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || !accept {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"slug":"lp"}`))
	}))
	defer srv.Close()
	app, err := NewApp(AppConfig{ID: 1, Slug: "lp", PrivateKey: key, APIURL: srv.URL, WebURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if slug, err := app.Identify(t.Context()); err != nil || slug != "lp" {
		t.Fatalf("identify: %q %v", slug, err)
	}
	accept = false
	if _, err := app.Identify(t.Context()); !errors.Is(err, ErrAppKeyRejected) {
		t.Fatalf("a key GitHub refuses: %v", err)
	}
}

func TestVaultPlatformApps(t *testing.T) {
	enc, err := crypto.NewEncryptor([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	store := NewVaultPlatformApps(secret.NewService(vaultmem.New(), enc, secret.WithAppID(PlatformAppsVaultAppID)))
	// A request scoped to an organization still saves at platform level,
	// where a process with no tenant reads it.
	scoped := scope.WithTenantID(t.Context(), "org_a")
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	in := PlatformApp{ClientID: "Iv1", ClientSecret: "cs", AppID: 3, Slug: "lp", PrivateKey: "pk", WebhookSecret: "wh", UpdatedBy: "u", UpdatedAt: at}
	if err = store.Put(scoped, PlatformGitHub, in); err != nil {
		t.Fatal(err)
	}
	got, err := store.List(t.Context())
	if err != nil || got[PlatformGitHub] != in {
		t.Fatalf("listed %v %v", got, err)
	}
	if err := store.Delete(scoped, PlatformGitHub); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(t.Context(), PlatformGitHub); err != nil {
		t.Fatalf("delete twice: %v", err)
	}
	if got, _ := store.List(t.Context()); len(got) != 0 {
		t.Fatalf("after delete %v", got)
	}
}
