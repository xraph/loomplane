//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

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
