//go:build integration

package integration

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/platform"
)

const installationToken = "ghs_private_repo"

// fakePrivateGitHub serves acme/private only to an installation token, and
// plays the App side of connecting installation 42.
func fakePrivateGitHub(t *testing.T, head *atomic.Pointer[string]) *httptest.Server {
	t.Helper()
	tgz := exampleTarball(t)
	pushedTarball := tarball(t, "acme-private-pushed", siteFiles)
	token := func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer "+installationToken }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /app/installations/42", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "account": map[string]string{"login": "acme", "type": "Organization"}})
	})
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("code") != "from-github" {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "ghu_installer"})
	})
	mux.HandleFunc("GET /user/installations", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"installations": []map[string]int{{"id": 42}}})
	})
	mux.HandleFunc("POST /app/installations/42/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"token": installationToken})
	})
	mux.HandleFunc("GET /repos/acme/private", func(w http.ResponseWriter, r *http.Request) {
		if !token(r) {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"default_branch":"main"}`))
	})
	mux.HandleFunc("GET /repos/acme/private/commits/main", func(w http.ResponseWriter, r *http.Request) {
		if !token(r) {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(*head.Load()))
	})
	mux.HandleFunc("GET /repos/acme/private/tarball/{sha}", func(w http.ResponseWriter, r *http.Request) {
		if !token(r) {
			http.NotFound(w, r)
			return
		}
		if sha := r.PathValue("sha"); sha != exampleSHA && sha != *head.Load() {
			http.NotFound(w, r)
			return
		}
		if r.PathValue("sha") == exampleSHA {
			_, _ = w.Write(tgz)
		} else {
			_, _ = w.Write(pushedTarball)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGitHub_aConnectedInstallationReadsPrivateRepositories(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var head atomic.Pointer[string]
	initialSHA := exampleSHA
	head.Store(&initialSHA)
	gh := fakePrivateGitHub(t, &head)
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.GitHub = config.GitHubConfig{
		APIURL: gh.URL, WebURL: gh.URL, AppID: 99, AppSlug: "loomplane-it",
		PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
		ClientID:   "Iv1.it", ClientSecret: "shh",
		WebhookSecret: "github-push-integration-secret",
	}
	r := start(t, cfg)
	defer r.stop()
	worker, err := platform.NewWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Start(t.Context()); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	defer func() {
		if serr := worker.Stop(t.Context()); serr != nil {
			t.Errorf("stop worker: %v", serr)
		}
	}()

	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "ada@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Acme", "slug": "acme"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)

	var status struct {
		Configured    bool   `json:"configured"`
		InstallURL    string `json:"install_url"`
		Installations []struct {
			ID      string `json:"id"`
			Account string `json:"account"`
		} `json:"installations"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/github", nil, &status)
	if !status.Configured || status.InstallURL == "" || len(status.Installations) != 0 {
		t.Fatalf("status %+v", status)
	}
	u, err := url.Parse(status.InstallURL)
	if err != nil {
		t.Fatal(err)
	}
	state := u.Query().Get("state")

	// GitHub's redirect comes back with these; a wrong code proves nothing.
	c.must(http.StatusForbidden, http.MethodPost, "/api/v1/github/installations", map[string]any{"installation_id": 42, "code": "guessed", "state": state}, nil)
	c.must(http.StatusBadRequest, http.MethodPost, "/api/v1/github/installations", map[string]any{"installation_id": 42, "code": "from-github", "state": "forged"}, nil)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/github/installations", map[string]any{"installation_id": 42, "code": "from-github", "state": state}, nil)
	c.must(http.StatusOK, http.MethodGet, "/api/v1/github", nil, &status)
	if len(status.Installations) != 1 || status.Installations[0].ID != "42" || status.Installations[0].Account != "acme" {
		t.Fatalf("installations %+v", status.Installations)
	}

	var project struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Private", "repository_url": "https://github.com/acme/private"}, &project)
	waitFor := func(a discovery.Analysis) discovery.Analysis {
		t.Helper()
		deadline := time.Now().Add(readyTimeout)
		for a.Status != "succeeded" && a.Status != "failed" {
			if time.Now().After(deadline) {
				t.Fatalf("analysis still %s", a.Status)
			}
			time.Sleep(100 * time.Millisecond)
			c.must(http.StatusOK, http.MethodGet, "/api/v1/analyses/"+a.ID, nil, &a)
		}
		return a
	}
	analyze := func() discovery.Analysis {
		t.Helper()
		var queued struct {
			Analyses []discovery.Analysis `json:"analyses"`
		}
		c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+project.ID+"/analyses", nil, &queued)
		if len(queued.Analyses) != 1 {
			t.Fatalf("queued %+v", queued)
		}
		return waitFor(queued.Analyses[0])
	}
	first := analyze()
	if first.Status != "succeeded" || first.Summary.Services != 7 || first.CommitSHA != exampleSHA {
		t.Fatalf("analysis of the private repository %+v", first)
	}

	// Only a signed push starts this second analysis. No manual Analyze
	// request is made after the private repository's default branch moves.
	pushedSHA := "0123456789abcdef0123456789abcdef01234567"
	head.Store(&pushedSHA)
	body, err := json.Marshal(map[string]any{
		"ref": "refs/heads/main", "after": pushedSHA,
		"repository":   map[string]any{"full_name": "acme/private", "default_branch": "main", "private": true},
		"installation": map[string]int{"id": 42},
	})
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(cfg.GitHub.WebhookSecret))
	mac.Write(body)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, r.srv.URL+"/api/v1/github/webhook", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var delivery struct {
		Started int `json:"analyses_started"`
	}
	if err = json.NewDecoder(res.Body).Decode(&delivery); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusAccepted || delivery.Started != 1 {
		t.Fatalf("push delivery: status %d, started %d", res.StatusCode, delivery.Started)
	}
	var list struct {
		Analyses []discovery.Analysis `json:"analyses"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project.ID+"/analyses", nil, &list)
	if len(list.Analyses) != 2 || list.Analyses[0].CreatedBy != "github-push" {
		t.Fatalf("push did not create a distinct automatic analysis: %+v", list)
	}
	automatic := waitFor(list.Analyses[0])
	if automatic.Status != "succeeded" || automatic.CommitSHA != pushedSHA || automatic.Summary.Services != 1 {
		t.Fatalf("automatic private analysis: %+v", automatic)
	}
	var view discovery.ProjectView
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project.ID+"/graph", nil, &view)
	if view.Graph == nil || len(view.Graph.Sources) != 1 || view.Graph.Sources[0].AnalysisID != automatic.ID || view.Graph.Sources[0].CommitSHA != pushedSHA {
		t.Fatalf("project graph still uses the previous analysis: %+v", view.Graph)
	}
	if len(view.Graph.Services) != 1 || view.Graph.Services[0].Name != "site" {
		t.Fatalf("push did not replace the previous graph's services: %+v", view.Graph.Services)
	}
	var previous discovery.Analysis
	c.must(http.StatusOK, http.MethodGet, "/api/v1/analyses/"+first.ID, nil, &previous)
	if previous.CommitSHA != exampleSHA || previous.Status != "succeeded" {
		t.Fatalf("push rewrote the earlier analysis: %+v", previous)
	}
	var historical struct {
		Graph graph.Graph `json:"graph"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/analyses/"+first.ID+"/graph", nil, &historical)
	if len(historical.Graph.Services) != 7 {
		t.Fatalf("push changed the historical graph: %+v", historical.Graph.Services)
	}

	// Disconnected, the organization can't read it any more.
	c.must(http.StatusNoContent, http.MethodDelete, "/api/v1/github/installations/42", nil, nil)
	if a := analyze(); a.Status != "failed" {
		t.Fatalf("analysis after disconnecting %+v", a)
	}
}
