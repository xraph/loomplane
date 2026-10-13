//go:build integration

package integration

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

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

func TestPlanning_aScopedPlanHoldsWhatsDeployed(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubRepos(t, map[string][]byte{
		"acme/mono": exampleTarball(t),
		"acme/site": tarball(t, "acme-site-fedcba9", siteFiles),
	}).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Providers.Paused = []string{"render"}
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)

	var proj struct {
		ID    string `json:"id"`
		OrgID string `json:"org_id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Shop", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	c.must(http.StatusCreated, http.MethodPost, base+"/sources", map[string]string{"repository": "acme/site", "alias": "site"}, nil)
	analyzeAll(t, cfg, c, proj.ID)
	// Render is paused, so the plans need no cloud connection and stay
	// ready for review: what this test reads is what they pin.
	manifest := "version: 1\ndefaults:\n  provider: render\nservices:\n  api:\n    repo: mono\n    source: services/api\n  site:\n    repo: site\n    source: .\n"
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": manifest}, nil)

	var full planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]any{"environment": "production"}, &full)
	waitPlanned(t, c, &full)
	if full.Status != "ready" || len(full.Sources) != 2 || !full.Sources[0].InScope || !full.Sources[1].InScope {
		t.Fatalf("full plan %+v", full)
	}

	// The scope picker reads what's deployed to lock on what isn't.
	type deployedBody struct {
		Sources []struct {
			SourceID   string    `json:"source_id"`
			Alias      string    `json:"alias"`
			Repository string    `json:"repository"`
			CommitSHA  string    `json:"commit_sha"`
			PlanID     string    `json:"plan_id"`
			DeployedAt time.Time `json:"deployed_at"`
		} `json:"sources"`
	}
	var deployed deployedBody
	c.must(http.StatusOK, http.MethodGet, base+"/environments/production/deployed", nil, &deployed)
	if deployed.Sources == nil || len(deployed.Sources) != 0 {
		t.Fatalf("deployed before anything was %+v", deployed)
	}
	c.must(http.StatusNotFound, http.MethodGet, base+"/environments/nowhere/deployed", nil, nil)

	// Nothing of mono is deployed, so a plan that holds it can't be made.
	c.must(http.StatusPreconditionFailed, http.MethodPost, base+"/plans", map[string]any{"environment": "production", "scope": []string{"site"}}, nil)
	c.must(http.StatusUnprocessableEntity, http.MethodPost, base+"/plans", map[string]any{"environment": "production", "scope": []string{"gone"}}, nil)

	// What milestone 4's executor will write once a deployment succeeds.
	conn, err := pgx.Connect(context.Background(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	mono := full.Sources[0]
	if _, err = conn.Exec(context.Background(), `INSERT INTO loomplane_environment_sources
		(environment_id, source_id, org_id, analysis_id, commit_sha, plan_id, deployed_at) VALUES ($1, $2, $3, $4, $5, $6, now())`,
		full.EnvironmentID, mono.SourceID, proj.OrgID, mono.AnalysisID, mono.CommitSHA, full.ID); err != nil {
		t.Fatal(err)
	}
	c.must(http.StatusOK, http.MethodGet, base+"/environments/production/deployed", nil, &deployed)
	if len(deployed.Sources) != 1 {
		t.Fatalf("deployed %+v", deployed)
	}
	if d := deployed.Sources[0]; d.SourceID != mono.SourceID || d.Alias != "mono" || d.Repository != "acme/mono" ||
		d.CommitSHA != mono.CommitSHA || d.PlanID != full.ID || d.DeployedAt.IsZero() {
		t.Fatalf("deployed %+v", d)
	}

	var scoped planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]any{"environment": "production", "scope": []string{"site"}}, &scoped)
	waitPlanned(t, c, &scoped)
	if scoped.Status != "ready" || scoped.Sources[0].Alias != "mono" || scoped.Sources[0].InScope || scoped.Sources[0].CommitSHA != mono.CommitSHA || !scoped.Sources[1].InScope {
		t.Fatalf("scoped plan %+v", scoped)
	}
}
