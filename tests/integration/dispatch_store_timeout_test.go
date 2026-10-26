//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/config"
)

// A lock on Dispatch's task table that outlasts the durable worker's
// store timeout makes its next store call time out, the way an
// overloaded Postgres did. Loomplane doesn't set the worker's timing, so
// this relies on Dispatch's defaults: a 30-second lease and a store
// timeout of min(5s, lease/4).
func TestDispatch_aStoreTimeoutDoesNotStopDurableWork(t *testing.T) {
	last := time.Now()
	took := func(what string) {
		t.Logf("%s took %s", what, time.Since(last).Round(100*time.Millisecond))
		last = time.Now()
	}
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
	took("starting the app")
	app, ok := r.app.(forge.App)
	if !ok {
		t.Fatal("the running app has no extensions")
	}
	jobs, err := app.GetExtension("dispatch")
	if err != nil {
		t.Fatal(err)
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
	var proj struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Mono", "repository_url": "https://github.com/acme/mono"}, &proj)
	base := "/api/v1/projects/" + proj.ID
	took("signing up")
	analyze(t, cfg, c, proj.ID)
	took("analysis")
	var draft struct {
		YAML string `json:"yaml"`
	}
	c.must(http.StatusOK, http.MethodGet, base+"/manifest/draft", nil, &draft)
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": draft.YAML}, nil)

	create := func() string {
		t.Helper()
		var p planBody
		c.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &p)
		return p.ID
	}
	// planned waits for a plan to settle. The project has no cloud
	// connections, so a plan that ran ends blocked; ready is fine too.
	// A plan the failed worker had claimed waits out its 30-second lease
	// before a replacement claims it, so the wait allows for that.
	planned := func(id string) {
		t.Helper()
		var p planBody
		deadline := time.Now().Add(90 * time.Second)
		for {
			c.must(http.StatusOK, http.MethodGet, "/api/v1/plans/"+id, nil, &p)
			if p.Status != "planning" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("plan %s still planning after the store timeout", id)
			}
			time.Sleep(200 * time.Millisecond)
		}
		if (p.Status != "blocked" && p.Status != "ready") || p.Error != "" {
			t.Fatalf("plan %s ended %q: %s", id, p.Status, p.Error)
		}
	}
	planned(create())
	took("the first plan")

	conn, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	// lockTasks locks Dispatch's task table, and gives up straight away
	// when still is false once the lock is held. Otherwise it holds the
	// lock until a Dispatch query is waiting on it and the durable worker
	// has stopped on the timeout, which proves the failure was injected.
	lockTasks := func(still func(pgx.Tx) bool) bool {
		t.Helper()
		tx, txErr := conn.Begin(t.Context())
		if txErr != nil {
			t.Fatal(txErr)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, txErr = tx.Exec(t.Context(), "SET LOCAL lock_timeout = '30s'"); txErr != nil {
			t.Fatal(txErr)
		}
		if _, txErr = tx.Exec(t.Context(), "LOCK TABLE dispatch_execution_tasks IN ACCESS EXCLUSIVE MODE"); txErr != nil {
			t.Fatal(txErr)
		}
		if still != nil && !still(tx) {
			return false
		}
		waiting, stopped := false, false
		deadline := time.Now().Add(20 * time.Second)
		for !waiting || !stopped {
			if time.Now().After(deadline) {
				t.Fatalf("no injected failure after 20s: a query waiting on the lock %t, the worker stopped %t", waiting, stopped)
			}
			if _, txErr = tx.Exec(t.Context(), "SELECT pg_stat_clear_snapshot()"); txErr != nil {
				t.Fatal(txErr)
			}
			var blocked int
			if txErr = tx.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database()
				AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%dispatch_execution_tasks%'`).Scan(&blocked); txErr != nil {
				t.Fatal(txErr)
			}
			waiting = waiting || blocked > 0
			if health := jobs.Health(t.Context()); health != nil && strings.Contains(health.Error(), "durable workflow worker stopped") {
				stopped = true
			}
			time.Sleep(50 * time.Millisecond)
		}
		// Both workers' claims waited on the same lock; give the second
		// timeout a moment to land too.
		time.Sleep(500 * time.Millisecond)
		if txErr = tx.Commit(t.Context()); txErr != nil {
			t.Fatal(txErr)
		}
		return true
	}
	// replaced waits for the health check to pass again. A stopped worker
	// never runs again, so that means the supervisor replaced it.
	replaced := func() {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for jobs.Health(t.Context()) != nil {
			if time.Now().After(deadline) {
				t.Fatalf("dispatch still unhealthy after the lock was released: %v", jobs.Health(t.Context()))
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// The worker times out while it's idle; a plan made afterwards is planned.
	lockTasks(nil)
	planned(create())
	replaced()
	took("the idle timeout")

	// The worker times out while it holds a plan's activity. That plan is
	// planned once its lease runs out and a replacement claims it again.
	inFlight := ""
	for attempt := 0; attempt < 5 && inFlight == ""; attempt++ {
		posted := make(chan error, 1)
		go func() { posted <- postPlan(t.Context(), c, base) }()
		id := claimedPlanActivity(t, conn, posted)
		if id != "" && lockTasks(func(tx pgx.Tx) bool {
			var claimed bool
			scanErr := tx.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM dispatch_execution_tasks WHERE workflow_id=$1
				AND kind='activity' AND NOT done AND lease_until > clock_timestamp())`, id).Scan(&claimed)
			if scanErr != nil {
				t.Fatal(scanErr)
			}
			return claimed
		}) {
			inFlight = id
			t.Logf("held the lock while a worker held plan %s's activity (attempt %d)", id, attempt+1)
		}
		if postErr := <-posted; postErr != nil {
			t.Fatal(postErr)
		}
	}
	if inFlight == "" {
		t.Fatal("never caught a plan's activity while it was claimed")
	}
	planned(inFlight)
	replaced()
	took("the in-flight timeout")
}

// postPlan asks for a plan without failing the test, so it can run on
// its own goroutine while the test watches Dispatch's task table.
func postPlan(ctx context.Context, c *client, base string) error {
	body, err := json.Marshal(map[string]string{"environment": "production"})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+base+"/plans", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Publishable-Key", c.key)
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		return fmt.Errorf("create plan: status %d", res.StatusCode)
	}
	return nil
}

// claimedPlanActivity returns the plan whose activity a worker holds now,
// or "" if the plan request ended without one being seen.
func claimedPlanActivity(t *testing.T, conn *pgx.Conn, posted <-chan error) string {
	t.Helper()
	settled := time.Time{}
	for {
		var id string
		err := conn.QueryRow(t.Context(), `SELECT workflow_id FROM dispatch_execution_tasks WHERE starts_with(workflow_id, 'plan_')
			AND kind='activity' AND NOT done AND lease_until > clock_timestamp() LIMIT 1`).Scan(&id)
		switch {
		case err == nil:
			return id
		case !errors.Is(err, pgx.ErrNoRows):
			t.Fatal(err)
		}
		if settled.IsZero() && len(posted) > 0 {
			settled = time.Now()
		}
		// The request can return before the activity is claimed, so keep
		// looking for a moment after it does.
		if !settled.IsZero() && time.Since(settled) > 2*time.Second {
			return ""
		}
		time.Sleep(5 * time.Millisecond)
	}
}
