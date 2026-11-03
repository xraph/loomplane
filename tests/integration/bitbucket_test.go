//go:build integration

package integration

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
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
