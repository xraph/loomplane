//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/platform"
)

// fakeBitbucketRepos serves each tarball as the repository its key names
// ("workspace/repo"), on a trunk branch at exampleSHA, the way Bitbucket
// Cloud serves a public repository. API calls and archives share one
// address, since the fake needs no separate web host.
func fakeBitbucketRepos(t *testing.T, repos map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, tgz := range repos {
			switch r.URL.Path {
			case "/repositories/" + name:
				_, _ = w.Write([]byte(`{"mainbranch":{"name":"trunk"}}`))
				return
			case "/repositories/" + name + "/refs/branches/trunk":
				_, _ = w.Write([]byte(`{"target":{"hash":"` + exampleSHA + `"}}`))
				return
			case "/" + name + "/get/" + exampleSHA + ".tar.gz":
				_, _ = w.Write(tgz)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBitbucket_aPublicRepositoryAnalyzesWithoutAConnection(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	bb := fakeBitbucketRepos(t, map[string][]byte{"acme/site": tarball(t, "acme-site-fedcba9", siteFiles)})
	cfg.Bitbucket.APIURL = bb.URL
	cfg.Bitbucket.WebURL = bb.URL
	cfg.Analysis.WorkDir = t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)

	var proj struct {
		ID           string   `json:"id"`
		Repositories []string `json:"repositories"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Site", "repository_url": "https://bitbucket.org/acme/site"}, &proj)
	if !slices.Equal(proj.Repositories, []string{"bitbucket.org/acme/site"}) {
		t.Fatalf("new project's repositories %v", proj.Repositories)
	}

	var list struct {
		Sources []sourceBody `json:"sources"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+proj.ID+"/sources", nil, &list)
	if len(list.Sources) != 1 || list.Sources[0].Repository != "bitbucket.org/acme/site" {
		t.Fatalf("sources %+v", list.Sources)
	}

	analyses := analyzeAll(t, cfg, c, proj.ID)
	if len(analyses) != 1 || analyses[0].Status != "succeeded" || analyses[0].CommitSHA != exampleSHA || analyses[0].Summary.Services == 0 {
		t.Fatalf("analyses %+v", analyses)
	}
}

// The fake Bitbucket's tokens. The refreshed pair is what the worker holds
// after spending the stored refresh token.
const (
	bbAccessToken     = "bb-access-first"
	bbRefreshToken    = "bb-refresh-first"
	bbAccessRefreshed = "bb-access-renewed"
	bbRefreshRotated  = "bb-refresh-rotated"
	bbConsumerKey     = "bb-consumer-key"
	bbConsumerSecret  = "bb-consumer-secret"
)

// fakePrivateBitbucket plays Bitbucket Cloud for an OAuth consumer: it
// signs the person in, says who they are and which workspace they belong
// to, and serves acme/api to a bearer token only. API calls, the token
// endpoint and archives share one address. extra adds routes, each behind
// the same bearer check.
func fakePrivateBitbucket(t *testing.T, extra ...func(mux *http.ServeMux, guarded func(http.HandlerFunc) http.HandlerFunc)) *httptest.Server {
	t.Helper()
	tgz := tarball(t, "acme-api-fedcba9", siteFiles)
	bearer := func(r *http.Request) bool {
		got := r.Header.Get("Authorization")
		return got == "Bearer "+bbAccessToken || got == "Bearer "+bbAccessRefreshed
	}
	guarded := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !bearer(r) {
				http.NotFound(w, r)
				return
			}
			h(w, r)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /site/oauth2/access_token", func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != bbConsumerKey || pass != bbConsumerSecret {
			http.Error(w, "bad consumer", http.StatusUnauthorized)
			return
		}
		switch r.FormValue("grant_type") {
		case "authorization_code":
			if r.FormValue("code") != "from-bitbucket" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"` + bbAccessToken + `","refresh_token":"` + bbRefreshToken + `","expires_in":7200,"token_type":"bearer"}`))
		case "refresh_token":
			if got := r.FormValue("refresh_token"); got != bbRefreshToken && got != bbRefreshRotated {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"` + bbAccessRefreshed + `","refresh_token":"` + bbRefreshRotated + `","expires_in":7200,"token_type":"bearer"}`))
		default:
			http.Error(w, "unsupported", http.StatusBadRequest)
		}
	})
	mux.HandleFunc("GET /user", guarded(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"uuid":"{7c5e2f1a-9d3b-4c60-8a41-5b2f6e9d0c13}","display_name":"Ada Lovelace"}`))
	}))
	mux.HandleFunc("GET /user/workspaces", guarded(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"values":[{"workspace":{"uuid":"{3f1c2a4e-0b7d-4e5a-9c18-6d4a7b2e8f90}","slug":"acme","name":"Acme","links":{"avatar":{"href":"https://bitbucket.org/avatar.png"}}}}]}`))
	}))
	mux.HandleFunc("GET /repositories/acme", guarded(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"values":[{"full_name":"acme/api","is_private":true,"mainbranch":{"name":"trunk"}}]}`))
	}))
	mux.HandleFunc("GET /repositories/acme/api", guarded(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"mainbranch":{"name":"trunk"}}`))
	}))
	mux.HandleFunc("GET /repositories/acme/api/refs/branches/trunk", guarded(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"target":{"hash":"` + exampleSHA + `"}}`))
	}))
	mux.HandleFunc("GET /acme/api/get/"+exampleSHA+".tar.gz", guarded(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tgz)
	}))
	for _, add := range extra {
		add(mux, guarded)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// bitbucketApp is a running Loomplane and worker over the fake Bitbucket,
// signed in as a person who may connect workspaces. Every response body it
// reads is kept in seen.
type bitbucketApp struct {
	t    *testing.T
	c    *client
	cfg  config.Config
	seen []string
}

// startBitbucketApp starts the API and a worker. With a consumer, the fake
// is configured as Loomplane's OAuth consumer.
func startBitbucketApp(t *testing.T, consumer bool) *bitbucketApp {
	t.Helper()
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.Analysis.WorkDir = t.TempDir()
	bb := fakePrivateBitbucket(t)
	cfg.Bitbucket.APIURL, cfg.Bitbucket.WebURL = bb.URL, bb.URL
	if consumer {
		cfg.Bitbucket.ClientID, cfg.Bitbucket.ClientSecret = bbConsumerKey, bbConsumerSecret
	}
	r := start(t, cfg)
	t.Cleanup(r.stop)
	worker, err := platform.NewWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Start(t.Context()); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	t.Cleanup(func() {
		if serr := worker.Stop(context.Background()); serr != nil {
			t.Errorf("stop worker: %v", serr)
		}
	})
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	return &bitbucketApp{t: t, c: c, cfg: cfg}
}

// call makes a request and returns the status and body, keeping the body.
func (a *bitbucketApp) call(method, path string, body any) (int, string) {
	a.t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(a.t.Context(), method, a.c.base+path, rd)
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Publishable-Key", a.c.key)
	req.Header.Set("Authorization", "Bearer "+a.c.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		a.t.Fatal(err)
	}
	a.seen = append(a.seen, string(raw))
	return res.StatusCode, string(raw)
}

// must is call that requires a status and decodes a JSON body into out.
func (a *bitbucketApp) must(want int, method, path string, body, out any) {
	a.t.Helper()
	got, raw := a.call(method, path, body)
	if got != want {
		a.t.Fatalf("%s %s: status %d, want %d: %s", method, path, got, want, raw)
	}
	if out != nil && raw != "" {
		if err := json.Unmarshal([]byte(raw), out); err != nil {
			a.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
}

// connect plays the whole sign-in: the link Loomplane gives, and what the
// callback carries back.
func (a *bitbucketApp) connect() {
	a.t.Helper()
	var link struct {
		URL string `json:"url"`
	}
	a.must(http.StatusOK, http.MethodPost, "/api/v1/bitbucket/authorize", map[string]string{"workspace": "acme"}, &link)
	u, err := url.Parse(link.URL)
	if err != nil || u.Query().Get("client_id") != bbConsumerKey || u.Query().Get("state") == "" {
		a.t.Fatalf("authorize link %q (%v)", link.URL, err)
	}
	a.must(http.StatusCreated, http.MethodPost, "/api/v1/bitbucket/workspaces", map[string]string{"code": "from-bitbucket", "state": u.Query().Get("state")}, nil)
}

// attach makes a project from bitbucket.org/acme/api and returns its ID.
func (a *bitbucketApp) attach() string {
	a.t.Helper()
	var proj struct {
		ID string `json:"id"`
	}
	a.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Api", "repository_url": "https://bitbucket.org/acme/api"}, &proj)
	return proj.ID
}

// analyze queues one analysis and waits for it to end, whichever way.
func (a *bitbucketApp) analyze(projectID string) analysisBody {
	a.t.Helper()
	var queued struct {
		Analyses []analysisBody `json:"analyses"`
	}
	a.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+projectID+"/analyses", nil, &queued)
	if len(queued.Analyses) != 1 {
		a.t.Fatalf("queued %+v", queued)
	}
	an := queued.Analyses[0]
	deadline := time.Now().Add(readyTimeout)
	for an.Status != "succeeded" && an.Status != "failed" {
		if time.Now().After(deadline) {
			a.t.Fatalf("analysis still %s", an.Status)
		}
		time.Sleep(100 * time.Millisecond)
		a.must(http.StatusOK, http.MethodGet, "/api/v1/analyses/"+an.ID, nil, &an)
	}
	return an
}

type bitbucketStatusBody struct {
	Configured bool `json:"configured"`
	CanConnect bool `json:"can_connect"`
	Workspaces []struct {
		ID          string `json:"id"`
		Workspace   string `json:"workspace"`
		Name        string `json:"name"`
		ConnectedBy string `json:"connected_by"`
		Status      string `json:"status"`
	} `json:"workspaces"`
}

func TestBitbucket_aConnectedWorkspaceReadsPrivateRepositories(t *testing.T) {
	a := startBitbucketApp(t, true)

	var st bitbucketStatusBody
	a.must(http.StatusOK, http.MethodGet, "/api/v1/bitbucket", nil, &st)
	if !st.Configured || !st.CanConnect || len(st.Workspaces) != 0 {
		t.Fatalf("status before connecting %+v", st)
	}

	// A forged state proves nothing, and neither does a spent code.
	a.must(http.StatusBadRequest, http.MethodPost, "/api/v1/bitbucket/workspaces", map[string]string{"code": "from-bitbucket", "state": "forged"}, nil)
	a.must(http.StatusBadRequest, http.MethodPost, "/api/v1/bitbucket/authorize", map[string]string{"workspace": "not a slug!"}, nil)
	a.connect()

	a.must(http.StatusOK, http.MethodGet, "/api/v1/bitbucket", nil, &st)
	if len(st.Workspaces) != 1 || st.Workspaces[0].Workspace != "acme" || st.Workspaces[0].Name != "Acme" ||
		st.Workspaces[0].Status != "ready" || st.Workspaces[0].ConnectedBy != "Ada Lovelace" {
		t.Fatalf("status after connecting %+v", st)
	}

	var repos struct {
		Repositories []struct {
			FullName      string `json:"full_name"`
			Private       bool   `json:"private"`
			DefaultBranch string `json:"default_branch"`
			Workspace     string `json:"workspace"`
		} `json:"repositories"`
	}
	a.must(http.StatusOK, http.MethodGet, "/api/v1/bitbucket/repositories", nil, &repos)
	if len(repos.Repositories) != 1 || repos.Repositories[0].FullName != "acme/api" || !repos.Repositories[0].Private ||
		repos.Repositories[0].DefaultBranch != "trunk" || repos.Repositories[0].Workspace != st.Workspaces[0].ID {
		t.Fatalf("repositories %+v", repos)
	}

	an := a.analyze(a.attach())
	if an.Status != "succeeded" || an.CommitSHA != exampleSHA || an.Summary.Services == 0 {
		t.Fatalf("analysis of the private repository %+v", an)
	}
}

func TestBitbucket_notConfiguredReadsPublicOnly(t *testing.T) {
	a := startBitbucketApp(t, false)

	var st bitbucketStatusBody
	a.must(http.StatusOK, http.MethodGet, "/api/v1/bitbucket", nil, &st)
	if st.Configured || len(st.Workspaces) != 0 {
		t.Fatalf("status %+v", st)
	}
	status, body := a.call(http.MethodPost, "/api/v1/bitbucket/authorize", map[string]string{"workspace": "acme"})
	if status != http.StatusConflict || !strings.Contains(body, "no Bitbucket OAuth consumer") {
		t.Fatalf("authorize: status %d, body %s", status, body)
	}
	var repos struct {
		Repositories []any `json:"repositories"`
	}
	a.must(http.StatusOK, http.MethodGet, "/api/v1/bitbucket/repositories", nil, &repos)
	if len(repos.Repositories) != 0 {
		t.Fatalf("repositories %+v", repos)
	}
}

func TestBitbucket_disconnectStopsPrivateReads(t *testing.T) {
	a := startBitbucketApp(t, true)
	a.connect()
	project := a.attach()
	if first := a.analyze(project); first.Status != "succeeded" {
		t.Fatalf("analysis while connected %+v", first)
	}

	var st bitbucketStatusBody
	a.must(http.StatusOK, http.MethodGet, "/api/v1/bitbucket", nil, &st)
	if len(st.Workspaces) != 1 {
		t.Fatalf("status %+v", st)
	}
	a.must(http.StatusNoContent, http.MethodDelete, "/api/v1/bitbucket/workspaces/"+st.Workspaces[0].ID, nil, nil)
	a.must(http.StatusNotFound, http.MethodDelete, "/api/v1/bitbucket/workspaces/"+st.Workspaces[0].ID, nil, nil)
	a.must(http.StatusOK, http.MethodGet, "/api/v1/bitbucket", nil, &st)
	if len(st.Workspaces) != 0 {
		t.Fatalf("status after disconnecting %+v", st)
	}

	second := a.analyze(project)
	if second.Status != "failed" || !strings.Contains(second.Error, "can't read it") {
		t.Fatalf("analysis after disconnecting %+v", second)
	}
}

func TestBitbucket_noTokenInResponses(t *testing.T) {
	a := startBitbucketApp(t, true)
	a.connect()
	project := a.attach()
	if an := a.analyze(project); an.Status != "succeeded" {
		t.Fatalf("analysis %+v", an)
	}
	var st bitbucketStatusBody
	a.must(http.StatusOK, http.MethodGet, "/api/v1/bitbucket", nil, &st)
	a.must(http.StatusOK, http.MethodGet, "/api/v1/bitbucket/repositories", nil, nil)
	a.call(http.MethodPost, "/api/v1/bitbucket/workspaces", map[string]string{"code": "from-bitbucket", "state": "replayed"})
	a.must(http.StatusNoContent, http.MethodDelete, "/api/v1/bitbucket/workspaces/"+st.Workspaces[0].ID, nil, nil)
	a.analyze(project)

	if len(a.seen) < 6 {
		t.Fatalf("only %d responses were read", len(a.seen))
	}
	for _, secret := range []string{bbAccessToken, bbRefreshToken, bbAccessRefreshed, bbRefreshRotated, bbConsumerSecret} {
		for i, body := range a.seen {
			if strings.Contains(body, secret) {
				t.Fatalf("response %d holds a Bitbucket secret: %s", i, body)
			}
		}
	}
}
