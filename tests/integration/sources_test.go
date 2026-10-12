//go:build integration

package integration

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

type sourceBody struct {
	ID         string `json:"id"`
	Alias      string `json:"alias"`
	Repository string `json:"repository"`
	Root       string `json:"root"`
	Ref        string `json:"ref"`
}

// signedIn signs up, makes an organization and switches to it.
func signedIn(t *testing.T, c *client) {
	t.Helper()
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
}

func TestSources_aProjectBuildsFromSeveralRepositories(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubRepos(t, map[string][]byte{
		"acme/mono": exampleTarball(t),
		"acme/site": tarball(t, "acme-site-fedcba9", siteFiles),
	}).URL
	cfg.Analysis.WorkDir = t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)

	var proj struct {
		ID           string   `json:"id"`
		Repositories []string `json:"repositories"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Shop", "repository_url": "https://github.com/acme/mono"}, &proj)
	if !slices.Equal(proj.Repositories, []string{"acme/mono"}) {
		t.Fatalf("new project's repositories %v", proj.Repositories)
	}
	base := "/api/v1/projects/" + proj.ID + "/sources"

	var site sourceBody
	c.must(http.StatusCreated, http.MethodPost, base, map[string]string{"repository": "acme/site", "alias": "site"}, &site)
	if site.Alias != "site" || site.Repository != "acme/site" {
		t.Fatalf("site %+v", site)
	}
	// One the fake GitHub doesn't serve can't be read, so it isn't added.
	c.must(http.StatusUnprocessableEntity, http.MethodPost, base, map[string]string{"repository": "acme/nowhere"}, nil)
	c.must(http.StatusConflict, http.MethodPost, base, map[string]string{"repository": "acme/other", "alias": "mono"}, nil)
	c.must(http.StatusConflict, http.MethodPost, base, map[string]string{"repository": "acme/mono", "alias": "api", "root": "services"}, nil)

	var list struct {
		Sources []sourceBody `json:"sources"`
	}
	c.must(http.StatusOK, http.MethodGet, base, nil, &list)
	if len(list.Sources) != 2 || list.Sources[0].Alias != "mono" || list.Sources[1].Alias != "site" {
		t.Fatalf("sources %+v", list.Sources)
	}

	analyses := analyzeAll(t, cfg, c, proj.ID)
	if len(analyses) != 2 || analyses[0].Source != "mono" || analyses[1].Source != "site" || analyses[1].Summary.Services == 0 {
		t.Fatalf("analyses %+v", analyses)
	}

	var view struct {
		Graph struct {
			Services []struct {
				ID     string `json:"id"`
				Source string `json:"source"`
			} `json:"services"`
			Sources []struct {
				Alias     string `json:"alias"`
				CommitSHA string `json:"commit_sha"`
			} `json:"sources"`
		} `json:"graph"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+proj.ID+"/graph", nil, &view)
	if len(view.Graph.Sources) != 2 || view.Graph.Sources[1].CommitSHA != exampleSHA ||
		!slices.ContainsFunc(view.Graph.Services, func(s struct {
			ID     string `json:"id"`
			Source string `json:"source"`
		}) bool {
			return s.ID == "svc:site:node:."
		}) {
		t.Fatalf("graph %+v", view.Graph)
	}

	c.must(http.StatusOK, http.MethodPatch, base+"/site", map[string]string{"ref": "main"}, &site)
	if site.Ref != "main" {
		t.Fatalf("updated site %+v", site)
	}
	c.must(http.StatusNoContent, http.MethodDelete, base+"/site", nil, nil)
	c.must(http.StatusNotFound, http.MethodDelete, base+"/site", nil, nil)
	c.must(http.StatusConflict, http.MethodPost, base, map[string]string{"repository": "acme/site", "alias": "site"}, nil)
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+proj.ID, nil, &proj)
	if !slices.Equal(proj.Repositories, []string{"acme/mono"}) {
		t.Fatalf("repositories after removing site %v", proj.Repositories)
	}
}

func TestSources_aRepositoryAttachedTwiceIsListedOnce(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.GitHub.APIURL = fakeGitHubRepos(t, map[string][]byte{
		"acme/mono": exampleTarball(t),
	}).URL
	cfg.Analysis.WorkDir = t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)

	var proj struct {
		ID           string   `json:"id"`
		Repositories []string `json:"repositories"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Monorepo"}, &proj)
	base := "/api/v1/projects/" + proj.ID + "/sources"
	c.must(http.StatusCreated, http.MethodPost, base, map[string]string{"repository": "acme/mono", "alias": "platform", "root": "services"}, nil)
	c.must(http.StatusCreated, http.MethodPost, base, map[string]string{"repository": "acme/mono", "alias": "apps", "root": "apps"}, nil)

	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+proj.ID, nil, &proj)
	if !slices.Equal(proj.Repositories, []string{"acme/mono"}) {
		t.Fatalf("repositories %v, want acme/mono once", proj.Repositories)
	}
}
