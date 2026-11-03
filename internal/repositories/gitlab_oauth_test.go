package repositories_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
	"github.com/xraph/loomplane/internal/repositories"
)

var gitlabApp = oauth.App{ClientID: "app-id", ClientSecret: "app-secret"}

// fakeGitLabOAuth serves GitLab's token, revoke, user and version
// endpoints, and keeps the forms it was sent.
type fakeGitLabOAuth struct {
	mu     sync.Mutex
	forms  []url.Values
	status int
	body   string
}

func (f *fakeGitLabOAuth) serve(t *testing.T) *repositories.GitLab {
	t.Helper()
	mux := http.NewServeMux()
	handle := func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("%s Content-Type = %q", r.URL.Path, ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		f.mu.Lock()
		f.forms = append(f.forms, r.PostForm)
		status, body := f.status, f.body
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
	mux.HandleFunc("POST /oauth/token", handle)
	mux.HandleFunc("POST /oauth/revoke", handle)
	mux.HandleFunc("GET /api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "{}", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"id":42,"username":"rex","avatar_url":"https://gitlab.example/a.png","email":"x@y.z"}`))
	})
	mux.HandleFunc("GET /api/v4/version", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"17.4.2-ee","revision":"abc"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return repositories.NewGitLab(srv.URL, nil)
}

func (f *fakeGitLabOAuth) last(t *testing.T) url.Values {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.forms) == 0 {
		t.Fatal("the fake was never called")
	}
	return f.forms[len(f.forms)-1]
}

func TestGitLab_AuthorizeURLAsksReadAPIOnly(t *testing.T) {
	g := repositories.NewGitLab("https://gitlab.example.com/", nil)
	raw := g.AuthorizeURL("app-id", "https://loom.example/cb", "st8", "chal")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme+"://"+u.Host+u.Path != "https://gitlab.example.com/oauth/authorize" {
		t.Fatalf("authorize URL = %s", raw)
	}
	q := u.Query()
	want := map[string]string{
		"client_id":             "app-id",
		"redirect_uri":          "https://loom.example/cb",
		"response_type":         "code",
		"state":                 "st8",
		"scope":                 "read_api",
		"code_challenge":        "chal",
		"code_challenge_method": "S256",
	}
	for k, v := range want {
		if got := q.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if len(q) != len(want) {
		t.Errorf("query has %d keys, want %d: %s", len(q), len(want), u.RawQuery)
	}
}

func TestGitLab_ExchangeSendsVerifier(t *testing.T) {
	f := &fakeGitLabOAuth{status: 200, body: `{"access_token":"at","refresh_token":"rt","expires_in":3600,"token_type":"Bearer"}`}
	g := f.serve(t)
	before := time.Now()
	grant, err := g.Exchange(context.Background(), gitlabApp, "https://loom.example/cb", "the-code", "the-verifier")
	if err != nil {
		t.Fatal(err)
	}
	form := f.last(t)
	for k, v := range map[string]string{
		"grant_type":    "authorization_code",
		"code":          "the-code",
		"code_verifier": "the-verifier",
		"redirect_uri":  "https://loom.example/cb",
		"client_id":     "app-id",
		"client_secret": "app-secret",
	} {
		if form.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, form.Get(k), v)
		}
	}
	if grant.AccessToken != "at" || grant.RefreshToken != "rt" {
		t.Fatalf("grant = %+v", grant)
	}
	if d := grant.Expiry.Sub(before); d < 59*time.Minute || d > 61*time.Minute {
		t.Fatalf("expiry is %s from now, want about 1h", d)
	}
}

func TestGitLab_ExchangeDefaultsToTwoHours(t *testing.T) {
	f := &fakeGitLabOAuth{status: 200, body: `{"access_token":"at","refresh_token":"rt"}`}
	grant, err := f.serve(t).Exchange(context.Background(), gitlabApp, "r", "c", "v")
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(grant.Expiry); d < 119*time.Minute || d > 121*time.Minute {
		t.Fatalf("expiry is %s from now, want about 2h", d)
	}
}

func TestGitLab_ExchangeNeedsAnAccessToken(t *testing.T) {
	f := &fakeGitLabOAuth{status: 200, body: `{"refresh_token":"rt"}`}
	if _, err := f.serve(t).Exchange(context.Background(), gitlabApp, "r", "c", "v"); err == nil {
		t.Fatal("a token response without an access token was accepted")
	}
}

func TestGitLab_RefreshSendsTheRefreshToken(t *testing.T) {
	f := &fakeGitLabOAuth{status: 200, body: `{"access_token":"at2","refresh_token":"rt2","expires_in":7200}`}
	g := f.serve(t)
	grant, err := g.Refresh(context.Background(), gitlabApp, oauth.Grant{AccessToken: "at", RefreshToken: "rt"})
	if err != nil {
		t.Fatal(err)
	}
	form := f.last(t)
	for k, v := range map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": "rt",
		"client_id":     "app-id",
		"client_secret": "app-secret",
	} {
		if form.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, form.Get(k), v)
		}
	}
	if grant.AccessToken != "at2" || grant.RefreshToken != "rt2" {
		t.Fatalf("grant = %+v", grant)
	}
}

func TestGitLab_RefreshRefused(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"spent token", 400, `{"error":"invalid_grant","error_description":"The provided authorization grant is invalid"}`, oauth.ErrRefused},
		{"spent token as 401", 401, `{"error":"invalid_grant"}`, oauth.ErrRefused},
		{"rotated secret", 401, `{"error":"invalid_client"}`, oauth.ErrAppRejected},
		{"rotated secret as 400", 400, `{"error":"invalid_client"}`, oauth.ErrAppRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeGitLabOAuth{status: tt.status, body: tt.body}
			_, err := f.serve(t).Refresh(context.Background(), gitlabApp, oauth.Grant{RefreshToken: "rt"})
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestGitLab_RefreshOutageIsNeitherRefusal(t *testing.T) {
	f := &fakeGitLabOAuth{status: 503, body: `{"error":"temporarily_unavailable"}`}
	_, err := f.serve(t).Refresh(context.Background(), gitlabApp, oauth.Grant{RefreshToken: "rt"})
	if err == nil || errors.Is(err, oauth.ErrRefused) || errors.Is(err, oauth.ErrAppRejected) {
		t.Fatalf("err = %v, want a plain failure", err)
	}
}

func TestGitLab_RefreshErrorNeverQuotesTokens(t *testing.T) {
	const secret = "rt-very-secret-0123"
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"echoed in a 500", 500, `{"error":"server_error","error_description":"bad refresh_token ` + secret + `"}`},
		{"error field holds it", 500, `{"error":"` + secret + `"}`},
		{"invalid_grant with a description", 400, `{"error":"invalid_grant","error_description":"` + secret + `"}`},
		{"not json", 502, "upstream said " + secret},
		{"success body that doesn't parse", 200, `{"access_token":"` + secret + `",`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeGitLabOAuth{status: tt.status, body: tt.body}
			_, err := f.serve(t).Refresh(context.Background(), gitlabApp, oauth.Grant{RefreshToken: secret})
			if err == nil {
				t.Fatal("want an error")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "app-secret") {
				t.Fatalf("error quotes a secret: %v", err)
			}
		})
	}
}

func TestGitLab_ExchangeErrorNeverQuotesCodes(t *testing.T) {
	f := &fakeGitLabOAuth{status: 500, body: `{"error":"server_error","error_description":"code the-code verifier the-verifier"}`}
	_, err := f.serve(t).Exchange(context.Background(), gitlabApp, "r", "the-code", "the-verifier")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, s := range []string{"the-code", "the-verifier", "app-secret"} {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error quotes %q: %v", s, err)
		}
	}
}

func TestGitLab_ExchangeSpentCodeIsRefused(t *testing.T) {
	f := &fakeGitLabOAuth{status: 400, body: `{"error":"invalid_grant"}`}
	_, err := f.serve(t).Exchange(context.Background(), gitlabApp, "r", "c", "v")
	if !errors.Is(err, oauth.ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
}

func TestGitLab_RevokeSendsTheToken(t *testing.T) {
	f := &fakeGitLabOAuth{status: 200, body: `{}`}
	if err := f.serve(t).Revoke(context.Background(), gitlabApp, "the-token"); err != nil {
		t.Fatal(err)
	}
	form := f.last(t)
	for k, v := range map[string]string{"token": "the-token", "client_id": "app-id", "client_secret": "app-secret"} {
		if form.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, form.Get(k), v)
		}
	}
}

func TestGitLab_RevokeFailureNeverQuotesTheToken(t *testing.T) {
	f := &fakeGitLabOAuth{status: 500, body: `{"error":"server_error","error_description":"the-token"}`}
	err := f.serve(t).Revoke(context.Background(), gitlabApp, "the-token")
	if err == nil || strings.Contains(err.Error(), "the-token") {
		t.Fatalf("err = %v", err)
	}
}

func TestGitLab_UserReadsTheSignedInAccount(t *testing.T) {
	g := (&fakeGitLabOAuth{}).serve(t)
	u, err := g.User(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if u != (repositories.GitLabUser{ID: 42, Username: "rex", AvatarURL: "https://gitlab.example/a.png"}) {
		t.Fatalf("user = %+v", u)
	}
	if _, err := g.User(context.Background(), "wrong"); !errors.Is(err, repositories.ErrNotFound) {
		t.Fatalf("a rejected token = %v, want ErrNotFound", err)
	}
}

func TestGitLab_VersionReadsTheInstance(t *testing.T) {
	v, err := (&fakeGitLabOAuth{}).serve(t).Version(context.Background(), "tok")
	if err != nil || v != "17.4.2-ee" {
		t.Fatalf("version = %q, %v", v, err)
	}
}

func TestGitLabVersionSupported(t *testing.T) {
	tests := map[string]bool{
		"17.0.0":                 true,
		"17.0":                   true,
		"17.4.2-ee":              true,
		"19.5.1-ee":              true,
		"100.0.0":                true,
		"16.11.3":                false,
		"16.11.3-ee":             false,
		"9.9.9":                  false,
		"":                       false,
		"latest":                 false,
		"v17.0.0":                false,
		"17":                     true,
		"-17.0.0":                false,
		".17.0":                  false,
		"17abc.0.0":              false,
		"99999999999999999999.0": false,
	}
	for v, want := range tests {
		if got := repositories.GitLabVersionSupported(v); got != want {
			t.Errorf("GitLabVersionSupported(%q) = %v, want %v", v, got, want)
		}
	}
}
