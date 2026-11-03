//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

func TestGitLab_aPublicProjectIsAnalyzed(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitLab.APIURL = fakeGitLabProjects(t, map[string][]byte{
		"grp/sub/api": tarball(t, "api-"+exampleSHA+"-"+exampleSHA, siteFiles),
	}).URL
	cfg.Analysis.WorkDir = t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)

	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Lab"}, &proj)
	base := "/api/v1/projects/" + proj.ID + "/sources"

	var api sourceBody
	c.must(http.StatusCreated, http.MethodPost, base, map[string]string{"repository": "https://gitlab.com/grp/sub/api", "alias": "api"}, &api)
	if api.Repository != "gitlab.com/grp/sub/api" {
		t.Fatalf("attached source %+v, want gitlab.com/grp/sub/api", api)
	}
	// A project the fake GitLab doesn't serve can't be read, so it isn't added.
	c.must(http.StatusUnprocessableEntity, http.MethodPost, base, map[string]string{"repository": "https://gitlab.com/grp/sub/nowhere"}, nil)

	analyses := analyzeAll(t, cfg, c, proj.ID)
	if len(analyses) != 1 || analyses[0].Source != "api" || analyses[0].Status != "succeeded" ||
		analyses[0].CommitSHA != exampleSHA || analyses[0].Summary.Services == 0 {
		t.Fatalf("analyses %+v", analyses)
	}

	var list struct {
		Sources []sourceBody `json:"sources"`
	}
	c.must(http.StatusOK, http.MethodGet, base, nil, &list)
	if len(list.Sources) != 1 || list.Sources[0].Repository != "gitlab.com/grp/sub/api" {
		t.Fatalf("sources %+v", list.Sources)
	}
}
