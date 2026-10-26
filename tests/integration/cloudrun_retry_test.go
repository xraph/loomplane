//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

func TestCloudRunFailedBuildRetrySurvivesAPIRestart(t *testing.T) {
	google := newFakeGoogle(t)
	cloud := newFakeCloud(google.srv.URL)
	cloud.failBuilds = true
	google.deploy = cloud
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.GoogleEndpoint = google.srv.URL
	app := start(t, cfg)
	defer func() { app.stop() }()
	c := &client{t: t, base: app.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	var project struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Cloud Run retry", "repository_url": "https://github.com/acme/mono"}, &project)
	base := "/api/v1/projects/" + project.ID
	analyze(t, cfg, c, project.ID)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]string{"cloud": "gcp", "name": "Prod", "credential": serviceAccountKey(t, google.srv.URL+"/token")}, nil)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": apiOnCloudRun}, nil)
	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if !plan.Executable || plan.Status != "ready" {
		t.Fatalf("plan was not executable: %+v", plan)
	}
	d := approveAndWait(t, c, plan)
	if d.Status != "failed" || !strings.Contains(d.Error, "step 0 exited 1") {
		t.Fatalf("expected failed build: %+v", d)
	}
	app.stop()
	cloud.mu.Lock()
	cloud.failBuilds = false
	cloud.mu.Unlock()
	app = start(t, cfg)
	c.base = app.srv.URL
	c.must(http.StatusOK, http.MethodPost, "/api/v1/deployments/"+d.ID+"/retry", nil, &d)
	deadline := time.Now().Add(readyTimeout)
	for d.Status == "running" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		c.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+d.ID, nil, &d)
	}
	if d.Status != "succeeded" {
		t.Fatalf("retry did not recover the failed build: %+v", d)
	}
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	if len(cloud.builds) != 2 || len(cloud.services) != 1 {
		t.Fatal("unexpected build or service duplication", len(cloud.builds), len(cloud.services))
	}
}
