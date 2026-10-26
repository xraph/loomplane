//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/config"
)

// A lock on Dispatch's task table that outlasts the durable worker's
// store timeout makes its next claim time out, the way an overloaded
// Postgres did. Plans made after the lock is gone must still be planned.
func TestDispatch_aStoreTimeoutDoesNotStopDurableWork(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.GitHub.APIURL = fakeGitHubServing(t).URL
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Clouds.GoogleEndpoint = newFakeGoogle(t).srv.URL
	cfg.Clouds.VercelEndpoint = fakeVercel(t, "vcl_0123456789abcdef").URL
	cfg.Clouds.AWSEndpoint = newFakeAWS(t).srv.URL
	cfg.Storage.URL = "mem://"
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
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	analyze(t, cfg, c, proj.ID)
	var draft struct {
		YAML string `json:"yaml"`
	}
	c.must(http.StatusOK, http.MethodGet, base+"/manifest/draft", nil, &draft)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": draft.YAML}, nil)

	create := func() planBody {
		t.Helper()
		var p planBody
		c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &p)
		return p
	}
	// A plan the failed worker had claimed waits out its 30-second lease
	// before a replacement claims it, so allow for that.
	planned := func(p planBody) {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for p.Status == "planning" {
			if time.Now().After(deadline) {
				t.Fatalf("plan %s still planning after the store timeout", p.ID)
			}
			time.Sleep(200 * time.Millisecond)
			c.must(http.StatusOK, http.MethodGet, "/api/v1/plans/"+p.ID, nil, &p)
		}
	}
	planned(create())

	conn, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	// lockTasks holds the lock past the worker's five-second store timeout.
	lockTasks := func() {
		t.Helper()
		tx, txErr := conn.Begin(t.Context())
		if txErr != nil {
			t.Fatal(txErr)
		}
		if _, txErr = tx.Exec(t.Context(), "LOCK TABLE dispatch_execution_tasks IN ACCESS EXCLUSIVE MODE"); txErr != nil {
			t.Fatal(txErr)
		}
		time.Sleep(8 * time.Second)
		if txErr = tx.Commit(t.Context()); txErr != nil {
			t.Fatal(txErr)
		}
	}

	// The worker times out while it's idle; a plan made afterwards is planned.
	lockTasks()
	planned(create())

	// The worker times out with a plan in flight, and a plan made
	// afterwards is planned too.
	inFlight := create()
	lockTasks()
	after := create()
	planned(inFlight)
	planned(after)
}
