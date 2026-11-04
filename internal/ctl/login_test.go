package ctl

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/client"
)

const threeOrgs = `{"organizations":[
	{"id":"aorg_1","name":"Engines","slug":"engines"},
	{"id":"aorg_2","name":"Looms","slug":"looms"},
	{"id":"aorg_3","name":"Mills","slug":"mills"}]}`

func orgServer(t *testing.T, body string) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/cli/organizations" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return client.New(srv.URL, "tok")
}

func TestChooseOrganization(t *testing.T) {
	c := orgServer(t, threeOrgs)
	pick := func(in, want string) (client.Organization, string, error) {
		var out bytes.Buffer
		app := New(Options{Out: &out, In: strings.NewReader(in), Getenv: func(string) string { return "" }})
		o, err := app.chooseOrganization(context.Background(), c, want)
		return o, out.String(), err
	}

	for in, slug := range map[string]string{"2\n": "looms", "mills\n": "mills", "Engines": "engines"} {
		o, out, err := pick(in, "")
		if err != nil || o.Slug != slug {
			t.Fatalf("answering %q picked %+v, %v", in, o, err)
		}
		if !strings.Contains(out, "  2. Looms (looms)") {
			t.Fatalf("the list:\n%s", out)
		}
	}
	// --org asks nothing.
	if o, out, err := pick("", "aorg_3"); err != nil || o.Slug != "mills" || out != "" {
		t.Fatalf("--org by ID: %+v %q %v", o, out, err)
	}
	if _, _, err := pick("", "foundries"); err == nil || !strings.Contains(err.Error(), "engines, looms, mills") {
		t.Fatalf("an unknown --org: %v", err)
	}
	// Nobody to ask, as in a script: say how to name one.
	if _, _, err := pick("", ""); err == nil || !strings.Contains(err.Error(), "--org") {
		t.Fatalf("no answer: %v", err)
	}
	if _, _, err := pick("9\n", ""); err == nil || !strings.Contains(err.Error(), `"9" isn't one of them`) {
		t.Fatalf("out of range: %v", err)
	}

	one := orgServer(t, `{"organizations":[{"id":"aorg_1","name":"Engines","slug":"engines"}]}`)
	app := New(Options{Out: &bytes.Buffer{}, In: strings.NewReader(""), Getenv: func(string) string { return "" }})
	if o, err := app.chooseOrganization(context.Background(), one, ""); err != nil || o.Slug != "engines" {
		t.Fatalf("the only one: %+v %v", o, err)
	}
}

// refreshServer is a Loomplane whose token endpoint renews "ref-1".
func refreshServer(t *testing.T, refreshed *int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/public-config", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"auth_base_path":"/authsome"}`))
	})
	mux.HandleFunc("POST /authsome/v1/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != "ref-1" || r.PostForm.Get("client_id") != client.CLIClientID {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		*refreshed++
		_, _ = w.Write([]byte(`{"access_token":"tok-2","refresh_token":"ref-2","expires_in":3600}`))
	})
	mux.HandleFunc("GET /api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"user_id":"ausr_1","org_id":"aorg_1","permissions":[],"auth":"` + r.Header.Get("Authorization") + `"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestClientRenewsASignInAboutToExpire(t *testing.T) {
	refreshed := 0
	srv := refreshServer(t, &refreshed)
	path := filepath.Join(t.TempDir(), "loomctl.json")
	write := func(s Settings) {
		data, _ := json.Marshal(s)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{}
	app := New(Options{Out: &bytes.Buffer{}, ConfigPath: path, Getenv: func(k string) string { return env[k] }})

	// Plenty of time left: used as it is.
	write(Settings{URL: srv.URL, Key: "tok-1", RefreshToken: "ref-1", ExpiresAt: time.Now().Add(time.Hour)})
	if _, err := app.client(); err != nil || refreshed != 0 {
		t.Fatalf("refreshed %d times, %v", refreshed, err)
	}

	// About to run out: renewed, and the new tokens saved.
	write(Settings{URL: srv.URL, Key: "tok-1", RefreshToken: "ref-1", ExpiresAt: time.Now().Add(10 * time.Second)})
	if _, err := app.client(); err != nil || refreshed != 1 {
		t.Fatalf("refreshed %d times, %v", refreshed, err)
	}
	s, err := app.saved()
	if err != nil || s.Key != "tok-2" || s.RefreshToken != "ref-2" || time.Until(s.ExpiresAt) < 59*time.Minute {
		t.Fatalf("saved %+v, %v", s, err)
	}

	// The environment's key wins, and isn't anyone's to renew.
	write(Settings{URL: srv.URL, Key: "tok-1", RefreshToken: "ref-1", ExpiresAt: time.Now().Add(-time.Hour)})
	env[EnvKey] = "lp_live_ci"
	if _, err := app.client(); err != nil || refreshed != 1 {
		t.Fatalf("refreshed %d times with the environment's key, %v", refreshed, err)
	}

	// A refresh token the server no longer takes: log in again.
	delete(env, EnvKey)
	write(Settings{URL: srv.URL, Key: "tok-1", RefreshToken: "spent", ExpiresAt: time.Now().Add(-time.Hour)})
	if _, err := app.client(); err == nil || !strings.Contains(err.Error(), "run `loomctl login` again") {
		t.Fatalf("a spent refresh token: %v", err)
	}
}

func TestLoginWithAKeyForgetsASignIn(t *testing.T) {
	refreshed := 0
	srv := refreshServer(t, &refreshed)
	path := filepath.Join(t.TempDir(), "loomctl.json")
	app := New(Options{Out: &bytes.Buffer{}, ConfigPath: path, Getenv: func(string) string { return "" }})
	if err := app.save(Settings{URL: srv.URL, Key: "tok-1", RefreshToken: "ref-1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run([]string{"login", "--url", srv.URL, "--key", "lp_live_abc"}); err != nil {
		t.Fatal(err)
	}
	s, err := app.saved()
	if err != nil || s.Key != "lp_live_abc" || s.RefreshToken != "" || !s.ExpiresAt.IsZero() {
		t.Fatalf("saved %+v, %v", s, err)
	}
}
