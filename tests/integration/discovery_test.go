//go:build integration

package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/platform"
)

const exampleSHA = "fedcba9876543210fedcba9876543210fedcba98"

// exampleTarball is examples/polyglot as a GitHub tarball of one commit.
func exampleTarball(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	// Compiled acceptance binaries run from the repository root. Resolve the
	// fixture from its source location so callers keep their configuration cwd.
	_, source, _, ok := runtime.Caller(0)
	if !ok || !filepath.IsAbs(source) {
		t.Fatal("repository fixture source location unavailable")
	}
	root := filepath.Join(filepath.Dir(source), "..", "..", "examples", "polyglot")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if err = tw.WriteHeader(&tar.Header{Name: "acme-mono-fedcba9/" + filepath.ToSlash(rel), Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			return err
		}
		_, err = tw.Write(body)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeGitHubRepos serves each tarball as the repository its key names, on
// a main branch at exampleSHA, the way GitHub's API serves a public
// repository.
func fakeGitHubRepos(t *testing.T, repos map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, tgz := range repos {
			switch r.URL.Path {
			case "/repos/" + name:
				_, _ = w.Write([]byte(`{"default_branch":"main"}`))
				return
			case "/repos/" + name + "/commits/main":
				_, _ = w.Write([]byte(exampleSHA))
				return
			case "/repos/" + name + "/tarball/" + exampleSHA:
				_, _ = w.Write(tgz)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeGitHubServing serves examples/polyglot as acme/mono.
func fakeGitHubServing(t *testing.T) *httptest.Server {
	t.Helper()
	return fakeGitHubRepos(t, map[string][]byte{"acme/mono": exampleTarball(t)})
}

// tarball packs files the way GitHub's tarball endpoint does: under one
// top directory.
func tarball(t *testing.T, top string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: top + "/" + name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// siteFiles is a Next.js app in a repository of its own, which uses a
// package published from elsewhere.
var siteFiles = map[string]string{
	"package.json": `{"name": "site", "scripts": {"build": "next build", "start": "next start"},
  "dependencies": {"next": "15.0.0", "react": "19.0.0", "@acme/ui": "^1.0.0"}}`,
	"app/page.tsx": "export default function Page() { return null }\n",
}

type analysisBody struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Status    string `json:"status"`
	CommitSHA string `json:"commit_sha"`
	Error     string `json:"error"`
	Summary   struct {
		Services   int `json:"services"`
		Deployable int `json:"deployable"`
		Packages   int `json:"packages"`
		Resources  int `json:"resources"`
	} `json:"summary"`
}

func TestDiscovery_theWorkerAnalyzesAProjectsRepository(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	r := start(t, cfg)
	defer r.stop()

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

	var mono struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &mono)
	// Only GitHub is supported, so a GitLab project isn't created at all.
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Lab", "repository_url": "https://gitlab.com/acme/lab"}, nil)

	var queued struct {
		Analyses []analysisBody `json:"analyses"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+mono.ID+"/analyses", nil, &queued)
	if len(queued.Analyses) != 1 {
		t.Fatalf("queued %+v", queued)
	}
	a := queued.Analyses[0]
	if a.Status != "queued" {
		t.Fatalf("new analysis %+v", a)
	}
	// The API never runs an analysis itself: with no worker, it waits.
	time.Sleep(time.Second)
	c.must(http.StatusOK, http.MethodGet, "/api/v1/analyses/"+a.ID, nil, &a)
	if a.Status != "queued" {
		t.Fatalf("with no worker running, the analysis is %s", a.Status)
	}

	// The worker is its own app on the same database, as loomplane-worker
	// would be.
	worker, err := platform.NewWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	if err = worker.Start(ctx); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	workerRunning := true
	stopWorker := func() {
		if !workerRunning {
			return
		}
		workerRunning = false
		if serr := worker.Stop(context.Background()); serr != nil {
			t.Errorf("stop worker: %v", serr)
		}
	}
	defer stopWorker()
	deadline := time.Now().Add(readyTimeout)
	for a.Status != "succeeded" && a.Status != "failed" {
		if time.Now().After(deadline) {
			t.Fatalf("analysis still %s after %s", a.Status, readyTimeout)
		}
		time.Sleep(100 * time.Millisecond)
		c.must(http.StatusOK, http.MethodGet, "/api/v1/analyses/"+a.ID, nil, &a)
	}
	if a.Status != "succeeded" || a.CommitSHA != exampleSHA || a.Summary.Services != 7 || a.Summary.Packages != 3 || a.Summary.Resources != 2 {
		t.Fatalf("finished analysis %+v", a)
	}

	var g struct {
		ID    string `json:"id"`
		Graph struct {
			Services []struct {
				Name     string `json:"name"`
				Kind     string `json:"kind"`
				Evidence []struct {
					Path string `json:"path"`
					Line int    `json:"line"`
				} `json:"evidence"`
			} `json:"services"`
		} `json:"graph"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/analyses/"+a.ID+"/graph", nil, &g)
	if g.ID != a.ID || len(g.Graph.Services) != 7 {
		t.Fatalf("graph %+v", g)
	}
	api := g.Graph.Services[0]
	if api.Name != "api" || api.Kind != "http-service" || len(api.Evidence) == 0 || api.Evidence[0].Path != "services/api/main.go" {
		t.Fatalf("api service %+v", api)
	}

	var list struct {
		Analyses []analysisBody `json:"analyses"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+mono.ID+"/analyses", nil, &list)
	if len(list.Analyses) != 1 || list.Analyses[0].ID != a.ID {
		t.Fatalf("list %+v", list)
	}
	// Finished, it no longer blocks another. With the worker stopped the
	// new one stays queued, and a queued analysis blocks a third; with it
	// running, the fake repository is analyzed so fast the second could
	// finish before the third arrives.
	stopWorker()
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+mono.ID+"/analyses", map[string]string{"ref": "main"}, nil)
	c.must(http.StatusConflict, http.MethodPost, "/api/v1/projects/"+mono.ID+"/analyses", nil, nil)
}
