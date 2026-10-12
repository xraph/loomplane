package repositories

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/warden"
	"github.com/xraph/warden/store/memory"

	"github.com/xraph/loomplane/internal/security"
)

const (
	testAppID     = 1234
	installed     = int64(42)
	someoneElse   = int64(43)
	goodCode      = "code-from-github"
	userToken     = "ghu_user"
	repoToken     = "ghs_repo"
	metadataToken = "ghs_metadata"
)

var (
	keyOnce sync.Once
	testKey *rsa.PrivateKey
)

func rsaKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		testKey = k
	})
	return testKey
}

func pkcs1PEM(t *testing.T) string {
	t.Helper()
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey(t))}))
}

func TestParsePrivateKey_repairsWhatEnvironmentVariablesDo(t *testing.T) {
	good := pkcs1PEM(t)
	der, err := x509.MarshalPKCS8PrivateKey(rsaKey(t))
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	tests := map[string]string{
		"pem":              good,
		"escaped newlines": strings.ReplaceAll(good, "\n", `\n`),
		"folded to spaces": strings.ReplaceAll(good, "\n", " "),
		"pkcs8":            pkcs8,
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			k, err := parsePrivateKey(in)
			if err != nil || !k.Equal(rsaKey(t)) {
				t.Fatalf("parsePrivateKey: %v", err)
			}
		})
	}
	if _, err := parsePrivateKey("not a key"); err == nil {
		t.Fatal("parsed garbage")
	}
}

// fakeGitHub plays the parts of GitHub an App connection touches. It
// checks the App's JWT with the key's public half.
func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	pub := &rsaKey(t).PublicKey
	appAuth := func(r *http.Request) bool {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		parts := strings.Split(tok, ".")
		if !ok || len(parts) != 3 {
			return false
		}
		sig, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			return false
		}
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig) != nil {
			return false
		}
		raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var claims struct {
			Iss string `json:"iss"`
			Iat int64  `json:"iat"`
			Exp int64  `json:"exp"`
		}
		if json.Unmarshal(raw, &claims) != nil || claims.Iss != strconv.Itoa(testAppID) || claims.Exp-claims.Iat > 600 {
			return false
		}
		return true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /app/installations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !appAuth(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": installed, "account": map[string]string{"login": "Acme", "type": "Organization"}})
	})
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("client_id") != "Iv1.client" || r.FormValue("client_secret") != "shh" || r.FormValue("code") != goodCode {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": userToken})
	})
	mux.HandleFunc("GET /user/installations", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+userToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"installations": []map[string]int64{{"id": installed}}})
	})
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		if !appAuth(r) || r.PathValue("id") != strconv.FormatInt(installed, 10) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Repositories []string          `json:"repositories"`
			Permissions  map[string]string `json:"permissions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case len(body.Repositories) == 1 && body.Repositories[0] == "mono" && body.Permissions["contents"] == "read" && len(body.Permissions) == 1:
			_ = json.NewEncoder(w).Encode(map[string]string{"token": repoToken})
		case len(body.Repositories) == 0 && body.Permissions["metadata"] == "read":
			_ = json.NewEncoder(w).Encode(map[string]string{"token": metadataToken})
		default:
			// The installation can't see that repository.
			w.WriteHeader(http.StatusUnprocessableEntity)
		}
	})
	mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+metadataToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"repositories": []map[string]any{
			{"full_name": "Acme/mono", "private": true, "default_branch": "main", "html_url": "https://github.com/Acme/mono"},
		}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestApp(t *testing.T, srv *httptest.Server) *App {
	t.Helper()
	app, err := NewApp(AppConfig{
		ID: testAppID, Slug: "loomplane-test", PrivateKey: pkcs1PEM(t), ClientID: "Iv1.client", ClientSecret: "shh",
		APIURL: srv.URL, WebURL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

type memInstallations struct {
	mu   sync.Mutex
	recs map[string]*InstallationRecord
}

func key(org string, id int64) string { return org + "/" + strconv.FormatInt(id, 10) }

func (m *memInstallations) SaveInstallation(_ context.Context, r *InstallationRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := *r
	m.recs[key(r.OrgID, r.ID)] = &c
	return nil
}

func (m *memInstallations) ListInstallations(_ context.Context, orgID string) ([]*InstallationRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*InstallationRecord
	for _, r := range m.recs {
		if r.OrgID == orgID {
			c := *r
			out = append(out, &c)
		}
	}
	return out, nil
}

func (m *memInstallations) DeleteInstallation(_ context.Context, orgID string, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.recs[key(orgID, id)]; !ok {
		return ErrInstallationNotFound
	}
	delete(m.recs, key(orgID, id))
	return nil
}

type connFixture struct {
	conns *Connections
	store *memInstallations
	authz *security.Authorizer
	key   []byte
}

func newConnFixture(t *testing.T, app GitHubApp) *connFixture {
	t.Helper()
	ws := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(ws))
	if err != nil {
		t.Fatal(err)
	}
	f := &connFixture{store: &memInstallations{recs: map[string]*InstallationRecord{}}, authz: security.NewAuthorizer(eng, ws, nil), key: []byte("0123456789abcdef0123456789abcdef")}
	f.conns = NewConnections(f.store, app, f.authz, nil, f.key)
	return f
}

func (f *connFixture) as(t *testing.T, org, user, role string) security.Principal {
	t.Helper()
	if err := f.authz.Assign(context.Background(), org, user, role, "test"); err != nil {
		t.Fatal(err)
	}
	return security.Principal{OrgID: org, UserID: user}
}

func TestState_signedAndShortLived(t *testing.T) {
	k := []byte("0123456789abcdef0123456789abcdef")
	now := time.Now()
	s, err := SignState(k, "aorg_a", "ausr_a", now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyState(k, s, now.Add(time.Minute))
	if err != nil || got.OrgID != "aorg_a" || got.UserID != "ausr_a" {
		t.Fatalf("VerifyState = %+v, %v", got, err)
	}
	payload, sig, _ := strings.Cut(s, ".")
	forged, _ := json.Marshal(InstallState{OrgID: "aorg_b", UserID: "ausr_a", Expires: now.Add(time.Hour).Unix()})
	for name, bad := range map[string]string{
		"tampered": base64.RawURLEncoding.EncodeToString(forged) + "." + sig,
		"other key": func() string {
			s2, _ := SignState([]byte("another key, 32 bytes long......"), "aorg_a", "ausr_a", now)
			return s2
		}(),
		"garbage": "nope",
		"no sig":  payload,
	} {
		if _, err := VerifyState(k, bad, now); !errors.Is(err, ErrBadState) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := VerifyState(k, s, now.Add(stateLifetime+time.Second)); !errors.Is(err, ErrBadState) {
		t.Fatalf("expired: %v", err)
	}
}

func TestConnections_connect(t *testing.T) {
	ctx := context.Background()
	srv := fakeGitHub(t)
	f := newConnFixture(t, newTestApp(t, srv))
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	dev := f.as(t, "aorg_a", "ausr_dev", security.RoleDeveloper)
	state := func(p security.Principal) string {
		s, err := SignState(f.key, p.OrgID, p.UserID, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	if _, err := f.conns.Connect(ctx, dev, ConnectGitHubInput{InstallationID: installed, Code: goodCode, State: state(dev)}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a developer connecting: %v", err)
	}
	refusals := map[string]ConnectGitHubInput{
		"bad code":                    {InstallationID: installed, Code: "stolen", State: state(admin)},
		"someone else's installation": {InstallationID: someoneElse, Code: goodCode, State: state(admin)},
		"state for another person":    {InstallationID: installed, Code: goodCode, State: state(dev)},
		"no state":                    {InstallationID: installed, Code: goodCode},
	}
	for name, in := range refusals {
		t.Run(name, func(t *testing.T) {
			_, err := f.conns.Connect(ctx, admin, in)
			if !errors.Is(err, ErrNotYourInstallation) && !errors.Is(err, ErrBadState) {
				t.Fatalf("Connect: %v", err)
			}
		})
	}
	if len(f.store.recs) != 0 {
		t.Fatalf("a refused connection was saved: %+v", f.store.recs)
	}

	inst, err := f.conns.Connect(ctx, admin, ConnectGitHubInput{InstallationID: installed, Code: goodCode, State: state(admin)})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if inst.ID != "42" || inst.Account != "Acme" || inst.AccountType != "Organization" {
		t.Fatalf("installation %+v", inst)
	}

	st, err := f.conns.Status(ctx, dev)
	if err != nil || !st.Configured || len(st.Installations) != 1 || st.InstallURL != "" {
		t.Fatalf("status for a developer: %+v, %v", st, err)
	}
	st, err = f.conns.Status(ctx, admin)
	if err != nil || !strings.HasPrefix(st.InstallURL, srv.URL+"/apps/loomplane-test/installations/new?state=") {
		t.Fatalf("status for an admin: %+v, %v", st, err)
	}

	repos, err := f.conns.Repositories(ctx, dev)
	if err != nil || len(repos) != 1 || repos[0].FullName != "Acme/mono" || repos[0].Installation != "42" {
		t.Fatalf("repositories: %+v, %v", repos, err)
	}

	if err := f.conns.Disconnect(ctx, admin, "42"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if err := f.conns.Disconnect(ctx, admin, "42"); !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("disconnect twice: %v", err)
	}
}

func TestConnections_tokensOnlyFromTheOrganizationsOwnInstallations(t *testing.T) {
	ctx := context.Background()
	f := newConnFixture(t, newTestApp(t, fakeGitHub(t)))
	if err := f.store.SaveInstallation(ctx, &InstallationRecord{OrgID: "aorg_a", ID: installed, Account: "Acme"}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, org string
		repo      Repository
		want      string
	}{
		{"owner matches, any case", "aorg_a", Repository{"acme", "mono"}, repoToken},
		{"the installation can't see the repository", "aorg_a", Repository{"acme", "secret"}, ""},
		{"another owner", "aorg_a", Repository{"globex", "mono"}, ""},
		{"another organization", "aorg_b", Repository{"acme", "mono"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := f.conns.Token(ctx, tt.org, tt.repo)
			if err != nil || got != tt.want {
				t.Fatalf("Token = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestConnections_withoutAnApp(t *testing.T) {
	ctx := context.Background()
	f := newConnFixture(t, nil)
	admin := f.as(t, "aorg_a", "ausr_admin", security.RoleAdmin)
	st, err := f.conns.Status(ctx, admin)
	if err != nil || st.Configured || st.InstallURL != "" {
		t.Fatalf("status %+v, %v", st, err)
	}
	if _, err := f.conns.Connect(ctx, admin, ConnectGitHubInput{InstallationID: installed}); !errors.Is(err, ErrAppNotConfigured) {
		t.Fatalf("connect: %v", err)
	}
	if tok, err := f.conns.Token(ctx, "aorg_a", Repository{"acme", "mono"}); err != nil || tok != "" {
		t.Fatalf("token %q, %v", tok, err)
	}
}
