//go:build integration

package integration

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
)

// An organization signs in with a configured DigitalOcean app and
// gets a ready connection whose grant never comes back over the API.
func TestCloudSignIn_digitalOceanEndToEnd(t *testing.T) {
	const (
		clientID     = "do-app"
		clientSecret = "do-app-secret-0123"
		accessToken  = "dop_v1_signed_in"
		refreshToken = "dor_v1_signed_in"
	)
	cfg := config.Defaults()
	callback := strings.TrimRight(cfg.PublicURL, "/") + "/integrations/oauth/callback"
	do := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth/token":
			if err := r.ParseForm(); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			for k, want := range map[string]string{"grant_type": "authorization_code", "code": "ok",
				"client_id": clientID, "client_secret": clientSecret, "redirect_uri": callback} {
				if got := r.PostForm.Get(k); got != want {
					t.Errorf("token form %s = %q, want %q", k, got, want)
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
					return
				}
			}
			_, _ = w.Write([]byte(`{"access_token":"` + accessToken + `","refresh_token":"` + refreshToken + `","expires_in":2592000,` +
				`"info":{"uuid":"acct-1","email":"ada@example.com","team":{"uuid":"team-acme","name":"Acme"}}}`))
		case "/v2/account":
			if r.Header.Get("Authorization") != "Bearer "+accessToken {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"id":"Unauthorized","message":"Unable to authenticate you"}`))
				return
			}
			_, _ = w.Write([]byte(`{"account":{"uuid":"acct-1","email":"ada@example.com","status":"active","team":{"uuid":"team-acme","name":"Acme"}}}`))
		case "/v2/databases":
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+accessToken {
				t.Error("inventory must read through the connected account")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`{"databases":[],"links":{},"meta":{"total":0}}`))
		default:
			t.Errorf("asked DigitalOcean %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer do.Close()
	cfg.Database.URL = freshDatabase(t)
	cfg.Clouds.DigitalOceanEndpoint = do.URL
	cfg.Clouds.OAuth = map[string]config.OAuthApp{"digitalocean": {ClientID: clientID, ClientSecret: clientSecret}}
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}

	c.must(http.StatusUnauthorized, http.MethodGet, "/api/v1/platform/cloud-sign-in/apps", nil, nil)
	signedIn(t, c)
	if path := os.Getenv("LOOMPLANE_TEST_OPENAPI_OUTPUT"); path != "" {
		var contract json.RawMessage
		c.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &contract)
		if err := os.WriteFile(path, contract, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	type appList struct {
		Apps        []connections.SignInApp `json:"apps"`
		CallbackURL string                  `json:"callback_url"`
	}
	source := func() string {
		t.Helper()
		var list appList
		c.must(http.StatusOK, http.MethodGet, "/api/v1/platform/cloud-sign-in/apps", nil, &list)
		if list.CallbackURL != callback {
			t.Fatalf("callback url %q, want %q", list.CallbackURL, callback)
		}
		for _, a := range list.Apps {
			if a.Cloud == connections.DigitalOcean {
				return a.Source
			}
		}
		t.Fatalf("no DigitalOcean in %+v", list.Apps)
		return ""
	}
	methods := func() []string {
		t.Helper()
		var list struct {
			Clouds []struct {
				Cloud   string   `json:"cloud"`
				Methods []string `json:"methods"`
			} `json:"clouds"`
		}
		c.must(http.StatusOK, http.MethodGet, "/api/v1/connection-clouds", nil, &list)
		for _, f := range list.Clouds {
			if f.Cloud == connections.DigitalOcean {
				return f.Methods
			}
		}
		t.Fatal("connection-clouds has no DigitalOcean")
		return nil
	}

	if got := source(); got != connections.AppFromConfig {
		t.Fatalf("source of the configured app: %q", got)
	}
	if body := c.raw("/api/v1/platform/cloud-sign-in/apps"); strings.Contains(body, clientSecret) {
		t.Fatalf("the app list shows the secret: %s", body)
	}
	if got := methods(); strings.Join(got, ",") != "oauth,token" {
		t.Fatalf("methods with an app: %v", got)
	}

	var signIn connections.SignIn
	c.must(http.StatusOK, http.MethodPost, "/api/v1/connections/sign-ins",
		connections.SignInInput{Cloud: connections.DigitalOcean, Name: "DigitalOcean"}, &signIn)
	u, err := url.Parse(signIn.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if !strings.HasPrefix(signIn.AuthorizeURL, do.URL+"/v1/oauth/authorize?") || q.Get("client_id") != clientID || q.Get("redirect_uri") != callback || q.Get("state") == "" {
		t.Fatalf("authorize url %s", signIn.AuthorizeURL)
	}

	var raw json.RawMessage
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections/sign-ins/complete",
		connections.CompleteInput{State: q.Get("state"), Code: "ok"}, &raw)
	var conn connections.Connection
	if err = json.Unmarshal(raw, &conn); err != nil {
		t.Fatal(err)
	}
	if conn.Method != connections.MethodOAuth || conn.Status != connections.StatusReady || conn.Account != "Acme" {
		t.Fatalf("connection %+v", conn)
	}
	for _, body := range []string{string(raw), c.raw("/api/v1/connections/" + conn.ID), c.raw("/api/v1/connections")} {
		for _, leak := range []string{"access_token", accessToken, refreshToken, clientSecret} {
			if strings.Contains(body, leak) {
				t.Fatalf("a connection response contains %q: %s", leak, body)
			}
		}
	}
	// A state works once.
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/connections/sign-ins/complete",
		connections.CompleteInput{State: q.Get("state"), Code: "ok"}, nil)

	// Signing in again for the same connection replaces its credential.
	c.must(http.StatusOK, http.MethodPost, "/api/v1/connections/sign-ins",
		connections.SignInInput{Cloud: connections.DigitalOcean, ConnectionID: conn.ID}, &signIn)
	u, err = url.Parse(signIn.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	var replaced connections.Connection
	c.must(http.StatusOK, http.MethodPost, "/api/v1/connections/sign-ins/complete",
		connections.CompleteInput{State: u.Query().Get("state"), Code: "ok"}, &replaced)
	if replaced.ID != conn.ID || replaced.Method != connections.MethodOAuth {
		t.Fatalf("replaced %+v", replaced)
	}

}

// fakeMemberDigitalOcean is a DigitalOcean that honors one app and signs anyone
// in as the account acct-1.
func fakeMemberDigitalOcean(t *testing.T, clientID, clientSecret, callback string) *httptest.Server {
	t.Helper()
	const accessToken = "dop_v1_member"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth/token":
			if err := r.ParseForm(); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			for k, want := range map[string]string{"code": "ok", "client_id": clientID,
				"client_secret": clientSecret, "redirect_uri": callback} {
				if got := r.PostForm.Get(k); got != want {
					t.Errorf("token form %s = %q, want %q", k, got, want)
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
					return
				}
			}
			_, _ = w.Write([]byte(`{"access_token":"` + accessToken + `","refresh_token":"dor_v1_member","expires_in":2592000,` +
				`"info":{"uuid":"acct-1","email":"grace@example.com","team":{"uuid":"team-acme","name":"Acme"}}}`))
		case "/v2/account":
			if r.Header.Get("Authorization") != "Bearer "+accessToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"account":{"uuid":"acct-1","email":"grace@example.com","status":"active","team":{"uuid":"team-acme","name":"Acme"}}}`))
		case "/v2/databases":
			_, _ = w.Write([]byte(`{"databases":[],"links":{},"meta":{"total":0}}`))
		default:
			t.Errorf("asked DigitalOcean %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// secondOrgMember signs up another account and gives it an organization of
// its own. It holds no platform role.
func secondOrgMember(t *testing.T, owner *client, cfg config.Config, email string) *client {
	t.Helper()
	member := &client{t: t, base: owner.base, key: cfg.Identity.PublishableKey}
	account(t, member, email)
	var org struct {
		ID string `json:"id"`
	}
	member.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Grace Co", "slug": "grace-co"}, &org)
	member.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	return member
}

func cloudMethods(t *testing.T, c *client, cloud string) string {
	t.Helper()
	var list struct {
		Clouds []struct {
			Cloud   string   `json:"cloud"`
			Methods []string `json:"methods"`
		} `json:"clouds"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/connection-clouds", nil, &list)
	for _, f := range list.Clouds {
		if f.Cloud == cloud {
			return strings.Join(f.Methods, ",")
		}
	}
	t.Fatalf("connection-clouds has no %s", cloud)
	return ""
}

// The platform owner sets a DigitalOcean app over HTTP and a member of
// another organization signs in with it. Until the app exists the cloud
// offers only tokens, and the secret never comes back.
func TestCloudSignInPlatform_ownerSetsAnAppAndMembersSignIn(t *testing.T) {
	const (
		clientID     = "do-platform-app"
		clientSecret = "do-platform-secret-0123"
	)
	cfg := config.Defaults()
	callback := strings.TrimRight(cfg.PublicURL, "/") + "/integrations/oauth/callback"
	do := fakeMemberDigitalOcean(t, clientID, clientSecret, callback)
	cfg.Database.URL = freshDatabase(t)
	cfg.Clouds.DigitalOceanEndpoint = do.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	owner := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, owner)
	member := secondOrgMember(t, owner, cfg, "grace@example.com")

	if got := cloudMethods(t, member, connections.DigitalOcean); got != "token" {
		t.Fatalf("methods with no app: %q, want token", got)
	}
	member.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/connections/sign-ins",
		connections.SignInInput{Cloud: connections.DigitalOcean, Name: "DigitalOcean"}, nil)

	// A secret is required, and Vercel needs its slug.
	owner.must(http.StatusUnprocessableEntity, http.MethodPut, "/api/v1/platform/cloud-sign-in/apps/digitalocean",
		connections.AppInput{ClientID: clientID}, nil)
	owner.must(http.StatusUnprocessableEntity, http.MethodPut, "/api/v1/platform/cloud-sign-in/apps/vercel",
		connections.AppInput{ClientID: "oac_app", ClientSecret: "vercel-secret"}, nil)

	var set json.RawMessage
	owner.must(http.StatusOK, http.MethodPut, "/api/v1/platform/cloud-sign-in/apps/digitalocean",
		connections.AppInput{ClientID: clientID, ClientSecret: clientSecret}, &set)
	var saved connections.SignInApp
	if err := json.Unmarshal(set, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Source != connections.AppFromPlatform || saved.ClientID != clientID || saved.UpdatedBy == "" {
		t.Fatalf("saved app %+v", saved)
	}
	if strings.Contains(string(set), clientSecret) {
		t.Fatalf("the PUT response shows the secret: %s", set)
	}
	if body := owner.raw("/api/v1/platform/cloud-sign-in/apps"); strings.Contains(body, clientSecret) {
		t.Fatalf("the app list shows the secret: %s", body)
	}
	if got := cloudMethods(t, member, connections.DigitalOcean); got != "oauth,token" {
		t.Fatalf("methods with the platform app: %q, want oauth,token", got)
	}

	var signIn connections.SignIn
	member.must(http.StatusOK, http.MethodPost, "/api/v1/connections/sign-ins",
		connections.SignInInput{Cloud: connections.DigitalOcean, Name: "DigitalOcean"}, &signIn)
	u, err := url.Parse(signIn.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	if q := u.Query(); q.Get("client_id") != clientID || q.Get("state") == "" {
		t.Fatalf("authorize url %s", signIn.AuthorizeURL)
	}
	var conn connections.Connection
	member.must(http.StatusCreated, http.MethodPost, "/api/v1/connections/sign-ins/complete",
		connections.CompleteInput{State: u.Query().Get("state"), Code: "ok"}, &conn)
	if conn.Method != connections.MethodOAuth || conn.Status != connections.StatusReady || conn.Account != "Acme" {
		t.Fatalf("connection %+v", conn)
	}

	// The owner's list counts that sign-in.
	var list struct {
		Apps []connections.SignInApp `json:"apps"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/api/v1/platform/cloud-sign-in/apps", nil, &list)
	for _, a := range list.Apps {
		if a.Cloud == connections.DigitalOcean && (a.Source != connections.AppFromPlatform || a.Connections != 1 || a.Organizations != 1) {
			t.Fatalf("digitalocean in the platform list: %+v", a)
		}
	}

	owner.must(http.StatusNoContent, http.MethodDelete, "/api/v1/platform/cloud-sign-in/apps/digitalocean", nil, nil)
	owner.must(http.StatusNotFound, http.MethodDelete, "/api/v1/platform/cloud-sign-in/apps/digitalocean", nil, nil)
	if got := cloudMethods(t, member, connections.DigitalOcean); got != "token" {
		t.Fatalf("methods after the app was removed: %q, want token", got)
	}
}

// Only a platform owner or admin on a session reaches the platform routes.
// An organization's owner, and any API key, get the 404 an unknown route
// would.
func TestCloudSignInPlatform_closedToEveryoneElse(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	owner := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, owner)
	member := secondOrgMember(t, owner, cfg, "grace@example.com")

	anon := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	anon.must(http.StatusUnauthorized, http.MethodGet, "/api/v1/platform/cloud-sign-in/apps", nil, nil)

	const apps = "/api/v1/platform/cloud-sign-in/apps"
	closed := func(who string, c *client) {
		t.Helper()
		c.must(http.StatusNotFound, http.MethodGet, apps, nil, nil)
		c.must(http.StatusNotFound, http.MethodPut, apps+"/digitalocean", connections.AppInput{ClientID: "id", ClientSecret: "secret"}, nil)
		c.must(http.StatusNotFound, http.MethodDelete, apps+"/digitalocean", nil, nil)
		t.Logf("%s is refused", who)
	}
	closed("an organization owner", member)

	// Keys never count, not even the platform owner's own.
	for who, maker := range map[string]*client{"the platform owner's key": owner, "a member's key": member} {
		var key struct {
			Secret string `json:"secret"`
		}
		maker.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "ci", "access": "write"}, &key)
		closed(who, &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey, token: key.Secret})
	}

	// Nothing above set an app.
	var list struct {
		Apps []connections.SignInApp `json:"apps"`
	}
	owner.must(http.StatusOK, http.MethodGet, apps, nil, &list)
	for _, a := range list.Apps {
		if a.Source != connections.AppFromNone {
			t.Fatalf("a refused PUT set an app: %+v", a)
		}
	}
}

// A cloud whose app comes from configuration can't be set or removed.
func TestCloudSignInPlatform_configLocksACloud(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Clouds.OAuth = map[string]config.OAuthApp{"digitalocean": {ClientID: "cfg-app", ClientSecret: "cfg-secret"}}
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	owner := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, owner)

	owner.must(http.StatusConflict, http.MethodPut, "/api/v1/platform/cloud-sign-in/apps/digitalocean",
		connections.AppInput{ClientID: "other", ClientSecret: "other-secret"}, nil)
	owner.must(http.StatusConflict, http.MethodDelete, "/api/v1/platform/cloud-sign-in/apps/digitalocean", nil, nil)
}

// An organization signs in to a project with a configured Google app, naming
// the project first, and gets a ready connection that says who signed in. No
// grant, verifier or app secret ever comes back over the API.
func TestCloudSignIn_googleEndToEnd(t *testing.T) {
	const (
		clientID     = "g-app.apps.googleusercontent.com"
		clientSecret = "g-app-secret-0123"
		firstToken   = "ya29.first-access"
		freshToken   = "ya29.fresh-access"
		refreshToken = "1//refresh-ada"
	)
	cfg := config.Defaults()
	callback := strings.TrimRight(cfg.PublicURL, "/") + "/integrations/oauth/callback"
	enc := base64.RawURLEncoding.EncodeToString
	// verifier is the PKCE verifier Google got, set on the fake's goroutine.
	var verifier atomic.Value
	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f := r.PostForm
			switch f.Get("grant_type") {
			case "authorization_code":
				if f.Get("code") != "ok" || f.Get("code_verifier") == "" || f.Get("client_id") != clientID ||
					f.Get("client_secret") != clientSecret || f.Get("redirect_uri") != callback {
					t.Errorf("code exchange form %v", f)
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
					return
				}
				verifier.Store(f.Get("code_verifier"))
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token": firstToken, "refresh_token": refreshToken, "expires_in": 3599,
					"id_token": enc([]byte(`{"alg":"RS256"}`)) + "." + enc([]byte(`{"sub":"1078","email":"ada@example.com"}`)) + ".sig",
				})
			case "refresh_token":
				if f.Get("refresh_token") != refreshToken {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
					return
				}
				_, _ = w.Write([]byte(`{"access_token":"` + freshToken + `","expires_in":3599,"token_type":"Bearer"}`))
			default:
				t.Errorf("token grant %q", f.Get("grant_type"))
				w.WriteHeader(http.StatusBadRequest)
			}
		case "/v3/projects/shop-prod:testIamPermissions":
			if auth := r.Header.Get("Authorization"); auth != "Bearer "+freshToken && auth != "Bearer "+firstToken {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"Request had invalid authentication credentials."}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"permissions": gcp.UserPermissions})
		case "/sql/v1beta4/projects/shop-prod/instances", "/v1/projects/shop-prod/locations/-/instances", "/storage/v1/b":
			if auth := r.Header.Get("Authorization"); r.Method != http.MethodGet || auth != "Bearer "+freshToken && auth != "Bearer "+firstToken {
				t.Error("inventory must read through the connected Google grant")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if r.URL.Path == "/storage/v1/b" && r.URL.Query().Get("project") != "shop-prod" {
				t.Error("bucket inventory reached another project")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`{"items":[],"instances":[]}`))
		default:
			t.Errorf("asked Google %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer google.Close()
	cfg.Database.URL = freshDatabase(t)
	cfg.Clouds.GoogleEndpoint = google.URL
	cfg.Clouds.OAuth = map[string]config.OAuthApp{"gcp": {ClientID: clientID, ClientSecret: clientSecret}}
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)

	var forms struct {
		Clouds []struct {
			Cloud          string   `json:"cloud"`
			Methods        []string `json:"methods"`
			SignInSettings []struct {
				Key      string `json:"key"`
				Label    string `json:"label"`
				Required bool   `json:"required"`
			} `json:"sign_in_settings"`
		} `json:"clouds"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/connection-clouds", nil, &forms)
	found := false
	for _, f := range forms.Clouds {
		if f.Cloud != connections.GCP {
			continue
		}
		found = true
		// Development on a loopback public URL can connect keylessly too.
		if strings.Join(f.Methods, ",") != "oauth,keyless,token" {
			t.Fatalf("gcp methods %v", f.Methods)
		}
		if len(f.SignInSettings) != 1 || f.SignInSettings[0].Key != "project_id" || f.SignInSettings[0].Label != "Project ID" || !f.SignInSettings[0].Required {
			t.Fatalf("gcp sign-in settings %+v", f.SignInSettings)
		}
	}
	if !found {
		t.Fatal("connection-clouds has no gcp")
	}

	var signIn connections.SignIn
	c.must(http.StatusOK, http.MethodPost, "/api/v1/connections/sign-ins",
		connections.SignInInput{Cloud: connections.GCP, Name: "Shop", Settings: map[string]string{"project_id": "shop-prod"}}, &signIn)
	u, err := url.Parse(signIn.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if !strings.HasPrefix(signIn.AuthorizeURL, google.URL+"/o/oauth2/v2/auth?") || q.Get("client_id") != clientID ||
		q.Get("redirect_uri") != callback || q.Get("state") == "" || q.Get("state") != signIn.State || q.Get("code_challenge") == "" {
		t.Fatalf("authorize url %s, state %q", signIn.AuthorizeURL, signIn.State)
	}

	var raw json.RawMessage
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections/sign-ins/complete",
		connections.CompleteInput{State: signIn.State, Code: "ok"}, &raw)
	var conn connections.Connection
	if err = json.Unmarshal(raw, &conn); err != nil {
		t.Fatal(err)
	}
	if conn.Method != connections.MethodOAuth || conn.Status != connections.StatusReady || conn.Account != "shop-prod" {
		t.Fatalf("connection %+v", conn)
	}
	if conn.SignedInBy == "" || conn.SignedInBy != conn.CreatedBy || conn.Details["signed_in_as"] != "ada@example.com" || conn.Details["project_id"] != "shop-prod" {
		t.Fatalf("who signed in: by %q, created by %q, details %v", conn.SignedInBy, conn.CreatedBy, conn.Details)
	}
	sent, _ := verifier.Load().(string)
	if sent == "" {
		t.Fatal("Google never got a PKCE verifier")
	}
	bodies := []string{string(raw), c.raw("/api/v1/connections/" + conn.ID), c.raw("/api/v1/connections"),
		c.raw("/api/v1/platform/cloud-sign-in/apps"), c.raw("/api/v1/connection-clouds")}
	for _, body := range bodies {
		for _, leak := range []string{"refresh_token", "client_secret", "access_token", firstToken, freshToken, refreshToken, clientSecret, sent} {
			if strings.Contains(body, leak) {
				t.Fatalf("a response contains %q: %s", leak, body)
			}
		}
	}
}

// A Vercel app's configured slug comes back in the app list and names the
// install page.
func TestCloudSignIn_vercelInstallUsesTheAppSlug(t *testing.T) {
	cfg := config.Defaults()
	vercel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("asked Vercel %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer vercel.Close()
	cfg.Database.URL = freshDatabase(t)
	cfg.Clouds.VercelEndpoint = vercel.URL
	cfg.Clouds.OAuth = map[string]config.OAuthApp{"vercel": {ClientID: "oac_app", ClientSecret: "vercel-app-secret", Slug: "loomplane-acme"}}
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)

	var list struct {
		Apps []connections.SignInApp `json:"apps"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/platform/cloud-sign-in/apps", nil, &list)
	slug := ""
	for _, a := range list.Apps {
		if a.Cloud == connections.Vercel {
			slug = a.Slug
		}
	}
	if slug != "loomplane-acme" {
		t.Fatalf("vercel slug %q in %+v", slug, list.Apps)
	}
	var signIn connections.SignIn
	c.must(http.StatusOK, http.MethodPost, "/api/v1/connections/sign-ins",
		connections.SignInInput{Cloud: connections.Vercel, Name: "Vercel"}, &signIn)
	if !strings.Contains(signIn.AuthorizeURL, "/integrations/loomplane-acme/new?") || signIn.State == "" ||
		!strings.Contains(signIn.AuthorizeURL, "state="+url.QueryEscape(signIn.State)) {
		t.Fatalf("install url %s, state %q", signIn.AuthorizeURL, signIn.State)
	}
}
