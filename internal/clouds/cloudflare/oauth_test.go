package cloudflare

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestOAuthAuthorizeUsesPKCEAndAccountPermissions(t *testing.T) {
	u, err := url.Parse(New("", false).AuthorizeURL(oauth.App{ClientID: "lp-id", ClientSecret: "never-in-url"}, oauth.AuthorizeRequest{Redirect: "https://lp.example/integrations/oauth/callback", State: "state-1", Challenge: "challenge-1"}))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "dash.cloudflare.com" || u.Path != "/oauth2/auth" || q.Get("client_id") != "lp-id" || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") != "challenge-1" || q.Get("state") != "state-1" || q.Get("scope") != "account-settings.read zone.read dns.write offline_access" || strings.Contains(u.String(), "never-in-url") {
		t.Fatalf("authorize URL: %s", u)
	}
}

func TestOAuthLongBearerReachesDNS(t *testing.T) {
	token := strings.Repeat("a", 1024)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Path != "/zones" {
			t.Error("lost bearer or wrong endpoint")
		}
		fmt.Fprint(w, `{"success":true,"result":[],"result_info":{"page":1,"total_pages":1}}`)
	}))
	defer api.Close()
	zones, err := New(api.URL, false).ListZones(t.Context(), provider.InfrastructureCredential{Provider: "cloudflare", AccountID: strings.Repeat("a", 32), Token: token})
	if err != nil || len(zones) != 0 {
		t.Fatalf("DNS rejected OAuth bearer: %v", err)
	}
}

func TestOAuthExchangeAndRefresh(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, secret, ok := r.BasicAuth()
		if !ok || id != "lp-id" || secret != "lp-secret" || r.Method != http.MethodPost || r.URL.Path != "/oauth2/token" {
			t.Errorf("wrong token request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostForm.Get("client_secret") != "" {
			t.Error("secret duplicated in request body")
		}
		if r.PostForm.Get("grant_type") == "authorization_code" {
			if r.PostForm.Get("code") != "code-1" || r.PostForm.Get("code_verifier") != "verifier-1" || r.PostForm.Get("redirect_uri") != "https://lp.example/callback" {
				t.Error("exchange lost code, verifier or redirect")
			}
			fmt.Fprint(w, `{"access_token":"first","refresh_token":"refresh-1","expires_in":3600,"token_type":"Bearer"}`)
			return
		}
		if r.PostForm.Get("refresh_token") != "refresh-1" {
			t.Error("refresh lost token")
		}
		fmt.Fprint(w, `{"access_token":"fresh","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer api.Close()
	c, app := New(api.URL, false), oauth.App{ClientID: "lp-id", ClientSecret: "lp-secret"}
	g, err := c.Exchange(t.Context(), app, oauth.CodeRequest{Code: "code-1", Verifier: "verifier-1", Redirect: "https://lp.example/callback", Settings: map[string]string{"account_id": strings.Repeat("a", 32)}})
	if err != nil || g.AccessToken != "first" || g.RefreshToken != "refresh-1" || g.Account["account_id"] != strings.Repeat("a", 32) || !g.Expiry.After(time.Now().Add(59*time.Minute)) {
		t.Fatalf("exchange: %+v, %v", g, err)
	}
	fresh, err := c.Refresh(t.Context(), app, g)
	if err != nil || fresh.AccessToken != "fresh" || fresh.RefreshToken != "refresh-1" || fresh.Account["account_id"] != g.Account["account_id"] {
		t.Fatalf("refresh: %+v, %v", fresh, err)
	}
}

func TestOAuthTokenResponsesFailSafely(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"revoked", 400, `{"error":"invalid_grant","error_description":"secret-leak"}`, oauth.ErrRefused},
		{"app", 401, `{"error":"invalid_client","error_description":"secret-leak"}`, oauth.ErrAppRejected},
		{"unknown", 503, `{"error":"secret-leak"}`, nil},
		{"malformed", 200, `not-json-secret-leak`, nil},
		{"missing expiry", 200, `{"access_token":"secret-leak","refresh_token":"refresh","token_type":"Bearer"}`, nil},
		{"wrong type", 200, `{"access_token":"secret-leak","refresh_token":"refresh","expires_in":3600,"token_type":"MAC"}`, nil},
		{"no refresh", 200, `{"access_token":"secret-leak","expires_in":3600,"token_type":"Bearer"}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer api.Close()
			_, err := New(api.URL, false).Exchange(t.Context(), oauth.App{}, oauth.CodeRequest{})
			if err == nil || tc.want != nil && !errors.Is(err, tc.want) || strings.Contains(err.Error(), "secret-leak") {
				t.Fatalf("unsafe or wrong error: %v", err)
			}
		})
	}
}
