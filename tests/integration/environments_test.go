//go:build integration

package integration

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/platform"
)

type variableBody struct {
	ID          string `json:"id"`
	Scope       string `json:"scope"`
	Key         string `json:"key"`
	Value       string `json:"value"`
	Secret      bool   `json:"secret"`
	Environment string `json:"environment"`
}

type resolution struct {
	Variables []struct {
		Key       string         `json:"key"`
		Variable  variableBody   `json:"variable"`
		Overrides []variableBody `json:"overrides"`
	} `json:"variables"`
}

func TestEnvironments_everyProjectHasOneAndVariablesLayer(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
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
	db, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(context.Background()) }()
	secretsIn := func(t *testing.T) int {
		t.Helper()
		var n int
		if qerr := db.QueryRow(t.Context(), `SELECT count(*) FROM vault_secrets WHERE app_id = 'loomplane-variables'`).Scan(&n); qerr != nil {
			t.Fatal(qerr)
		}
		return n
	}

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

	var project struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &project)
	type envList struct {
		Environments []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"environments"`
	}
	var envs envList
	// Straight after creation, before the background job has run.
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project.ID+"/environments", nil, &envs)
	if len(envs.Environments) != 2 || envs.Environments[0].Name != "production" {
		t.Fatalf("a new project's environments: %+v", envs)
	}

	var staging struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+project.ID+"/environments", map[string]string{"name": "staging"}, &staging)
	if staging.Kind != "staging" {
		t.Fatalf("staging is %+v", staging)
	}
	c.must(http.StatusConflict, http.MethodPost, "/api/v1/projects/"+project.ID+"/environments", map[string]string{"name": "staging"}, nil)
	c.must(http.StatusBadRequest, http.MethodPost, "/api/v1/projects/"+project.ID+"/environments", map[string]string{"name": "Not OK"}, nil)

	// Analyze it, so its services can have variables of their own.
	var a analysisBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+project.ID+"/analyses", nil, &a)
	for deadline := time.Now().Add(readyTimeout); a.Status != "succeeded"; time.Sleep(100 * time.Millisecond) {
		if a.Status == "failed" || time.Now().After(deadline) {
			t.Fatalf("analysis %+v", a)
		}
		c.must(http.StatusOK, http.MethodGet, "/api/v1/analyses/"+a.ID, nil, &a)
	}

	set := func(path string, body map[string]any) variableBody {
		t.Helper()
		var v variableBody
		c.must(http.StatusCreated, http.MethodPost, path, body, &v)
		return v
	}
	projectVars := "/api/v1/projects/" + project.ID + "/variables"
	set("/api/v1/variables", map[string]any{"key": "LOG_LEVEL", "value": "info"})
	set("/api/v1/variables", map[string]any{"key": "LOG_LEVEL", "value": "warn", "environment": "production"})
	dbURL := set(projectVars, map[string]any{"key": "DATABASE_URL", "value": "postgres://prod-db/app", "secret": true, "environment": "production"})
	set(projectVars, map[string]any{"key": "PORT", "value": "3000", "service": "svc:node:apps/web"})
	stagingSecret := set(projectVars, map[string]any{"key": "STRIPE_KEY", "value": "sk_test_123", "secret": true, "environment": "staging"})
	c.must(http.StatusUnprocessableEntity, http.MethodPost, projectVars, map[string]any{"key": "PORT", "value": "1", "service": "svc:go:not-there"}, nil)
	c.must(http.StatusConflict, http.MethodPost, projectVars, map[string]any{"key": "PORT", "value": "1", "service": "svc:node:apps/web"}, nil)
	if dbURL.Value != "" || !dbURL.Secret {
		t.Fatalf("a secret came back with its value: %+v", dbURL)
	}

	// The secret is in Vault, encrypted, and nowhere else.
	var alg string
	var stored []byte
	if err = db.QueryRow(t.Context(), `SELECT encryption_alg, encrypted_value FROM vault_secrets WHERE key = $1`, org.ID+"/"+dbURL.ID).
		Scan(&alg, &stored); err != nil {
		t.Fatalf("the secret isn't in Vault: %v", err)
	}
	if alg != "AES-256-GCM" || bytes.Contains(stored, []byte("prod-db")) {
		t.Fatalf("the secret is stored as %s: %q", alg, stored)
	}
	var plainCopies int
	if err = db.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_variables WHERE value LIKE '%prod-db%'`).Scan(&plainCopies); err != nil || plainCopies != 0 {
		t.Fatalf("the secret's value is in the variables table: %d, %v", plainCopies, err)
	}

	var res resolution
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project.ID+"/variables/resolved?"+
		url.Values{"environment": {"production"}, "service": {"svc:node:apps/web"}}.Encode(), nil, &res)
	got := map[string]string{}
	for _, rv := range res.Variables {
		got[rv.Key] = rv.Variable.Scope + ":" + rv.Variable.Value
	}
	if len(got) != 3 || got["LOG_LEVEL"] != "organization:warn" || got["DATABASE_URL"] != "project:" || got["PORT"] != "service:3000" {
		t.Fatalf("production for web: %v", got)
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project.ID+"/variables/resolved?environment=preview", nil, &res)
	if len(res.Variables) != 1 || res.Variables[0].Variable.Value != "info" {
		t.Fatalf("preview for the project: %+v", res.Variables)
	}

	// Deleting staging takes its variables, and their secrets, with it.
	if n := secretsIn(t); n != 2 {
		t.Fatalf("%d secrets before deleting staging", n)
	}
	c.must(http.StatusNoContent, http.MethodDelete, "/api/v1/projects/"+project.ID+"/environments/"+staging.ID, nil, nil)
	c.must(http.StatusNotFound, http.MethodPatch, "/api/v1/variables/"+stagingSecret.ID, map[string]string{"value": "x"}, nil)
	if n := secretsIn(t); n != 1 {
		t.Fatalf("%d secrets after deleting staging", n)
	}

	// Down to one environment, it stays.
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project.ID+"/environments", nil, &envs)
	for _, e := range envs.Environments {
		if e.Name == "preview" {
			c.must(http.StatusNoContent, http.MethodDelete, "/api/v1/projects/"+project.ID+"/environments/"+e.ID, nil, nil)
		}
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+project.ID+"/environments", nil, &envs)
	if len(envs.Environments) != 1 {
		t.Fatalf("environments %+v", envs)
	}
	c.must(http.StatusConflict, http.MethodDelete, "/api/v1/projects/"+project.ID+"/environments/"+envs.Environments[0].ID, nil, nil)

	// The project goes, and so does every secret it had.
	c.must(http.StatusNoContent, http.MethodDelete, "/api/v1/projects/"+project.ID, nil, nil)
	if n := secretsIn(t); n != 0 {
		t.Fatalf("%d secrets outlived their project", n)
	}
	var orgVars struct {
		Variables []variableBody `json:"variables"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/variables", nil, &orgVars)
	if len(orgVars.Variables) != 2 {
		t.Fatalf("organization variables after the project went: %+v", orgVars)
	}
}
