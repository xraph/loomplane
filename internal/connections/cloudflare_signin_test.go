package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/clouds/oauth"
)

func TestCloudflareSignInRetainsAccountAndUsesPKCE(t *testing.T) {
	r := newSignInRig(t)
	r.s.Providers[Cloudflare] = r.provider
	r.s.checkers[Cloudflare] = CloudflareChecker{}
	r.s.ConfigApps[Cloudflare] = oauth.App{ClientID: "cf-id", ClientSecret: "cf-secret"}
	started, err := r.s.StartSignIn(t.Context(), starter, SignInInput{Cloud: Cloudflare, Name: "Cloudflare", Settings: map[string]string{"account_id": strings.Repeat("a", 32), "ignored": "other"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.provider.asked.Challenge == "" || r.provider.asked.Settings["account_id"] != strings.Repeat("a", 32) || r.provider.asked.Settings["ignored"] != "" {
		t.Fatalf("lost PKCE or account: %+v", r.provider.asked)
	}
	u, _ := url.Parse(started.AuthorizeURL)
	if u.Query().Get("state") == "" {
		t.Fatal("no state")
	}
	_, err = r.s.StartSignIn(t.Context(), starter, SignInInput{Cloud: Cloudflare, Name: "Cloudflare", Settings: map[string]string{"account_id": "bad"}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid account accepted: %v", err)
	}
}

func TestCloudflareCallbackAndCredentialRenewal(t *testing.T) {
	r := newSignInRig(t)
	f := newCloudflareFixture(t)
	f.status, f.dnsCode = "expired", http.StatusBadRequest
	refreshes := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/oauth2/token" {
			f.Config.Handler.ServeHTTP(w, req)
			return
		}
		if err := req.ParseForm(); err != nil {
			t.Error(err)
		}
		if req.PostForm.Get("grant_type") == "refresh_token" {
			refreshes++
			fmt.Fprint(w, `{"access_token":"tok-secret","refresh_token":"rotated-refresh","expires_in":3600,"token_type":"Bearer"}`)
			return
		}
		if req.PostForm.Get("code_verifier") == "" {
			t.Error("missing verifier")
		}
		fmt.Fprint(w, `{"access_token":"tok-secret","refresh_token":"first-refresh","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer api.Close()
	c := cloudflare.New(api.URL, false)
	r.s.Providers[Cloudflare], r.s.checkers[Cloudflare] = c, CloudflareChecker{Client: c}
	r.s.ConfigApps[Cloudflare] = oauth.App{ClientID: "cf-id", ClientSecret: "cf-secret"}
	store := &lockingStore{memStore: r.store}
	r.s.store, r.s.grants = store, store
	r.clock = time.Now()
	state := r.start(t, starter, SignInInput{Cloud: Cloudflare, Name: "Cloudflare", Settings: map[string]string{"account_id": f.account}})
	connected, err := r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code"})
	if err != nil || connected.Status != StatusReady || connected.Method != MethodOAuth {
		t.Fatalf("complete: %+v %v", connected, err)
	}
	if _, err = r.s.CompleteSignIn(t.Context(), starter, CompleteInput{State: state, Code: "code"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("callback replay: %v", err)
	}
	identity := connected.AccountIdentity
	current, cred, err := r.s.Credential(t.Context(), starter.OrgID, connected.ID)
	if err != nil || string(cred) != "tok-secret" || refreshes != 1 || current.AccountIdentity != identity {
		t.Fatalf("credential: %s %v (%d renewals)", cred, err, refreshes)
	}
	var stored oauth.Grant
	if err = json.Unmarshal(r.secrets.kept[starter.OrgID+"/"+connected.ID], &stored); err != nil {
		t.Fatal(err)
	}
	if stored.RefreshToken != "rotated-refresh" {
		t.Fatal("rotated refresh token was not stored")
	}
	expanded, err := r.s.StartSignIn(t.Context(), starter, SignInInput{Cloud: Cloudflare, ConnectionID: connected.ID, Settings: map[string]string{"account_id": strings.Repeat("d", 32), "services": "dns,r2"}})
	if err != nil {
		t.Fatal(err)
	}
	pending := r.apps.signIns[expanded.State]
	if pending.Settings["account_id"] != f.account || pending.Settings["services"] != "dns,r2" {
		t.Fatalf("reconnect changed account or lost R2: %v", pending.Settings)
	}
	lapsed := 0
	r.s.Lapsed = func(_ context.Context, _ *Connection) { lapsed++ }
	f.accountCode = http.StatusUnauthorized
	checked, err := r.s.Recheck(t.Context(), starter, connected.ID)
	if err != nil || checked.Status != StatusSignInNeeded || lapsed != 1 {
		t.Fatalf("revoked sign-in: %+v %v (%d notices)", checked, err, lapsed)
	}
}

func TestCloudflareOAuthCheckerVerifiesAccountWithoutTokenManagement(t *testing.T) {
	f := newCloudflareFixture(t)
	f.status = "expired" // OAuth must not call the API-token verification endpoint.
	f.dnsCode = http.StatusBadRequest
	checker := CloudflareChecker{Client: cloudflare.New(f.URL, false)}
	g := oauth.Grant{AccessToken: "tok-secret", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour), Account: map[string]string{"account_id": f.account}}
	raw, _ := json.Marshal(g)
	if !checker.IsGrant(raw) {
		t.Fatal("OAuth grant accepted as a pasted API token")
	}
	c, err := checker.Check(t.Context(), raw, map[string]string{"account_id": f.account})
	if err != nil || c.Account != "Acme" || c.Details["account_id"] != f.account || c.Details["token_id"] != "" || len(c.Missing) != 0 {
		t.Fatalf("OAuth check: %+v, %v", c, err)
	}
	f.accountCode = http.StatusForbidden
	if _, err := checker.Check(t.Context(), raw, map[string]string{"account_id": f.account}); err == nil {
		t.Fatal("refused OAuth account accepted")
	}
	if _, err := checker.Parse(raw, map[string]string{"account_id": strings.Repeat("d", 32)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("changed account accepted: %v", err)
	}
}
