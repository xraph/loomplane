package digitalocean

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
)

var testApp = oauth.App{ClientID: "cid", ClientSecret: "csecret"}

const tokenReply = `{"access_token":"dop_v1_new","refresh_token":"dor_v1_new","expires_in":2592000,
"info":{"name":"Ada","email":"ada@example.com","uuid":"acct-1","team":{"uuid":"team-1","name":"Acme"}}}`

// tokenServer answers /v1/oauth/token with status and body, and hands the
// posted form to got.
func tokenServer(t *testing.T, status int, body string, got *url.Values) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/oauth/token" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("content type %q", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		*got = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func TestAuthorizeURLAsksForReadWrite(t *testing.T) {
	c := New("")
	u, err := url.Parse(c.AuthorizeURL(testApp, oauth.AuthorizeRequest{Redirect: "https://lp.example/cb", State: "st8"}))
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme+"://"+u.Host+u.Path != "https://cloud.digitalocean.com/v1/oauth/authorize" {
		t.Errorf("url %s", u)
	}
	q := u.Query()
	for k, want := range map[string]string{"client_id": "cid", "response_type": "code", "scope": "read write",
		"state": "st8", "redirect_uri": "https://lp.example/cb"} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
}

func TestAuthorizeURLUsesTheTestEndpoint(t *testing.T) {
	if got := New("http://127.0.0.1:1/").AuthorizeURL(testApp, oauth.AuthorizeRequest{Redirect: "r", State: "s"}); !strings.HasPrefix(got, "http://127.0.0.1:1/v1/oauth/authorize?") {
		t.Errorf("url %s", got)
	}
}

func TestExchangeReadsTheGrantAndItsTeam(t *testing.T) {
	var form url.Values
	c := tokenServer(t, 200, tokenReply, &form)
	before := time.Now()
	g, err := c.Exchange(context.Background(), testApp, oauth.CodeRequest{Redirect: "https://lp.example/cb", Code: "the-code"})
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"grant_type": "authorization_code", "code": "the-code", "client_id": "cid",
		"client_secret": "csecret", "redirect_uri": "https://lp.example/cb"} {
		if form.Get(k) != want {
			t.Errorf("form %s = %q, want %q", k, form.Get(k), want)
		}
	}
	if g.AccessToken != "dop_v1_new" || g.RefreshToken != "dor_v1_new" {
		t.Errorf("tokens %+v", g)
	}
	if d := g.Expiry.Sub(before); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Errorf("expiry %s from now", d)
	}
	want := map[string]string{"account_uuid": "acct-1", "team_uuid": "team-1", "team_name": "Acme", "email": "ada@example.com"}
	for k, v := range want {
		if g.Account[k] != v {
			t.Errorf("account %s = %q, want %q", k, g.Account[k], v)
		}
	}
}

func TestRefreshSpendsTheRefreshToken(t *testing.T) {
	var form url.Values
	c := tokenServer(t, 200, tokenReply, &form)
	g, err := c.Refresh(context.Background(), testApp, oauth.Grant{AccessToken: "dop_v1_old", RefreshToken: "dor_v1_old"})
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"grant_type": "refresh_token", "refresh_token": "dor_v1_old",
		"client_id": "cid", "client_secret": "csecret"} {
		if form.Get(k) != want {
			t.Errorf("form %s = %q, want %q", k, form.Get(k), want)
		}
	}
	if form.Has("code") || form.Has("redirect_uri") {
		t.Errorf("refresh sent %v", form)
	}
	if g.AccessToken != "dop_v1_new" || g.RefreshToken != "dor_v1_new" {
		t.Errorf("tokens %+v", g)
	}
}

func TestRefreshRefusedIsErrRefused(t *testing.T) {
	var form url.Values
	c := tokenServer(t, 400, `{"error":"invalid_grant","error_description":"expired"}`, &form)
	if _, err := c.Refresh(context.Background(), testApp, oauth.Grant{AccessToken: "dop_v1_old", RefreshToken: "dor_v1_old"}); !errors.Is(err, oauth.ErrRefused) {
		t.Errorf("400: %v", err)
	}
	c = tokenServer(t, 500, `{"error":"server_error"}`, &form)
	_, err := c.Refresh(context.Background(), testApp, oauth.Grant{AccessToken: "dop_v1_old", RefreshToken: "dor_v1_old"})
	if err == nil || errors.Is(err, oauth.ErrRefused) {
		t.Errorf("500: %v", err)
	}
	if err != nil && (strings.Contains(err.Error(), "csecret") || strings.Contains(err.Error(), "dor_v1_old")) {
		t.Errorf("error leaks a secret: %v", err)
	}
}

// A rotated or deleted client secret is the app's fault, not the grant's:
// signing in again with the same app wouldn't help.
func TestRefreshInvalidClientIsAppRejected(t *testing.T) {
	var form url.Values
	for _, tc := range []struct {
		status int
		body   string
	}{
		{401, `{"error":"invalid_client"}`},
		{400, `{"error":"unauthorized_client"}`},
	} {
		c := tokenServer(t, tc.status, tc.body, &form)
		_, err := c.Refresh(context.Background(), testApp, oauth.Grant{AccessToken: "dop_v1_old", RefreshToken: "dor_v1_old"})
		if !errors.Is(err, oauth.ErrAppRejected) || errors.Is(err, oauth.ErrRefused) {
			t.Errorf("%d %s: %v", tc.status, tc.body, err)
		}
		if err != nil && strings.Contains(err.Error(), "csecret") {
			t.Errorf("error leaks the client secret: %v", err)
		}
	}
}

func TestProbeAsksForTheAccount(t *testing.T) {
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		if auth != "Bearer dop_v1_live" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"id":"Unauthorized","message":"Unable to authenticate you"}`))
			return
		}
		_, _ = w.Write([]byte(`{"account":{"uuid":"acct-1","team":{"uuid":"team-1","name":"Acme"}}}`))
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL)
	if err := c.Probe(context.Background(), testApp, oauth.Grant{AccessToken: "dop_v1_live", RefreshToken: "dor_v1_live"}); err != nil {
		t.Fatal(err)
	}
	if path != "/v2/account" || auth != "Bearer dop_v1_live" {
		t.Fatalf("probe asked %s with %q", path, auth)
	}
	err := c.Probe(context.Background(), testApp, oauth.Grant{AccessToken: "dop_v1_revoked"})
	if !errors.Is(err, oauth.ErrRefused) || strings.Contains(err.Error(), "dop_v1_revoked") {
		t.Fatalf("revoked: %v", err)
	}
}
