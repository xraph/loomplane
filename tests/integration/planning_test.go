//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/platform"
)

type planBody struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Revision string `json:"revision"`
	Error    string `json:"error"`
	Actions  []struct {
		ID       string `json:"id"`
		Op       string `json:"op"`
		Name     string `json:"name"`
		Provider string `json:"provider"`
		Step     int    `json:"step"`
	} `json:"actions"`
	Problems []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"problems"`
	Warnings []struct {
		Code string `json:"code"`
		Name string `json:"name"`
	} `json:"warnings"`
	Executable   bool   `json:"executable"`
	DecidedBy    string `json:"decided_by"`
	DecisionNote string `json:"decision_note"`
}

// analyze runs one analysis of each of a project's sources with a worker
// of its own, and stops the worker once they're done.
func analyze(t *testing.T, cfg config.Config, c *client, projectID string) {
	t.Helper()
	analyzeAll(t, cfg, c, projectID)
}

func analyzeAll(t *testing.T, cfg config.Config, c *client, projectID string) []analysisBody {
	t.Helper()
	worker, err := platform.NewWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	if err = worker.Start(ctx); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	defer func() {
		if serr := worker.Stop(context.Background()); serr != nil {
			t.Errorf("stop worker: %v", serr)
		}
	}()
	var list struct {
		Analyses []analysisBody `json:"analyses"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+projectID+"/analyses", nil, &list)
	deadline := time.Now().Add(readyTimeout)
	for i := range list.Analyses {
		a := &list.Analyses[i]
		for a.Status != "succeeded" && a.Status != "failed" {
			if time.Now().After(deadline) {
				t.Fatalf("analysis of %s still %s after %s", a.Source, a.Status, readyTimeout)
			}
			time.Sleep(100 * time.Millisecond)
			c.must(http.StatusOK, http.MethodGet, "/api/v1/analyses/"+a.ID, nil, a)
		}
		if a.Status != "succeeded" {
			t.Fatalf("analysis of %s failed: %s", a.Source, a.Error)
		}
	}
	return list.Analyses
}

// waitPlanned polls a plan until planning is over.
func waitPlanned(t *testing.T, c *client, p *planBody) {
	t.Helper()
	deadline := time.Now().Add(readyTimeout)
	for p.Status == "planning" {
		if time.Now().After(deadline) {
			t.Fatalf("plan still planning after %s", readyTimeout)
		}
		time.Sleep(100 * time.Millisecond)
		c.must(http.StatusOK, http.MethodGet, "/api/v1/plans/"+p.ID, nil, p)
	}
}

func TestPlanning_aPlanIsReviewedAndApprovedAcrossARestart(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	r := start(t, cfg)

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
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID

	// Nothing to plan from yet: no manifest, and no analysis to draft one.
	c.must(http.StatusNotFound, http.MethodGet, base+"/manifest", nil, nil)
	c.must(http.StatusPreconditionFailed, http.MethodGet, base+"/manifest/draft", nil, nil)
	c.must(http.StatusPreconditionFailed, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, nil)

	var providers struct {
		Providers []struct {
			Name string `json:"name"`
		} `json:"providers"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/providers", nil, &providers)
	if len(providers.Providers) == 0 || providers.Providers[0].Name != "vercel" {
		t.Fatalf("providers %+v", providers)
	}

	analyze(t, cfg, c, proj.ID)

	var draft struct {
		ProjectID string `json:"project_id"`
		YAML      string `json:"yaml"`
	}
	c.must(http.StatusOK, http.MethodGet, base+"/manifest/draft", nil, &draft)
	if draft.ProjectID != proj.ID || !strings.Contains(draft.YAML, "source: go:services/api") {
		t.Fatalf("draft %+v", draft)
	}
	t.Logf("draft:\n%s", draft.YAML)

	// A manifest that doesn't parse is refused, with where it's wrong.
	c.must(http.StatusUnprocessableEntity, http.MethodPut, base+"/manifest", map[string]string{"yaml": "version: 2\n"}, nil)

	var rev struct {
		ID       string `json:"id"`
		Revision int    `json:"revision"`
	}
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": draft.YAML}, &rev)
	if rev.Revision != 1 {
		t.Fatalf("first revision %+v", rev)
	}
	// Saving the same thing again adds nothing to the history.
	c.must(http.StatusOK, http.MethodPut, base+"/manifest", map[string]string{"yaml": draft.YAML}, &rev)
	if rev.Revision != 1 {
		t.Fatalf("resaved revision %+v", rev)
	}

	c.must(http.StatusNotFound, http.MethodPost, base+"/plans", map[string]string{"environment": "staging"}, nil)
	var plan planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if plan.Status != "ready" || plan.Revision == "" || len(plan.Actions) == 0 {
		t.Fatalf("plan %+v", plan)
	}
	for _, a := range plan.Actions {
		if a.Op != "create" {
			t.Fatalf("with nothing deployed, every action creates: %+v", a)
		}
	}

	// Approving needs the revision you reviewed, and only that one.
	c.must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{}, nil)
	c.must(http.StatusConflict, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": "not-it"}, nil)

	// The plan waits for its decision through a restart: the workflow is
	// durable, so nothing holds it in memory.
	r.stop()
	r = start(t, cfg)
	defer r.stop()
	c.base = r.srv.URL

	c.must(http.StatusOK, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": plan.Revision, "note": "Looks right"}, &plan)
	deadline := time.Now().Add(readyTimeout)
	for plan.Status == "ready" {
		if time.Now().After(deadline) {
			t.Fatalf("plan still ready %s after approval", readyTimeout)
		}
		time.Sleep(100 * time.Millisecond)
		c.must(http.StatusOK, http.MethodGet, "/api/v1/plans/"+plan.ID, nil, &plan)
	}
	if plan.Status != "approved" || plan.DecisionNote != "Looks right" || plan.DecidedBy == "" {
		t.Fatalf("approved plan %+v", plan)
	}
	c.must(http.StatusConflict, http.MethodPost, "/api/v1/plans/"+plan.ID+"/reject", map[string]string{}, nil)

	// A new plan for the environment supersedes the open one.
	var first, second planBody
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &first)
	waitPlanned(t, c, &first)
	c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &second)
	c.must(http.StatusOK, http.MethodGet, "/api/v1/plans/"+first.ID, nil, &first)
	if first.Status != "superseded" {
		t.Fatalf("older plan %+v", first)
	}
	c.must(http.StatusConflict, http.MethodPost, "/api/v1/plans/"+first.ID+"/approve", map[string]string{"revision": first.Revision}, nil)

	waitPlanned(t, c, &second)
	c.must(http.StatusOK, http.MethodPost, "/api/v1/plans/"+second.ID+"/reject", map[string]string{"note": "Not today"}, &second)
	deadline = time.Now().Add(readyTimeout)
	for second.Status == "ready" {
		if time.Now().After(deadline) {
			t.Fatalf("plan still ready %s after rejection", readyTimeout)
		}
		time.Sleep(100 * time.Millisecond)
		c.must(http.StatusOK, http.MethodGet, "/api/v1/plans/"+second.ID, nil, &second)
	}
	if second.Status != "rejected" {
		t.Fatalf("rejected plan %+v", second)
	}

	var list struct {
		Plans []planBody `json:"plans"`
	}
	c.must(http.StatusOK, http.MethodGet, base+"/plans?environment=production", nil, &list)
	if len(list.Plans) != 3 || list.Plans[0].ID != second.ID {
		t.Fatalf("plans %+v", list.Plans)
	}
	var bindings struct {
		Bindings []struct{} `json:"bindings"`
	}
	c.must(http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &bindings)
	if len(bindings.Bindings) != 0 {
		t.Fatalf("planning deployed something: %+v", bindings)
	}
}
