package vercel

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

var testApp = oauth.App{ClientID: "cid", ClientSecret: "csecret", Slug: "loom plane"}

func TestVercelAuthorizeURLIsTheInstallPage(t *testing.T) {
	got := New("").AuthorizeURL(testApp, oauth.AuthorizeRequest{Redirect: "https://lp.example/cb", State: "st8"})
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme+"://"+u.Host != "https://vercel.com" || u.EscapedPath() != "/integrations/loom%20plane/new" {
		t.Errorf("url %s", got)
	}
	if u.Query().Get("state") != "st8" {
		t.Errorf("state %q", u.Query().Get("state"))
	}
	if got := New("http://127.0.0.1:1/").AuthorizeURL(testApp, oauth.AuthorizeRequest{State: "s"}); !strings.HasPrefix(got, "http://127.0.0.1:1/integrations/loom%20plane/new?") {
		t.Errorf("test url %s", got)
	}
}

func exchangeServer(t *testing.T, status int, body string, got *url.Values) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/oauth/access_token" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("content type %q", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		*got = r.PostForm
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func TestVercelExchangeReadsTheTeam(t *testing.T) {
	var form url.Values
	c := exchangeServer(t, 200, `{"token_type":"Bearer","access_token":"vcp_new","installation_id":"icfg_1","user_id":"user_1","team_id":"team_1"}`, &form)
	g, err := c.Exchange(context.Background(), testApp, oauth.CodeRequest{Redirect: "https://lp.example/cb", Code: "the-code"})
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"client_id": "cid", "client_secret": "csecret", "code": "the-code", "redirect_uri": "https://lp.example/cb"} {
		if form.Get(k) != want {
			t.Errorf("form %s = %q, want %q", k, form.Get(k), want)
		}
	}
	if g.AccessToken != "vcp_new" || g.RefreshToken != "" || !g.Expiry.IsZero() {
		t.Errorf("grant %+v", g)
	}
	for k, want := range map[string]string{"team_id": "team_1", "user_id": "user_1", "installation_id": "icfg_1"} {
		if g.Account[k] != want {
			t.Errorf("account %s = %q, want %q", k, g.Account[k], want)
		}
	}
}

func TestVercelExchangeOnAPersonalAccount(t *testing.T) {
	var form url.Values
	c := exchangeServer(t, 200, `{"access_token":"vcp_new","installation_id":"icfg_1","user_id":"user_1","team_id":null}`, &form)
	g, err := c.Exchange(context.Background(), testApp, oauth.CodeRequest{Code: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := g.Account["team_id"]; ok || g.Account["user_id"] != "user_1" {
		t.Errorf("account %v", g.Account)
	}
}

func TestVercelExchangeErrorClasses(t *testing.T) {
	var form url.Values
	c := exchangeServer(t, 400, `{"error":"invalid_grant"}`, &form)
	if _, err := c.Exchange(context.Background(), testApp, oauth.CodeRequest{Code: "c"}); !errors.Is(err, oauth.ErrRefused) {
		t.Errorf("invalid_grant: %v", err)
	}
	c = exchangeServer(t, 400, `{"error":"invalid_client"}`, &form)
	_, err := c.Exchange(context.Background(), testApp, oauth.CodeRequest{Code: "c"})
	if !errors.Is(err, oauth.ErrAppRejected) || errors.Is(err, oauth.ErrRefused) || strings.Contains(err.Error(), "csecret") {
		t.Errorf("invalid_client: %v", err)
	}
	c = exchangeServer(t, 500, `{"error":"server_error"}`, &form)
	if _, err = c.Exchange(context.Background(), testApp, oauth.CodeRequest{Code: "c"}); err == nil || errors.Is(err, oauth.ErrRefused) || errors.Is(err, oauth.ErrAppRejected) {
		t.Errorf("500: %v", err)
	}
}

func TestVercelRefreshReturnsTheGrantUnchanged(t *testing.T) {
	in := oauth.Grant{AccessToken: "vcp", Account: map[string]string{"team_id": "team_1"}}
	got, err := New("").Refresh(context.Background(), testApp, in)
	if err != nil || got.AccessToken != "vcp" || got.Account["team_id"] != "team_1" || got.Due(time.Now(), 0) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestVercelProbeRefusedWhenUninstalled(t *testing.T) {
	var team string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		team = r.URL.Query().Get("teamId")
		switch r.Header.Get("Authorization") {
		case "Bearer live":
			_, _ = w.Write([]byte(`{"user":{"id":"user_1","username":"ada"}}`))
		case "Bearer vcp-gone":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"integration_configuration_disabled","message":"disabled"}}`))
		case "Bearer forbidden":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"no"}}`))
		case "Bearer broken":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"boom"}}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"bad token"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL)
	ctx := context.Background()
	if err := c.Probe(ctx, testApp, oauth.Grant{AccessToken: "live", Account: map[string]string{"team_id": "team_1"}}); err != nil || team != "team_1" {
		t.Fatalf("live: %v team=%q", err, team)
	}
	for _, tok := range []string{"vcp-gone", "vcp-revoked"} {
		err := c.Probe(ctx, testApp, oauth.Grant{AccessToken: tok})
		if !errors.Is(err, oauth.ErrRefused) || strings.Contains(err.Error(), tok) {
			t.Errorf("%s: %v", tok, err)
		}
	}
	for _, tok := range []string{"broken", "forbidden"} {
		if err := c.Probe(ctx, testApp, oauth.Grant{AccessToken: tok}); err == nil || errors.Is(err, oauth.ErrRefused) {
			t.Errorf("%s: %v", tok, err)
		}
	}
}
