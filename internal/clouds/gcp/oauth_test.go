package gcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
)

var testApp = oauth.App{ClientID: "cid.apps.googleusercontent.com", ClientSecret: "csecret"}

// idToken is an unsigned ID token carrying claims, as a fake token
// endpoint hands back.
func idToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc(payload) + "." + enc([]byte("not-a-signature"))
}

// googleTokenServer answers /token with status and body, and hands the
// posted form to got.
func googleTokenServer(t *testing.T, status int, body string, got *url.Values) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/token" {
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

func TestCheckProjectID(t *testing.T) {
	for id, ok := range map[string]bool{
		"shop-prod":                    true,
		"a12345":                       true,
		strings.Repeat("a", 30):        true,
		"shop":                         false,
		"Shop-prod":                    false,
		"shop-":                        false,
		"1shop-prod":                   false,
		"shop_prod":                    false,
		strings.Repeat("a", 31):        false,
		"":                             false,
		"shop-prod/../../other-proj-x": false,
	} {
		if err := CheckProjectID(id); (err == nil) != ok {
			t.Errorf("CheckProjectID(%q) = %v, want ok=%v", id, err, ok)
		}
	}
}

const userKey = `{"type":"authorized_user","client_id":"cid.apps.googleusercontent.com","client_secret":"csecret",
"refresh_token":"1//rt","project_id":"shop-prod","client_email":"ada@example.com","token_uri":"https://oauth2.googleapis.com/token"}`

func TestParseKeyReadsAnAuthorizedUser(t *testing.T) {
	k, err := ParseKey([]byte(userKey))
	if err != nil {
		t.Fatal(err)
	}
	if !k.User || k.ProjectID != "shop-prod" || k.ClientEmail != "ada@example.com" {
		t.Fatalf("key %+v", k)
	}
	// token_uri is optional: Google's own is the default.
	var m map[string]string
	if err = json.Unmarshal([]byte(userKey), &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "token_uri")
	raw, _ := json.Marshal(m)
	if k, err = ParseKey(raw); err != nil || !k.User {
		t.Fatalf("without token_uri: %+v %v", k, err)
	}
	// A service account key is not a user's.
	sa := `{"type":"service_account","project_id":"p","client_email":"a@p.iam.gserviceaccount.com","private_key":"k"}`
	if k, err = ParseKey([]byte(sa)); err != nil || k.User {
		t.Fatalf("service account: %+v %v", k, err)
	}
}

func TestParseKeyRefusesAnAuthorizedUserWithoutAProject(t *testing.T) {
	for _, field := range []string{"project_id", "client_email", "client_id", "client_secret", "refresh_token"} {
		var m map[string]string
		if err := json.Unmarshal([]byte(userKey), &m); err != nil {
			t.Fatal(err)
		}
		delete(m, field)
		raw, _ := json.Marshal(m)
		_, err := ParseKey(raw)
		if !errors.Is(err, ErrInvalidKey) {
			t.Errorf("without %s: %v", field, err)
			continue
		}
		for _, secret := range []string{"csecret", "1//rt"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("without %s, the error quotes a secret: %v", field, err)
			}
		}
	}
}

func TestGoogleAuthorizeAsksForOfflineAccessWithPKCE(t *testing.T) {
	req := oauth.AuthorizeRequest{Redirect: "https://lp.example/cb", State: "st8", Challenge: "chal", Settings: map[string]string{"project_id": "shop-prod"}}
	u, err := url.Parse(New("").AuthorizeURL(testApp, req))
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme+"://"+u.Host+u.Path != "https://accounts.google.com/o/oauth2/v2/auth" {
		t.Errorf("url %s", u)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id": testApp.ClientID, "redirect_uri": "https://lp.example/cb", "response_type": "code",
		"scope":       "openid email https://www.googleapis.com/auth/cloud-platform",
		"access_type": "offline", "prompt": "consent", "include_granted_scopes": "true",
		"state": "st8", "code_challenge": "chal", "code_challenge_method": "S256",
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	if q.Has("client_secret") {
		t.Error("the client secret is in the authorize URL")
	}
	if got := New("http://127.0.0.1:1/").AuthorizeURL(testApp, req); !strings.HasPrefix(got, "http://127.0.0.1:1/o/oauth2/v2/auth?") {
		t.Errorf("test url %s", got)
	}
}

func TestGoogleExchangeReadsWhoSignedIn(t *testing.T) {
	var form url.Values
	body, _ := json.Marshal(map[string]any{
		"access_token": "ya29.new", "expires_in": 3599, "refresh_token": "1//new", "token_type": "Bearer",
		"id_token": idToken(t, map[string]any{"sub": "1078", "email": "ada@example.com", "iss": "https://accounts.google.com"}),
	})
	c := googleTokenServer(t, 200, string(body), &form)
	g, err := c.Exchange(context.Background(), testApp, oauth.CodeRequest{
		Redirect: "https://lp.example/cb", Code: "the-code", Verifier: "the-verifier", Settings: map[string]string{"project_id": "shop-prod"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"grant_type": "authorization_code", "code": "the-code", "code_verifier": "the-verifier",
		"client_id": testApp.ClientID, "client_secret": "csecret", "redirect_uri": "https://lp.example/cb",
	} {
		if form.Get(k) != want {
			t.Errorf("form %s = %q, want %q", k, form.Get(k), want)
		}
	}
	if g.AccessToken != "ya29.new" || g.RefreshToken != "1//new" {
		t.Errorf("grant tokens %q %q", g.AccessToken, g.RefreshToken)
	}
	if left := time.Until(g.Expiry); left < 59*time.Minute || left > time.Hour {
		t.Errorf("expiry in %s", left)
	}
	for k, want := range map[string]string{"subject": "1078", "email": "ada@example.com", "project_id": "shop-prod"} {
		if g.Account[k] != want {
			t.Errorf("account %s = %q, want %q", k, g.Account[k], want)
		}
	}
}

func TestGoogleExchangeWithoutARefreshTokenIsRefused(t *testing.T) {
	var form url.Values
	body, _ := json.Marshal(map[string]any{
		"access_token": "ya29.new", "expires_in": 3599,
		"id_token": idToken(t, map[string]any{"sub": "1078", "email": "ada@example.com"}),
	})
	c := googleTokenServer(t, 200, string(body), &form)
	_, err := c.Exchange(context.Background(), testApp, oauth.CodeRequest{Code: "c", Settings: map[string]string{"project_id": "shop-prod"}})
	if !errors.Is(err, oauth.ErrRefused) || !strings.Contains(err.Error(), "myaccount.google.com/permissions") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "ya29.new") {
		t.Fatalf("the access token leaked: %v", err)
	}
}

func TestGoogleExchangeErrorClasses(t *testing.T) {
	var form url.Values
	c := googleTokenServer(t, 400, `{"error":"invalid_grant","error_description":"Bad Request"}`, &form)
	if _, err := c.Exchange(context.Background(), testApp, oauth.CodeRequest{Code: "c"}); !errors.Is(err, oauth.ErrRefused) {
		t.Errorf("invalid_grant: %v", err)
	}
	c = googleTokenServer(t, 401, `{"error":"invalid_client","error_description":"Unauthorized"}`, &form)
	_, err := c.Exchange(context.Background(), testApp, oauth.CodeRequest{Code: "c"})
	if !errors.Is(err, oauth.ErrAppRejected) || errors.Is(err, oauth.ErrRefused) || strings.Contains(err.Error(), "csecret") {
		t.Errorf("invalid_client: %v", err)
	}
	c = googleTokenServer(t, 500, `{"error":"internal_failure","secret":"csecret"}`, &form)
	if _, err = c.Exchange(context.Background(), testApp, oauth.CodeRequest{Code: "c"}); err == nil || errors.Is(err, oauth.ErrRefused) ||
		errors.Is(err, oauth.ErrAppRejected) || strings.Contains(err.Error(), "csecret") {
		t.Errorf("500: %v", err)
	}
	// An answer without an ID token says nothing about who signed in.
	c = googleTokenServer(t, 200, `{"access_token":"ya29.new","refresh_token":"1//new","expires_in":3599}`, &form)
	if _, err = c.Exchange(context.Background(), testApp, oauth.CodeRequest{Code: "c"}); err == nil || strings.Contains(err.Error(), "1//new") {
		t.Errorf("no ID token: %v", err)
	}
}

func TestGoogleRefreshKeepsTheRefreshToken(t *testing.T) {
	var form url.Values
	c := googleTokenServer(t, 200, `{"access_token":"ya29.fresh","expires_in":3599,"token_type":"Bearer"}`, &form)
	held := oauth.Grant{AccessToken: "ya29.old", RefreshToken: "1//rt", Expiry: time.Now(), Account: map[string]string{"email": "ada@example.com", "project_id": "shop-prod"}}
	g, err := c.Refresh(context.Background(), testApp, held)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"grant_type": "refresh_token", "refresh_token": "1//rt", "client_id": testApp.ClientID, "client_secret": "csecret"} {
		if form.Get(k) != want {
			t.Errorf("form %s = %q, want %q", k, form.Get(k), want)
		}
	}
	if g.AccessToken != "ya29.fresh" || g.RefreshToken != "1//rt" || g.Account["email"] != "ada@example.com" || !g.Expiry.After(time.Now().Add(50*time.Minute)) {
		t.Errorf("grant %+v", g)
	}

	c = googleTokenServer(t, 400, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`, &form)
	if _, err = c.Refresh(context.Background(), testApp, held); !errors.Is(err, oauth.ErrRefused) || strings.Contains(err.Error(), "1//rt") {
		t.Errorf("invalid_grant: %v", err)
	}
	if err = c.Probe(context.Background(), testApp, held); !errors.Is(err, oauth.ErrRefused) {
		t.Errorf("probe: %v", err)
	}
	c = googleTokenServer(t, 401, `{"error":"invalid_client"}`, &form)
	if _, err = c.Refresh(context.Background(), testApp, held); !errors.Is(err, oauth.ErrAppRejected) || strings.Contains(err.Error(), "csecret") {
		t.Errorf("invalid_client: %v", err)
	}
}

func TestGoogleHandoutIsAnAuthorizedUserKey(t *testing.T) {
	c := New("http://127.0.0.1:1")
	g := oauth.Grant{AccessToken: "ya29.old", RefreshToken: "1//rt", Account: map[string]string{"email": "ada@example.com", "project_id": "shop-prod"}}
	raw, err := c.Handout(testApp, g, map[string]string{"project_id": "shop-prod", "region": "us-central1"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"type": "authorized_user", "client_id": testApp.ClientID, "client_secret": "csecret", "refresh_token": "1//rt",
		"project_id": "shop-prod", "client_email": "ada@example.com", "token_uri": "http://127.0.0.1:1/token"}
	if len(got) != len(want) {
		t.Errorf("handout has %d fields: %v", len(got), keys(got))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if k, err := ParseKey(raw); err != nil || !k.User {
		t.Fatalf("parse the handout: %+v %v", k, err)
	}

	for name, bad := range map[string]oauth.Grant{
		"no refresh token": {AccessToken: "ya29.old", Account: g.Account},
		"no email":         {AccessToken: "ya29.old", RefreshToken: "1//rt"},
	} {
		if _, err := c.Handout(testApp, bad, map[string]string{"project_id": "shop-prod"}); err == nil || strings.Contains(err.Error(), "csecret") || strings.Contains(err.Error(), "ya29") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := c.Handout(testApp, g, map[string]string{"project_id": "Shop"}); err == nil || strings.Contains(err.Error(), "1//rt") {
		t.Errorf("bad project: %v", err)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// userAPI is a fake Google: its token endpoint renews refresh token
// 1//rt as ya29.fresh and refuses any other, and its Resource Manager
// grants the bearer of ya29.fresh every permission on shop-prod.
func userAPI(t *testing.T, refreshes *[]url.Values) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			*refreshes = append(*refreshes, r.PostForm)
			w.Header().Set("Content-Type", "application/json")
			if r.PostForm.Get("refresh_token") != "1//rt" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"ya29.fresh","expires_in":3599,"token_type":"Bearer"}`))
		case "/v3/projects/shop-prod:testIamPermissions":
			if r.Header.Get("Authorization") != "Bearer ya29.fresh" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"bad bearer"}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"permissions": Permissions})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func TestUserSessionRenewsItself(t *testing.T) {
	var refreshes []url.Values
	c := userAPI(t, &refreshes)
	raw, err := c.Handout(testApp, oauth.Grant{AccessToken: "ya29.old", RefreshToken: "1//rt", Account: map[string]string{"email": "ada@example.com"}},
		map[string]string{"project_id": "shop-prod"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := ParseKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Session(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	missing, err := s.Missing(context.Background())
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing %v: %v", missing, err)
	}
	if len(refreshes) != 1 || refreshes[0].Get("grant_type") != "refresh_token" || refreshes[0].Get("refresh_token") != "1//rt" {
		t.Fatalf("refreshes %v", refreshes)
	}
}

func TestUserSessionWithARevokedSignInIsRejected(t *testing.T) {
	var refreshes []url.Values
	c := userAPI(t, &refreshes)
	raw, err := c.Handout(testApp, oauth.Grant{AccessToken: "ya29.old", RefreshToken: "1//revoked", Account: map[string]string{"email": "ada@example.com"}},
		map[string]string{"project_id": "shop-prod"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := ParseKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Session(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Missing(context.Background())
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("got %v", err)
	}
	for _, secret := range []string{"1//revoked", "csecret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("a secret leaked: %v", err)
		}
	}
}
