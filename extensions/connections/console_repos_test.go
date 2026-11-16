package connections

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge/extensions/dashboard/contract"

	"github.com/xraph/loomplane/internal/repositories"
)

// memRepos is an in-memory repositoryApps.
type memRepos struct {
	apps map[string]repositories.PlatformApp
}

func (m *memRepos) Fresh(context.Context) (map[string]repositories.PlatformApp, error) {
	return maps.Clone(m.apps), nil
}

func (m *memRepos) Save(_ context.Context, key string, a repositories.PlatformApp) error {
	m.apps[key] = a
	return nil
}

func (m *memRepos) Remove(_ context.Context, key string) error {
	delete(m.apps, key)
	return nil
}

type repoAudit struct{ events []string }

func (a *repoAudit) RecordRepositoryApp(_ context.Context, action, userID, key, clientID string) {
	a.events = append(a.events, strings.Join([]string{action, userID, key, clientID}, " "))
}

func reposConsole(t *testing.T) (*console, *memRepos, *repoAudit) {
	t.Helper()
	c, _ := testConsole()
	repos := &memRepos{apps: map[string]repositories.PlatformApp{}}
	audit := &repoAudit{}
	c.repos, c.audit = repos, audit
	c.now = func() time.Time { return time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC) }
	return c, repos, audit
}

func testKey(t *testing.T) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
}

func integrationsByKey(t *testing.T, c *console) map[string]ConsoleIntegration {
	t.Helper()
	got, err := c.integrations(t.Context(), struct{}{}, operator)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]ConsoleIntegration{}
	for _, it := range got.Integrations {
		out[it.Key] = it
	}
	return out
}

func TestRepositoryAppsAreEditableWhereConfigDoesntSetThem(t *testing.T) {
	c, repos, _ := reposConsole(t)
	repos.apps[keyBitbucket] = repositories.PlatformApp{
		ClientID: "bb", ClientSecret: "s", UpdatedBy: "usr_1", UpdatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
	}
	by := integrationsByKey(t, c)
	if gh := by[keyGitHub]; !gh.Editable || gh.Source != SourceNone || len(gh.Provider.Fields) != 4 {
		t.Fatalf("github %+v", gh)
	}
	// gitlab.com is set in configuration in testConsole, so it wins.
	if gl := by[keyGitLab]; gl.Editable || gl.Source != SourceConfig || gl.ClientID != "gl-app" {
		t.Fatalf("gitlab %+v", gl)
	}
	if h := by["gitlab-host:git.example.com"]; h.Editable {
		t.Fatalf("a self-managed GitLab from config %+v", h)
	}
	b := by[keyBitbucket]
	if !b.Editable || b.Source != SourcePlatform || b.ClientID != "bb" || b.UpdatedBy != "first@example.com" ||
		!slices.Equal(b.SavedSecrets, []string{secretClient}) || b.UpdatedAt == nil {
		t.Fatalf("bitbucket %+v", b)
	}
	if raw, _ := json.Marshal(b); strings.Contains(string(raw), `"s"`) {
		t.Fatalf("a secret on the wire: %s", raw)
	}
}

func TestSaveRepositoryApp(t *testing.T) {
	c, repos, audit := reposConsole(t)
	if _, err := c.save(t.Context(), SaveInput{Key: "bitbucket", ClientID: " bb ", ClientSecret: " s1 "}, operator); err != nil {
		t.Fatal(err)
	}
	if a := repos.apps[keyBitbucket]; a.ClientID != "bb" || a.ClientSecret != "s1" || a.UpdatedBy != "usr_ops" {
		t.Fatalf("saved %v", a)
	}
	// A blank secret keeps the saved one for the same client only.
	if _, err := c.save(t.Context(), SaveInput{Key: "bitbucket", ClientID: "bb"}, operator); err != nil || repos.apps[keyBitbucket].ClientSecret != "s1" {
		t.Fatalf("keep the secret: %v %v", err, repos.apps[keyBitbucket])
	}
	if _, err := c.save(t.Context(), SaveInput{Key: "bitbucket", ClientID: "other"}, operator); codeOf(err) != contract.CodeBadRequest {
		t.Fatalf("a new client without a secret: %v", err)
	}
	if !slices.Equal(audit.events, []string{"repository_app.set usr_ops bitbucket bb", "repository_app.set usr_ops bitbucket bb"}) {
		t.Fatalf("audit %v", audit.events)
	}
	if _, err := c.remove(t.Context(), keyInput{Key: "bitbucket"}, operator); err != nil {
		t.Fatal(err)
	}
	if _, ok := repos.apps[keyBitbucket]; ok || audit.events[2] != "repository_app.delete usr_ops bitbucket bb" {
		t.Fatalf("removed? %v %v", repos.apps, audit.events)
	}
	if _, err := c.remove(t.Context(), keyInput{Key: "bitbucket"}, operator); codeOf(err) != contract.CodeNotFound {
		t.Fatalf("remove twice: %v", err)
	}
}

func TestSaveRepositoryAppRespectsConfiguration(t *testing.T) {
	c, repos, _ := reposConsole(t)
	if _, err := c.save(t.Context(), SaveInput{Key: "gitlab", ClientID: "x", ClientSecret: "y"}, operator); codeOf(err) != contract.CodeConflict {
		t.Fatalf("gitlab.com from config: %v", err)
	}
	if _, err := c.save(t.Context(), SaveInput{Key: "gitlab-host:git.example.com", ClientID: "x", ClientSecret: "y"}, operator); codeOf(err) != contract.CodeConflict {
		t.Fatalf("self-managed gitlab: %v", err)
	}
	if len(repos.apps) != 0 {
		t.Fatalf("saved %v", repos.apps)
	}
}

func TestSaveGitHubApp(t *testing.T) {
	c, repos, _ := reposConsole(t)
	key := testKey(t)
	base := SaveInput{Key: "github", ClientID: "Iv1.a", ClientSecret: "cs", AppID: "42", Slug: "lp", PrivateKey: key, WebhookSecret: "wh"}
	for name, in := range map[string]SaveInput{
		"no app id":      func() SaveInput { b := base; b.AppID = "x"; return b }(),
		"no slug":        func() SaveInput { b := base; b.Slug = ""; return b }(),
		"no key":         func() SaveInput { b := base; b.PrivateKey = ""; return b }(),
		"not a key":      func() SaveInput { b := base; b.PrivateKey = "nope"; return b }(),
		"no client id":   func() SaveInput { b := base; b.ClientID = ""; return b }(),
		"no secret, new": func() SaveInput { b := base; b.ClientSecret = ""; return b }(),
	} {
		if _, err := c.save(t.Context(), in, operator); codeOf(err) != contract.CodeBadRequest {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := c.save(t.Context(), base, operator); err != nil {
		t.Fatal(err)
	}
	saved := repos.apps[keyGitHub]
	if saved.AppID != 42 || saved.Slug != "lp" || saved.PrivateKey == "" || saved.WebhookSecret != "wh" {
		t.Fatalf("saved %v", saved)
	}
	// Blank secrets keep the saved ones while the App and client are the
	// same; clearing the webhook secret turns webhooks off.
	again := SaveInput{Key: "github", ClientID: "Iv1.a", AppID: "42", Slug: "lp", ClearWebhookSecret: true}
	if _, err := c.save(t.Context(), again, operator); err != nil {
		t.Fatal(err)
	}
	if a := repos.apps[keyGitHub]; a.PrivateKey != saved.PrivateKey || a.ClientSecret != "cs" || a.WebhookSecret != "" {
		t.Fatalf("kept %v", a)
	}
	// Another App never borrows the saved key.
	other := again
	other.AppID = "43"
	if _, err := c.save(t.Context(), other, operator); codeOf(err) != contract.CodeBadRequest {
		t.Fatalf("a new App without its key: %v", err)
	}
	by := integrationsByKey(t, c)
	if gh := by[keyGitHub]; gh.AppID != 42 || gh.Slug != "lp" || !slices.Equal(gh.SavedSecrets, []string{secretClient, secretKey}) {
		t.Fatalf("listed %+v", gh)
	}
}

// fakeHost answers token requests as GitLab, Bitbucket and GitHub do: a
// client it knows gets the code refused, any other is refused itself. It
// also answers GitHub's /app for any bearer JWT.
func fakeHost(t *testing.T, seen *http.Request) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		*seen = *r.Clone(context.Background())
		seen.Form = r.Form
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app" {
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"slug":"lp"}`))
			return
		}
		id, secret, basic := r.BasicAuth()
		if !basic {
			id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
		}
		good := id == "good" && secret == "s"
		switch r.URL.Path {
		case "/login/oauth/access_token": // GitHub answers 200 either way
			if good {
				_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`))
			} else {
				_, _ = w.Write([]byte(`{"error":"incorrect_client_credentials"}`))
			}
		default:
			if good {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"bad code"}`))
			} else {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"Client authentication failed for ` + secret + `"}`))
			}
		}
	}))
}

func TestCheckRepositoryApps(t *testing.T) {
	var seen http.Request
	srv := fakeHost(t, &seen)
	defer srv.Close()
	c, repos, _ := reposConsole(t)
	c.cfg.GitLab.APIURL, c.cfg.Bitbucket.WebURL = srv.URL, srv.URL
	c.cfg.GitHub.WebURL, c.cfg.GitHub.APIURL = srv.URL, srv.URL
	c.cfg.GitLab.Hosts[0].WebURL, c.cfg.GitLab.Hosts[0].ClientSecret = srv.URL, "s"
	c.cfg.GitLab.Hosts[0].ClientID = "good"

	// gitlab.com from config, checked with its callback URL.
	c.cfg.GitLab.ClientID, c.cfg.GitLab.ClientSecret = "good", "s"
	got, err := c.check(t.Context(), CheckInput{Key: "gitlab"}, operator)
	if err != nil || got.Result != repositories.ClientAccepted || got.CheckedAt.IsZero() {
		t.Fatalf("gitlab: %+v %v", got, err)
	}
	if seen.URL.Path != "/oauth/token" || seen.Form.Get("redirect_uri") != "https://lp.example/integrations/gitlab/callback" {
		t.Fatalf("gitlab asked %s with %v", seen.URL.Path, seen.Form)
	}

	// A self-managed GitLab from config, by its key.
	got, err = c.check(t.Context(), CheckInput{Key: gitLabHostKey(srv.URL)}, operator)
	if err != nil || got.Result != repositories.ClientAccepted {
		t.Fatalf("self-managed: %+v %v", got, err)
	}

	// Bitbucket's typed values, sent as a Basic header; a wrong secret is
	// rejected and kept out of the answer.
	got, err = c.check(t.Context(), CheckInput{Key: "bitbucket", ClientID: "good", ClientSecret: "wrong-secret"}, operator)
	if err != nil || got.Result != repositories.ClientRejected || strings.Contains(got.Detail, "wrong-secret") {
		t.Fatalf("bitbucket: %+v %v", got, err)
	}
	if _, _, basic := seen.BasicAuth(); !basic || seen.URL.Path != "/site/oauth2/access_token" {
		t.Fatalf("bitbucket asked %s, basic %v", seen.URL.Path, basic)
	}

	// The saved GitHub App: its client, and its App ID and key with /app.
	repos.apps[keyGitHub] = repositories.PlatformApp{ClientID: "good", ClientSecret: "s", AppID: 7, Slug: "lp", PrivateKey: testKey(t)}
	got, err = c.check(t.Context(), CheckInput{Key: "github"}, operator)
	if err != nil || got.Result != repositories.ClientAccepted || !strings.Contains(got.Summary, "App ID and private key") {
		t.Fatalf("github: %+v %v", got, err)
	}
	repos.apps[keyGitHub] = repositories.PlatformApp{ClientID: "good", ClientSecret: "s", AppID: 7, Slug: "other", PrivateKey: repos.apps[keyGitHub].PrivateKey}
	if got, _ := c.check(t.Context(), CheckInput{Key: "github"}, operator); got.Result != repositories.ClientRejected {
		t.Fatalf("github slug mismatch: %+v", got)
	}

	// Nothing to check, and other values for a config app.
	delete(repos.apps, keyGitHub)
	if _, err := c.check(t.Context(), CheckInput{Key: "github"}, operator); codeOf(err) != contract.CodeBadRequest {
		t.Fatalf("no app: %v", err)
	}
	if _, err := c.check(t.Context(), CheckInput{Key: "gitlab", ClientID: "x", ClientSecret: "y"}, operator); codeOf(err) != contract.CodeConflict {
		t.Fatalf("other values for config: %v", err)
	}
}
