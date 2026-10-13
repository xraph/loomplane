// Command github-app creates Loomplane's GitHub App with GitHub's App
// Manifest flow and writes its settings where Loomplane reads them, so
// nobody copies a key or a secret by hand.
//
//	go run ./scripts/github-app -org xraph
//
// It serves a page that hands GitHub the App's settings, the ones
// docs/github-app.md lists. You confirm on GitHub, which sends you back
// here with a one-time code, and the code turns into the App's ID, slug,
// client ID, client secret and private key. The key goes in a file only
// you can read, and the rest in a github block in config.local.yaml,
// which git ignores.
//
// A webhook can't reach localhost, so it's off unless you pass -webhook,
// for a Loomplane GitHub can reach.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "github-app:", err)
		os.Exit(1)
	}
}

type options struct {
	org, name, publicURL, webURL, apiURL, listen, config, key string
	public, webhook                                           bool
}

func run() error {
	var o options
	flag.StringVar(&o.org, "org", "", "the GitHub organization that owns the App; empty for your own account")
	flag.StringVar(&o.name, "name", "Loomplane Dev", "the App's name, unique across GitHub")
	flag.StringVar(&o.publicURL, "url", "http://localhost:8780", "Loomplane's public URL")
	flag.StringVar(&o.webURL, "github", "https://github.com", "GitHub's web address")
	flag.StringVar(&o.apiURL, "api", "https://api.github.com", "GitHub's API")
	flag.StringVar(&o.listen, "listen", "127.0.0.1:8781", "where this command waits for GitHub to send you back")
	flag.StringVar(&o.config, "config", "config.local.yaml", "the local config the App's settings are added to")
	flag.StringVar(&o.key, "key", ".loomplane/github-app.pem", "where the App's private key is written")
	flag.BoolVar(&o.public, "public", true, "let any GitHub account install the App, not only its owner")
	flag.BoolVar(&o.webhook, "webhook", false, "turn the push webhook on; GitHub must be able to reach -url")
	flag.Parse()
	o.publicURL = strings.TrimRight(o.publicURL, "/")
	o.webURL = strings.TrimRight(o.webURL, "/")
	o.apiURL = strings.TrimRight(o.apiURL, "/")

	if has, err := hasGitHubBlock(o.config); err != nil || has {
		if err == nil {
			err = fmt.Errorf("%s already has a github block; remove it to create another App", o.config)
		}
		return err
	}
	state, err := token()
	if err != nil {
		return err
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", o.listen)
	if err != nil {
		return err
	}
	back := "http://" + ln.Addr().String()
	manifest, err := json.Marshal(appManifest(o, back+"/created"))
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = page.Execute(w, map[string]string{ //nolint:errcheck // the browser sees what it got
			"Action": newAppURL(o) + "?state=" + url.QueryEscape(state), "Manifest": string(manifest), "Owner": owner(o),
		})
	})
	mux.HandleFunc("GET /created", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "That isn't the App this command asked GitHub for.", http.StatusBadRequest)
			return
		}
		app, cerr := convert(r.Context(), o.apiURL, r.URL.Query().Get("code"))
		if cerr == nil {
			cerr = save(o, app)
		}
		if cerr != nil {
			http.Error(w, cerr.Error(), http.StatusBadGateway)
			done <- cerr
			return
		}
		fmt.Fprintf(w, "Created %s. Its settings are in %s. Restart Loomplane, then connect an account under Integrations. You can close this tab.\n", app.HTMLURL, o.config)
		done <- nil
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }() //nolint:errcheck // Shutdown ends it

	fmt.Printf("Open %s/ to create the App on %s.\n", back, owner(o))
	err = <-done
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx) //nolint:errcheck // the result's already decided
	if err != nil {
		return err
	}
	fmt.Printf("Done. %s has the App's settings and %s its key.\n", o.config, o.key)
	return nil
}

// appManifest is the App docs/github-app.md describes: it reads contents
// and nothing else, and asks the installer to sign in during installation
// so Loomplane can check they see the installation they claim.
func appManifest(o options, redirect string) map[string]any {
	m := map[string]any{
		"name":                     o.name,
		"url":                      o.publicURL,
		"redirect_url":             redirect,
		"callback_urls":            []string{o.publicURL + "/integrations/github/setup"},
		"request_oauth_on_install": true,
		"public":                   o.public,
		"default_permissions":      map[string]string{"contents": "read", "metadata": "read"},
		"hook_attributes":          map[string]any{"url": o.publicURL + "/api/v1/github/webhook", "active": o.webhook},
	}
	if o.webhook {
		m["default_events"] = []string{"push"}
	}
	return m
}

func newAppURL(o options) string {
	if o.org == "" {
		return o.webURL + "/settings/apps/new"
	}
	return o.webURL + "/organizations/" + url.PathEscape(o.org) + "/settings/apps/new"
}

func owner(o options) string {
	if o.org == "" {
		return "your GitHub account"
	}
	return "the " + o.org + " organization"
}

// createdApp is what GitHub hands back for a manifest's code.
type createdApp struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	HTMLURL       string `json:"html_url"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	WebhookSecret string `json:"webhook_secret"`
	PEM           string `json:"pem"`
}

// convert turns the one-time code GitHub sent back into the App. The code
// is the only credential the call needs, and it works once, within the
// hour.
func convert(ctx context.Context, apiURL, code string) (*createdApp, error) {
	if code == "" {
		return nil, errors.New("GitHub sent no code back")
	}
	// apiURL is this command's own flag, and the code is escaped into
	// GitHub's path.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"/app-manifests/"+url.PathEscape(code)+"/conversions", http.NoBody) //nolint:gosec // G704: see above
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := http.DefaultClient.Do(req) //nolint:gosec // G704: see above
	if err != nil {
		return nil, err
	}
	defer res.Body.Close() // read-only body
	if res.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("GitHub answered %s to the App's code", res.Status)
	}
	var app createdApp
	if err := json.NewDecoder(res.Body).Decode(&app); err != nil {
		return nil, err
	}
	if app.ID == 0 || app.Slug == "" || app.PEM == "" || app.ClientID == "" || app.ClientSecret == "" {
		return nil, errors.New("GitHub's answer is missing some of the App's settings")
	}
	return &app, nil
}

// save writes the key where only you can read it, then adds the App's
// settings to the local config.
func save(o options, app *createdApp) error {
	key, aerr := filepath.Abs(o.key)
	if aerr != nil {
		return aerr
	}
	if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		return err
	}
	// The path is this command's own flag.
	fkey, err := os.OpenFile(key, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = fkey.WriteString(app.PEM); err != nil {
		_ = fkey.Close()
		return err
	}
	if closeErr := fkey.Close(); closeErr != nil {
		return closeErr
	}
	block := fmt.Sprintf("\n# The GitHub App %s, created by scripts/github-app.\ngithub:\n  app_id: %q\n  app_slug: %q\n  client_id: %q\n  client_secret: %q\n  private_key_file: %q\n",
		app.HTMLURL, fmt.Sprint(app.ID), app.Slug, app.ClientID, app.ClientSecret, key)
	if o.webhook && app.WebhookSecret != "" {
		block += fmt.Sprintf("  webhook_secret: %q\n", app.WebhookSecret)
	}
	f, err := os.OpenFile(o.config, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err = f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.WriteString(block); err != nil {
		_ = f.Close() // the write's error is the one to report
		return err
	}
	return f.Close()
}

// hasGitHubBlock reports whether the config already sets the github key,
// which a second block would collide with.
func hasGitHubBlock(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var cfg map[string]any
	if err = yaml.Unmarshal(data, &cfg); err != nil {
		return false, fmt.Errorf("read local config: %w", err)
	}
	_, has := cfg["github"]
	return has, nil
}

func token() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// page posts the manifest to GitHub, which is the only way GitHub takes
// one: a form submitted from the browser of someone signed in to it.
var page = template.Must(template.New("page").Parse(`<!doctype html>
<meta charset="utf-8">
<title>Create Loomplane's GitHub App</title>
<style>body{font:15px/1.5 system-ui,sans-serif;max-width:34rem;margin:4rem auto;padding:0 1rem}button{font:inherit;padding:.5rem 1rem;border-radius:999px}</style>
<h1>Create Loomplane's GitHub App</h1>
<p>GitHub will show you the App's settings for {{.Owner}}: it reads repository contents and nothing else. Check them and choose Create GitHub App, and GitHub sends you back here.</p>
<form method="post" action="{{.Action}}">
<input type="hidden" name="manifest" value="{{.Manifest}}">
<button type="submit">Continue to GitHub</button>
</form>
`))
