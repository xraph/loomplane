package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/oauth"
)

// The sign-in app, which is not the service principal of settings().
const appClient = "77777777-7777-7777-7777-777777777777"

var testApp = oauth.App{ClientID: appClient, ClientSecret: "app~secret"}

// azureSecrets are what a sign-in must never show.
var azureSecrets = []string{"app~secret", "rt-", "at-", "the-verifier"}

func noSecrets(t *testing.T, what, s string) {
	t.Helper()
	for _, secret := range azureSecrets {
		if strings.Contains(s, secret) {
			t.Errorf("%s carries a secret", what)
		}
	}
}

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

// tokenServer answers tenant's token endpoint with answer, which sees the
// posted form, and records every form posted.
type tokenServer struct {
	mu    sync.Mutex
	forms []url.Values
}

func (ts *tokenServer) last() url.Values {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if len(ts.forms) == 0 {
		return nil
	}
	return ts.forms[len(ts.forms)-1]
}

func newTokenServer(t *testing.T, answer func(form url.Values) (int, string)) (*Client, *tokenServer) {
	t.Helper()
	ts := &tokenServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/"+tenant+"/oauth2/v2.0/token" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("content type %q", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		ts.mu.Lock()
		ts.forms = append(ts.forms, r.PostForm)
		ts.mu.Unlock()
		status, body := answer(r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL), ts
}

func answering(status int, body string) func(url.Values) (int, string) {
	return func(url.Values) (int, string) { return status, body }
}

// placement is where a sign-in deploys: settings() without a principal.
func placement() map[string]string {
	return map[string]string{"tenant_id": tenant, "subscription_id": sub}
}

func TestCheckPlacementIsWhatParseCredentialChecked(t *testing.T) {
	// Today's cases still hold for a service principal.
	c, err := ParseCredential([]byte(" s3cr~t.value \n"), map[string]string{"tenant_id": "Contoso.onmicrosoft.com", "client_id": strings.ToUpper(client), "subscription_id": sub})
	if err != nil || c.Secret != "s3cr~t.value" || c.ClientID != client || c.ResourceGroup != DefaultResourceGroup || c.TenantID != "Contoso.onmicrosoft.com" {
		t.Fatalf("%+v, %v", c, err)
	}
	for name, tt := range map[string]struct {
		mutate func(map[string]string)
		// placed is whether CheckPlacement takes it: the client is the
		// principal's, not the placement's.
		placed bool
	}{
		"as given":            {func(map[string]string) {}, true},
		"no tenant":           {func(m map[string]string) { delete(m, "tenant_id") }, false},
		"tenant is a path":    {func(m map[string]string) { m["tenant_id"] = tenant + "/../x" }, false},
		"client isn't a GUID": {func(m map[string]string) { m["client_id"] = "my-app" }, true},
		"no subscription":     {func(m map[string]string) { m["subscription_id"] = "" }, false},
		"bad resource group":  {func(m map[string]string) { m["resource_group"] = "ends-in." }, false},
		"a resource group":    {func(m map[string]string) { m["resource_group"] = "shop-prod" }, true},
	} {
		m := settings()
		tt.mutate(m)
		_, parseErr := ParseCredential([]byte("secret"), m)
		if parsed := tt.placed && name != "client isn't a GUID"; (parseErr == nil) != parsed || (parseErr != nil && !errors.Is(parseErr, ErrInvalid)) {
			t.Errorf("ParseCredential %s: %v", name, parseErr)
		}
		_, _, _, placeErr := CheckPlacement(m)
		if (placeErr == nil) != tt.placed || (placeErr != nil && !errors.Is(placeErr, ErrInvalid)) {
			t.Errorf("CheckPlacement %s: %v", name, placeErr)
		}
	}
	if _, spaceErr := ParseCredential([]byte("two words"), settings()); !errors.Is(spaceErr, ErrInvalid) {
		t.Errorf("a secret with a space: %v", spaceErr)
	}

	// Both normalize the same way: GUIDs lower-cased, a domain kept, and
	// no group the default one.
	m := map[string]string{"tenant_id": " " + strings.ToUpper(tenant) + " ", "client_id": client, "subscription_id": strings.ToUpper(sub)}
	tn, sb, group, err := CheckPlacement(m)
	if err != nil || tn != tenant || sb != sub || group != DefaultResourceGroup {
		t.Fatalf("CheckPlacement = %q %q %q, %v", tn, sb, group, err)
	}
	if c, err = ParseCredential([]byte("secret"), m); err != nil || c.TenantID != tn || c.SubscriptionID != sb || c.ResourceGroup != group {
		t.Fatalf("ParseCredential %+v, %v", c, err)
	}
	if tn, _, _, err = CheckPlacement(map[string]string{"tenant_id": "Contoso.onmicrosoft.com", "subscription_id": sub}); err != nil || tn != "Contoso.onmicrosoft.com" {
		t.Fatalf("a domain tenant: %q, %v", tn, err)
	}
}

// signInKey is the azure_user key a sign-in hands out, with fields left
// out as asked.
func signInKey(t *testing.T, without ...string) []byte {
	t.Helper()
	k := map[string]string{"type": "azure_user", "client_id": strings.ToUpper(appClient), "client_secret": "app~secret",
		"refresh_token": "rt-" + strings.Repeat("x", 3000), "username": "ada@contoso.com"}
	for _, field := range without {
		delete(k, field)
	}
	raw, err := json.Marshal(k)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseCredentialReadsASignIn(t *testing.T) {
	// The client is the sign-in app's, from the key; a principal's in
	// the settings is ignored.
	s := placement()
	s["client_id"] = client
	s["resource_group"] = "shop-prod"
	s["signed_in_as"] = "someone@else.com"
	c, err := ParseCredential(signInKey(t), s)
	if err != nil {
		t.Fatal(err)
	}
	if c.ClientID != appClient || c.Secret != "app~secret" || !strings.HasPrefix(c.RefreshToken, "rt-") || c.Username != "ada@contoso.com" {
		t.Fatalf("credential client %q user %q", c.ClientID, c.Username)
	}
	if c.TenantID != tenant || c.SubscriptionID != sub || c.ResourceGroup != "shop-prod" {
		t.Fatalf("placement %q %q %q", c.TenantID, c.SubscriptionID, c.ResourceGroup)
	}

	for _, field := range []string{"client_id", "client_secret", "refresh_token", "username"} {
		_, err := ParseCredential(signInKey(t, field), placement())
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("without %s: %v", field, err)
			continue
		}
		noSecrets(t, "without "+field+", the error", err.Error())
	}
	for name, raw := range map[string][]byte{
		"another type":   []byte(`{"type":"service_principal","client_id":"` + appClient + `","client_secret":"app~secret","refresh_token":"rt-1","username":"a"}`),
		"not JSON":       []byte(`{"type":"azure_user",`),
		"client no GUID": []byte(`{"type":"azure_user","client_id":"my-app","client_secret":"app~secret","refresh_token":"rt-1","username":"a"}`),
		"too large":      []byte(`{"type":"azure_user","refresh_token":"rt-` + strings.Repeat("x", 64<<10) + `"}`),
	} {
		_, err := ParseCredential(raw, placement())
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		noSecrets(t, name+", the error", err.Error())
	}
	bad := placement()
	bad["subscription_id"] = "nope"
	if _, err := ParseCredential(signInKey(t), bad); !errors.Is(err, ErrInvalid) {
		t.Errorf("a bad placement: %v", err)
	}
}

func TestAzureAuthorizeUsesTheTenantAndPKCE(t *testing.T) {
	req := oauth.AuthorizeRequest{Redirect: "https://lp.example/cb", State: "st8", Challenge: "chal", Settings: placement()}
	u, err := url.Parse(New("").AuthorizeURL(testApp, req))
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme+"://"+u.Host+u.Path != "https://login.microsoftonline.com/"+tenant+"/oauth2/v2.0/authorize" {
		t.Errorf("url %s", u)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id": appClient, "response_type": "code", "redirect_uri": "https://lp.example/cb", "response_mode": "query",
		"scope": "openid profile offline_access https://management.azure.com/user_impersonation",
		"state": "st8", "code_challenge": "chal", "code_challenge_method": "S256",
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	if q.Has("client_secret") {
		t.Error("the client secret is in the authorize URL")
	}
	// A tenant named by its domain is escaped into the path, and a test's
	// fake takes the place of Microsoft's host.
	req.Settings = map[string]string{"tenant_id": "contoso.onmicrosoft.com", "subscription_id": sub}
	if got := New("http://127.0.0.1:1/").AuthorizeURL(testApp, req); !strings.HasPrefix(got, "http://127.0.0.1:1/contoso.onmicrosoft.com/oauth2/v2.0/authorize?") {
		t.Errorf("test url %s", got)
	}
}

func TestAzureExchangeReadsWhoSignedIn(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"access_token": "at-first", "expires_in": 3599, "refresh_token": "rt-first", "token_type": "Bearer",
		"id_token": idToken(t, map[string]any{"oid": "aaaaaaaa-0000-0000-0000-000000000001", "tid": tenant, "preferred_username": "ada@contoso.com"}),
	})
	c, ts := newTokenServer(t, answering(200, string(body)))
	s := placement()
	s["resource_group"] = "shop-prod"
	g, err := c.Exchange(context.Background(), testApp, oauth.CodeRequest{Redirect: "https://lp.example/cb", Code: "the-code", Verifier: "the-verifier", Settings: s})
	if err != nil {
		t.Fatal(err)
	}
	form := ts.last()
	for k, want := range map[string]string{
		"grant_type": "authorization_code", "code": "the-code", "code_verifier": "the-verifier",
		"client_id": appClient, "client_secret": "app~secret", "redirect_uri": "https://lp.example/cb",
	} {
		if form.Get(k) != want {
			t.Errorf("form %s = %q, want %q", k, form.Get(k), want)
		}
	}
	if g.AccessToken != "at-first" || g.RefreshToken != "rt-first" {
		t.Error("the grant doesn't carry the tokens Azure gave")
	}
	if left := time.Until(g.Expiry); left < 59*time.Minute || left > time.Hour {
		t.Errorf("expiry in %s", left)
	}
	want := map[string]string{"subject": "aaaaaaaa-0000-0000-0000-000000000001", "email": "ada@contoso.com",
		"tenant_id": tenant, "subscription_id": sub, "resource_group": "shop-prod"}
	if !maps.Equal(g.Account, want) {
		t.Errorf("account %v", g.Account)
	}

	// An answer that doesn't say who signed in is no sign-in.
	for name, claims := range map[string]map[string]any{
		"no oid":      {"tid": tenant, "preferred_username": "ada@contoso.com"},
		"no username": {"oid": "aaaaaaaa-0000-0000-0000-000000000001", "tid": tenant},
	} {
		body, _ := json.Marshal(map[string]any{"access_token": "at-first", "refresh_token": "rt-first", "expires_in": 3599, "id_token": idToken(t, claims)})
		anon, _ := newTokenServer(t, answering(200, string(body)))
		_, anonErr := anon.Exchange(context.Background(), testApp, oauth.CodeRequest{Code: "c", Verifier: "the-verifier", Settings: placement()})
		if anonErr == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		noSecrets(t, name+", the error", anonErr.Error())
	}
	c, _ = newTokenServer(t, answering(200, `{"access_token":"at-first","refresh_token":"rt-first","expires_in":3599}`))
	if _, err = c.Exchange(context.Background(), testApp, oauth.CodeRequest{Code: "c", Settings: placement()}); err == nil {
		t.Error("no ID token: no error")
	} else {
		noSecrets(t, "no ID token, the error", err.Error())
	}
	c, _ = newTokenServer(t, answering(200, `{"access_token":"at-first","expires_in":3599,"id_token":"`+idToken(t, map[string]any{"oid": "o", "preferred_username": "a"})+`"}`))
	if _, err = c.Exchange(context.Background(), testApp, oauth.CodeRequest{Code: "c", Settings: placement()}); !errors.Is(err, oauth.ErrRefused) {
		t.Errorf("no refresh token: %v", err)
	}
}

func TestAzureTokenErrorsKeepOnlyTheCode(t *testing.T) {
	long := "AADSTS70008: The provided authorization code or refresh token has expired. app~secret " + strings.Repeat("trace ", 200)
	for _, tt := range []struct {
		status          int
		code            string
		refused, appBad bool
	}{
		{400, "invalid_grant", true, false},
		{400, "interaction_required", true, false},
		{401, "invalid_client", false, true},
		{400, "unauthorized_client", false, true},
		{400, "invalid_request", false, false},
		{500, "temporarily_unavailable", false, false},
		{400, strings.Repeat("y", 500), false, false},
	} {
		body, _ := json.Marshal(map[string]any{"error": tt.code, "error_description": long})
		c, _ := newTokenServer(t, answering(tt.status, string(body)))
		_, err := c.Refresh(context.Background(), testApp, oauth.Grant{RefreshToken: "rt-old", Account: placement()})
		if err == nil {
			t.Errorf("%d %s: no error", tt.status, tt.code)
			continue
		}
		if errors.Is(err, oauth.ErrRefused) != tt.refused || errors.Is(err, oauth.ErrAppRejected) != tt.appBad {
			t.Errorf("%d %.20s: %v", tt.status, tt.code, err)
		}
		if strings.Contains(err.Error(), "AADSTS") || strings.Contains(err.Error(), "trace") || len(err.Error()) > 200 {
			t.Errorf("%d %.20s: the error keeps the description: %.300s", tt.status, tt.code, err)
		}
		noSecrets(t, tt.code+", the error", err.Error())
	}
	c, _ := newTokenServer(t, answering(502, `<html>app~secret</html>`))
	if _, err := c.Refresh(context.Background(), testApp, oauth.Grant{RefreshToken: "rt-old", Account: placement()}); err == nil {
		t.Error("502: no error")
	} else {
		noSecrets(t, "502, the error", err.Error())
	}
}

func TestAzureRefreshKeepsTheRotatedToken(t *testing.T) {
	held := oauth.Grant{AccessToken: "at-old", RefreshToken: "rt-old", Expiry: time.Now(),
		Account: map[string]string{"subject": "o", "email": "ada@contoso.com", "tenant_id": tenant, "subscription_id": sub, "resource_group": "loomplane"}}
	c, ts := newTokenServer(t, answering(200, `{"access_token":"at-fresh","refresh_token":"rt-new","expires_in":3599}`))
	g, err := c.Refresh(context.Background(), testApp, held)
	if err != nil {
		t.Fatal(err)
	}
	form := ts.last()
	for k, want := range map[string]string{
		"grant_type": "refresh_token", "refresh_token": "rt-old", "client_id": appClient, "client_secret": "app~secret",
		"scope": "https://management.azure.com/.default offline_access",
	} {
		if form.Get(k) != want {
			t.Errorf("form %s = %q, want %q", k, form.Get(k), want)
		}
	}
	if g.AccessToken != "at-fresh" || g.RefreshToken != "rt-new" || !maps.Equal(g.Account, held.Account) || !g.Expiry.After(time.Now().Add(50*time.Minute)) {
		t.Errorf("grant account %v, expiry %s", g.Account, g.Expiry)
	}

	// An answer without a new refresh token keeps the old one.
	c, _ = newTokenServer(t, answering(200, `{"access_token":"at-fresh","expires_in":3599}`))
	if g, err = c.Refresh(context.Background(), testApp, held); err != nil || g.RefreshToken != "rt-old" {
		t.Errorf("no rotation: %v", err)
	}
	// A probe renews and drops what it got.
	c, ts = newTokenServer(t, answering(200, `{"access_token":"at-fresh","refresh_token":"rt-new","expires_in":3599}`))
	if err = c.Probe(context.Background(), testApp, held); err != nil || ts.last().Get("refresh_token") != "rt-old" {
		t.Errorf("probe: %v", err)
	}
	c, _ = newTokenServer(t, answering(400, `{"error":"invalid_grant"}`))
	if err = c.Probe(context.Background(), testApp, held); !errors.Is(err, oauth.ErrRefused) {
		t.Errorf("refused probe: %v", err)
	}
}

func TestAzureHandoutIsASignInKey(t *testing.T) {
	g := oauth.Grant{AccessToken: "at-old", RefreshToken: "rt-held", Account: map[string]string{"subject": "o", "email": "ada@contoso.com", "tenant_id": tenant}}
	details := placement()
	details["signed_in_as"] = "ada@contoso.com"
	raw, err := New("").Handout(testApp, g, details)
	if err != nil {
		t.Fatal(err)
	}
	var k map[string]string
	if err = json.Unmarshal(raw, &k); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"type": "azure_user", "client_id": appClient, "client_secret": "app~secret", "refresh_token": "rt-held", "username": "ada@contoso.com"}
	if !maps.Equal(k, want) {
		t.Errorf("handout keys %v", slices.Sorted(maps.Keys(k)))
	}
	c, err := ParseCredential(raw, details)
	if err != nil || c.RefreshToken != "rt-held" || c.Username != "ada@contoso.com" || c.SubscriptionID != sub {
		t.Fatalf("parse the handout: %v", err)
	}
	for name, bad := range map[string]oauth.Grant{
		"no refresh token": {Account: g.Account},
		"no email":         {RefreshToken: "rt-held", Account: map[string]string{"subject": "o"}},
	} {
		_, badErr := New("").Handout(testApp, bad, details)
		if badErr == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		noSecrets(t, name+", the error", badErr.Error())
	}
	if _, err = New("").Handout(testApp, g, map[string]string{"tenant_id": tenant}); err == nil {
		t.Error("no subscription: no error")
	}
}

func TestBearerRenewsFromARefreshToken(t *testing.T) {
	// Each answer rotates the refresh token, and expires at once, so
	// every bearer call renews.
	var n int
	var mu sync.Mutex
	c, ts := newTokenServer(t, func(form url.Values) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if form.Get("refresh_token") != "rt-"+string(rune('0'+n)) {
			return 400, `{"error":"invalid_grant","error_description":"AADSTS70008: expired"}`
		}
		return 200, `{"access_token":"at-` + string(rune('0'+n)) + `","refresh_token":"rt-` + string(rune('0'+n+1)) + `","expires_in":60}`
	})
	cred, err := ParseCredential(mustKey(t, "rt-1"), placement())
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Session(context.Background(), cred)
	if err != nil {
		t.Fatal(err)
	}
	form := ts.last()
	for k, want := range map[string]string{
		"grant_type": "refresh_token", "client_id": appClient, "client_secret": "app~secret", "refresh_token": "rt-1",
		"scope": "https://management.azure.com/.default offline_access",
	} {
		if form.Get(k) != want {
			t.Errorf("form %s = %q, want %q", k, form.Get(k), want)
		}
	}
	// The second renewal spends the token the first one was given.
	tok, err := s.bearer(context.Background())
	if err != nil || tok != "at-2" || ts.last().Get("refresh_token") != "rt-2" {
		t.Fatalf("second renewal: %v", err)
	}
	if s.Credential().RefreshToken != "rt-1" {
		t.Error("the session's credential changed under its callers")
	}

	// A refused refresh token is ErrRejected, with only the code.
	cred.RefreshToken = "rt-revoked"
	_, err = c.Session(context.Background(), cred)
	if !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "invalid_grant") || strings.Contains(err.Error(), "AADSTS") {
		t.Fatalf("refused: %v", err)
	}
	noSecrets(t, "the refusal", err.Error())

	// A principal still signs in with its secret.
	pc, pts := newTokenServer(t, answering(200, `{"access_token":"tok","expires_in":3600}`))
	pcred, err := ParseCredential([]byte("p~secret"), settings())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pc.Session(context.Background(), pcred); err != nil {
		t.Fatal(err)
	}
	if f := pts.last(); f.Get("grant_type") != "client_credentials" || f.Get("scope") != DefaultManagement+"/.default" || f.Has("refresh_token") {
		t.Errorf("principal form grant %q scope %q", f.Get("grant_type"), f.Get("scope"))
	}
}

func mustKey(t *testing.T, refresh string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"type": "azure_user", "client_id": appClient, "client_secret": "app~secret", "refresh_token": refresh, "username": "ada@contoso.com"})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
