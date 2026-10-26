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

// An organization sets up its own DigitalOcean app, signs in with it, and
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
		default:
			t.Errorf("asked DigitalOcean %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer do.Close()
	cfg.Database.URL = freshDatabase(t)
	cfg.Clouds.DigitalOceanEndpoint = do.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}

	c.must(http.StatusUnauthorized, http.MethodGet, "/api/v1/cloud-sign-in/apps", nil, nil)
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
		c.must(http.StatusOK, http.MethodGet, "/api/v1/cloud-sign-in/apps", nil, &list)
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

	if got := source(); got != connections.AppFromNone {
		t.Fatalf("source before setting an app: %q", got)
	}
	if got := methods(); strings.Join(got, ",") != "token" {
		t.Fatalf("methods without an app: %v", got)
	}
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/connections/sign-ins",
		connections.SignInInput{Cloud: connections.DigitalOcean, Name: "DigitalOcean"}, nil)
	c.must(http.StatusNotFound, http.MethodDelete, "/api/v1/cloud-sign-in/apps/digitalocean", nil, nil)
	c.must(http.StatusUnprocessableEntity, http.MethodPut, "/api/v1/cloud-sign-in/apps/digitalocean",
		connections.AppInput{ClientID: clientID}, nil)

	var set json.RawMessage
	c.must(http.StatusOK, http.MethodPut, "/api/v1/cloud-sign-in/apps/digitalocean",
		connections.AppInput{ClientID: clientID, ClientSecret: clientSecret}, &set)
	if strings.Contains(string(set), clientSecret) {
		t.Fatalf("setting an app returned its secret: %s", set)
	}
	if got := source(); got != connections.AppFromOrganization {
		t.Fatalf("source after setting an app: %q", got)
	}
	if body := c.raw("/api/v1/cloud-sign-in/apps"); strings.Contains(body, clientSecret) {
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

	c.must(http.StatusNoContent, http.MethodDelete, "/api/v1/cloud-sign-in/apps/digitalocean", nil, nil)
	if got := source(); got != connections.AppFromNone {
		t.Fatalf("source after deleting the app: %q", got)
	}
	c.must(http.StatusUnprocessableEntity, http.MethodPut, "/api/v1/cloud-sign-in/apps/nowhere",
		connections.AppInput{ClientID: clientID, ClientSecret: clientSecret}, nil)
}

// An organization sets up its own Google app, signs in to a project it
// names first, and gets a ready connection that says who signed in. No
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
			_ = json.NewEncoder(w).Encode(map[string]any{"permissions": gcp.Permissions})
		default:
			t.Errorf("asked Google %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer google.Close()
	cfg.Database.URL = freshDatabase(t)
	cfg.Clouds.GoogleEndpoint = google.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)

	var set json.RawMessage
	c.must(http.StatusOK, http.MethodPut, "/api/v1/cloud-sign-in/apps/gcp",
		connections.AppInput{ClientID: clientID, ClientSecret: clientSecret}, &set)
	if strings.Contains(string(set), clientSecret) {
		t.Fatalf("setting an app returned its secret: %s", set)
	}

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
		if strings.Join(f.Methods, ",") != "oauth,token" {
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
	bodies := []string{string(set), string(raw), c.raw("/api/v1/connections/" + conn.ID), c.raw("/api/v1/connections"),
		c.raw("/api/v1/cloud-sign-in/apps"), c.raw("/api/v1/connection-clouds")}
	for _, body := range bodies {
		for _, leak := range []string{"refresh_token", "client_secret", "access_token", firstToken, freshToken, refreshToken, clientSecret, sent} {
			if strings.Contains(body, leak) {
				t.Fatalf("a response contains %q: %s", leak, body)
			}
		}
	}
}

// A Vercel app is installed by its slug, so one without is refused, and
// the slug saved comes back and names the install page.
func TestCloudSignIn_vercelAppNeedsASlug(t *testing.T) {
	cfg := config.Defaults()
	vercel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("asked Vercel %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer vercel.Close()
	cfg.Database.URL = freshDatabase(t)
	cfg.Clouds.VercelEndpoint = vercel.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)

	c.must(http.StatusUnprocessableEntity, http.MethodPut, "/api/v1/cloud-sign-in/apps/vercel",
		connections.AppInput{ClientID: "oac_app", ClientSecret: "vercel-app-secret"}, nil)
	var set connections.SignInApp
	c.must(http.StatusOK, http.MethodPut, "/api/v1/cloud-sign-in/apps/vercel",
		connections.AppInput{ClientID: "oac_app", ClientSecret: "vercel-app-secret", Slug: "loomplane-acme"}, &set)
	if set.Slug != "loomplane-acme" || set.Source != connections.AppFromOrganization {
		t.Fatalf("set %+v", set)
	}
	var list struct {
		Apps []connections.SignInApp `json:"apps"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/cloud-sign-in/apps", nil, &list)
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
